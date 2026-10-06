// Command release builds the four supported distributions from the repository root.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type target struct {
	os, arch, extension string
}

var targets = []target{
	{"windows", "amd64", ".zip"},
	{"darwin", "arm64", ".tar.gz"},
	{"linux", "amd64", ".tar.gz"},
	{"linux", "arm64", ".tar.gz"},
}

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$`)

type buildInfo struct {
	commit, dirty, goVersion string
}

type licenseFile struct {
	source, destination string
}

type module struct {
	Path, Version, Dir string
	Main               bool
}

type buildFunc func(target, string) error

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(output)
	version := flags.String("version", "", "release version, e.g. v1.0.0 or v1.0.0-rc.1")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || !versionPattern.MatchString(*version) {
		return errors.New("usage: go run ./scripts/release -version v1.0.0 (or v1.0.0-rc.1)")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, name := range []string{"go.mod", "cmd/minihub/main.go", "release/README.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("run from the minihub repository root: %w", err)
		}
	}
	if err := requireAbsent(filepath.Join(root, "dist", *version)); err != nil {
		return err
	}
	git := func(args ...string) (string, error) {
		data, err := command(root, nil, "git", args...)
		return strings.TrimSpace(string(data)), err
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	status, err := git("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	dirty := "false"
	if status != "" {
		dirty = "true"
		fmt.Fprintln(output, "Working tree has changes; BUILDINFO.txt will record dirty: true.")
	}
	versionData, err := command(root, nil, "go", "version")
	if err != nil {
		return err
	}
	licenses, err := dependencyLicenses(root)
	if err != nil {
		return fmt.Errorf("collect dependency licenses: %w", err)
	}
	build := func(t target, destination string) error {
		fmt.Fprintf(output, "Building %s/%s\n", t.os, t.arch)
		_, err := command(root, buildEnv(t), "go", "build", "-mod=readonly", "-trimpath", "-o", destination, "./cmd/minihub")
		return err
	}
	info := buildInfo{commit, dirty, strings.TrimSpace(string(versionData))}
	if err := createRelease(root, *version, info, licenses, build); err != nil {
		return err
	}
	fmt.Fprintf(output, "Release files: %s\n", filepath.Join(root, "dist", *version))
	return nil
}

// Replace environment values instead of appending duplicates (also on Windows).
func buildEnv(t target) []string {
	overrides := map[string]string{
		"GOOS": t.os, "GOARCH": t.arch, "CGO_ENABLED": "0",
		"GOFLAGS": "", "GOAMD64": "v1", "GOARM64": "v8.0",
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func command(root string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func dependencyLicenses(root string) ([]licenseFile, error) {
	// Collect the union of dependencies actually used by the target builds,
	// rather than test-only modules that may not be downloaded.
	modules := map[string]module{}
	for _, t := range targets {
		data, err := command(root, buildEnv(t), "go", "list", "-mod=readonly", "-deps", "-json", "./cmd/minihub")
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var pkg struct{ Module *module }
			if err := decoder.Decode(&pkg); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
			if m := pkg.Module; m != nil && !m.Main {
				modules[m.Path+"@"+m.Version] = *m
			}
		}
	}
	var files []licenseFile
	for key, m := range modules {
		if m.Dir == "" {
			return nil, fmt.Errorf("module %s has no source directory", key)
		}
		found, err := findLicenses(m.Dir, filepath.Join("licenses", filepath.FromSlash(key)))
		if err != nil {
			return nil, err
		}
		files = append(files, found...)
	}
	goroot, err := command(root, nil, "go", "env", "GOROOT")
	if err != nil {
		return nil, err
	}
	for _, source := range []struct{ dir, destination string }{
		{strings.TrimSpace(string(goroot)), "licenses/go"},
		{filepath.Join(root, "web/vendor/bootstrap"), "licenses/bootstrap"},
		{filepath.Join(root, "web/vendor/bootstrap-icons"), "licenses/bootstrap-icons"},
	} {
		found, err := findLicenses(source.dir, source.destination)
		if err != nil {
			return nil, err
		}
		files = append(files, found...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].destination < files[j].destination })
	return files, nil
}

func findLicenses(dir, destination string) ([]licenseFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []licenseFile
	for _, entry := range entries {
		name := strings.ToUpper(entry.Name())
		if entry.Type().IsRegular() && (strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING") || strings.HasPrefix(name, "NOTICE") || name == "PATENTS") {
			files = append(files, licenseFile{filepath.Join(dir, entry.Name()), filepath.Join(destination, entry.Name())})
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no license documents found in %s", dir)
	}
	return files, nil
}

func requireAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("output already exists: %s", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func createRelease(root, version string, info buildInfo, licenses []licenseFile, build buildFunc) (result error) {
	if !versionPattern.MatchString(version) {
		return errors.New("invalid release version")
	}
	dist := filepath.Join(root, "dist")
	final := filepath.Join(dist, version)
	if err := requireAbsent(final); err != nil {
		return err
	}
	if err := os.MkdirAll(dist, 0755); err != nil {
		return err
	}
	// Reserve this version while building. This also prevents two builders from
	// racing to rename over an existing empty directory on Unix.
	lockPath := filepath.Join(dist, "."+version+".lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("reserve release output: %w", err)
	}
	defer func() { result = errors.Join(result, os.Remove(lockPath)) }()
	if err := lock.Close(); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dist, ".release-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(stage)) }()
	artifacts := filepath.Join(stage, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		return err
	}
	var checksums strings.Builder
	for _, t := range targets {
		name := fmt.Sprintf("minihub_%s_%s_%s", version, t.os, t.arch)
		bundle := filepath.Join(stage, name)
		if err := os.Mkdir(bundle, 0755); err != nil {
			return err
		}
		executable := "minihub"
		if t.os == "windows" {
			executable += ".exe"
		}
		if err := build(t, filepath.Join(bundle, executable)); err != nil {
			return fmt.Errorf("build %s/%s: %w", t.os, t.arch, err)
		}
		if err := os.Chmod(filepath.Join(bundle, executable), 0755); err != nil {
			return err
		}
		for _, file := range []licenseFile{
			{filepath.Join(root, "minihub.sample.json"), "minihub.sample.json"},
			{filepath.Join(root, "release/README.md"), "README.md"},
		} {
			if err := copyFile(file.source, filepath.Join(bundle, file.destination)); err != nil {
				return err
			}
		}
		if err := copyDocs(filepath.Join(root, "docs_html"), filepath.Join(bundle, "docs_html")); err != nil {
			return err
		}
		for _, file := range licenses {
			if err := copyFile(file.source, filepath.Join(bundle, file.destination)); err != nil {
				return err
			}
		}
		if t.os == "windows" {
			if err := copyFile(filepath.Join(root, "scripts/backup.bat"), filepath.Join(bundle, "scripts/backup.bat")); err != nil {
				return err
			}
		}
		metadata := fmt.Sprintf("version: %s\ncommit: %s\ndirty: %s\ngo: %s\ntarget: %s/%s\ncgo: disabled\n", version, info.commit, info.dirty, info.goVersion, t.os, t.arch)
		if err := os.WriteFile(filepath.Join(bundle, "BUILDINFO.txt"), []byte(metadata), 0644); err != nil {
			return err
		}
		archiveName := name + t.extension
		archivePath := filepath.Join(artifacts, archiveName)
		if err := writeArchive(bundle, archivePath, t.extension); err != nil {
			return fmt.Errorf("archive %s: %w", archiveName, err)
		}
		file, err := os.Open(archivePath)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, hashErr := io.Copy(hash, file)
		if err := errors.Join(hashErr, file.Close()); err != nil {
			return err
		}
		fmt.Fprintf(&checksums, "%x  %s\n", hash.Sum(nil), archiveName)
		if err := os.RemoveAll(bundle); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(artifacts, "SHA256SUMS.txt"), []byte(checksums.String()), 0644); err != nil {
		return err
	}
	if err := requireAbsent(final); err != nil {
		return err
	}
	return os.Rename(artifacts, final)
}

func copyFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", source)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0644)
}

func copyDocs(source, destination string) error {
	// The documentation bundle only contains static HTML and CSS. Do not copy
	// unrelated files placed alongside the manuals.
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(source, "index.html")); err != nil {
		return err
	}
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if ext == ".html" || ext == ".css" {
			if err := copyFile(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeArchive(bundle, destination, extension string) (result error) {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	var zipWriter *zip.Writer
	var tarWriter *tar.Writer
	if extension == ".zip" {
		zipWriter = zip.NewWriter(file)
		defer func() { result = errors.Join(result, zipWriter.Close()) }()
	} else if extension == ".tar.gz" {
		compressed := gzip.NewWriter(file)
		defer func() { result = errors.Join(result, compressed.Close()) }()
		tarWriter = tar.NewWriter(compressed)
		defer func() { result = errors.Join(result, tarWriter.Close()) }()
	} else {
		return fmt.Errorf("unsupported archive extension: %s", extension)
	}
	return filepath.WalkDir(bundle, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported archive entry: %s", path)
		}
		relative, err := filepath.Rel(filepath.Dir(bundle), path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if info.IsDir() {
			name += "/"
		}
		// Fixed archive timestamps avoid platform-dependent source timestamps.
		stamp := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		mode := fs.FileMode(0644)
		if info.IsDir() {
			mode = fs.ModeDir | 0755
		} else if entry.Name() == "minihub" || entry.Name() == "minihub.exe" {
			mode = 0755
		}
		var writer io.Writer
		if zipWriter != nil {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name, header.Modified = name, stamp
			header.SetMode(mode)
			if !info.IsDir() {
				header.Method = zip.Deflate
			}
			writer, err = zipWriter.CreateHeader(header)
			if err != nil {
				return err
			}
		} else {
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name, header.ModTime = name, stamp
			header.Mode = int64(mode.Perm())
			header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}
			writer = tarWriter
		}
		if info.IsDir() {
			return nil
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, source)
		return errors.Join(copyErr, source.Close())
	})
}
