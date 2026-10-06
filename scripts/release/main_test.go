package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, []licenseFile) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"release/README.md":           "distribution guide",
		"minihub.sample.json":         `{"version":1}`,
		"scripts/backup.bat":          "@echo off\r\n",
		"docs_html/index.html":        `<a href="admin-manual.html">Manual</a>`,
		"docs_html/admin-manual.html": `<link rel="stylesheet" href="docs.css">`,
		"docs_html/docs.css":          "body {}",
		"dependency/LICENSE":          "dependency notice",
		"minihub.json":                "PRIVATE CONFIG",
		"data/server.log":             "PRIVATE LOG",
		"data/minuhub.db":             "PRIVATE DATABASE",
		"docs_html/private.key":       "PRIVATE KEY",
		"docs_html/cert.pem":          "PRIVATE CERTIFICATE",
		"docs_html/data/secret.json":  "PRIVATE DATA",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root, []licenseFile{{filepath.Join(root, "dependency/LICENSE"), "licenses/example.org/dependency@v1.0.0/LICENSE"}}
}

type archiveEntry struct {
	body []byte
	mode fs.FileMode
}

func readArchive(t *testing.T, path string) map[string]archiveEntry {
	t.Helper()
	entries := map[string]archiveEntry{}
	add := func(name string, mode fs.FileMode, reader io.Reader) {
		t.Helper()
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "../") {
			t.Fatalf("unsafe archive path: %q", name)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := entries[name]; exists {
			t.Fatalf("duplicate archive path: %q", name)
		}
		entries[name] = archiveEntry{body, mode}
	}
	if strings.HasSuffix(path, ".zip") {
		archive, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		defer archive.Close()
		for _, file := range archive.File {
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			add(file.Name, file.Mode(), reader)
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		compressed, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		defer compressed.Close()
		archive := tar.NewReader(compressed)
		for {
			header, err := archive.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			add(header.Name, header.FileInfo().Mode(), archive)
		}
	}
	return entries
}

func TestReleaseContentsAndChecksums(t *testing.T) {
	root, licenses := fixture(t)
	version := "v1.2.3-rc.1"
	info := buildInfo{"0123456789abcdef", "true", "go version go1.27.1"}
	build := func(target target, destination string) error {
		return os.WriteFile(destination, []byte(target.os+"/"+target.arch), 0600)
	}
	if err := createRelease(root, version, info, licenses, build); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "dist", version)
	files, err := os.ReadDir(destination)
	if err != nil || len(files) != 5 {
		t.Fatalf("release files=%v error=%v", files, err)
	}
	var checksums strings.Builder
	for _, target := range targets {
		name := fmt.Sprintf("minihub_%s_%s_%s", version, target.os, target.arch)
		archiveName := name + target.extension
		path := filepath.Join(destination, archiveName)
		archive := readArchive(t, path)
		executable := "minihub"
		if target.os == "windows" {
			executable += ".exe"
		}
		want := map[string]string{
			executable:                    target.os + "/" + target.arch,
			"README.md":                   "distribution guide",
			"minihub.sample.json":         `{"version":1}`,
			"docs_html/index.html":        `<a href="admin-manual.html">Manual</a>`,
			"docs_html/admin-manual.html": `<link rel="stylesheet" href="docs.css">`,
			"docs_html/docs.css":          "body {}",
			"licenses/example.org/dependency@v1.0.0/LICENSE": "dependency notice",
			"BUILDINFO.txt": fmt.Sprintf("version: %s\ncommit: %s\ndirty: true\ngo: %s\ntarget: %s/%s\ncgo: disabled\n", version, info.commit, info.goVersion, target.os, target.arch),
		}
		if target.os == "windows" {
			want["scripts/backup.bat"] = "@echo off\r\n"
		}
		for relative, body := range want {
			entry, exists := archive[name+"/"+relative]
			if !exists || string(entry.body) != body {
				t.Errorf("%s: missing or incorrect %s", archiveName, relative)
			}
			wantMode := fs.FileMode(0644)
			if relative == executable {
				wantMode = 0755
			}
			if entry.mode.Perm() != wantMode {
				t.Errorf("%s: %s mode=%o, want %o", archiveName, relative, entry.mode.Perm(), wantMode)
			}
		}
		for path, entry := range archive {
			if !strings.HasPrefix(path, name+"/") {
				t.Errorf("entry outside the distribution folder: %s", path)
			}
			if entry.mode.IsDir() {
				continue
			}
			if _, allowed := want[strings.TrimPrefix(path, name+"/")]; !allowed {
				t.Errorf("unexpected file (possibly private data): %s", path)
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(data), archiveName)
	}
	actual, err := os.ReadFile(filepath.Join(destination, "SHA256SUMS.txt"))
	if err != nil || string(actual) != checksums.String() {
		t.Fatalf("checksum mismatch: %s, error=%v", actual, err)
	}
	remaining, err := os.ReadDir(filepath.Join(root, "dist"))
	if err != nil || len(remaining) != 1 || remaining[0].Name() != version {
		t.Fatalf("temporary files remain: %v, error=%v", remaining, err)
	}
}

func TestReleaseFailureLeavesNoOutput(t *testing.T) {
	for _, scenario := range []string{"build", "missing-document", "missing-license"} {
		t.Run(scenario, func(t *testing.T) {
			root, licenses := fixture(t)
			calls := 0
			build := func(target target, destination string) error {
				calls++
				if scenario == "build" && calls == 2 {
					return errors.New("compiler failed")
				}
				return os.WriteFile(destination, []byte("binary"), 0755)
			}
			if scenario == "missing-document" {
				if err := os.Remove(filepath.Join(root, "docs_html/index.html")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing-license" {
				licenses[0].source = filepath.Join(root, "missing-license")
			}
			if err := createRelease(root, "v1.0.0", buildInfo{}, licenses, build); err == nil {
				t.Fatal("failed release was accepted")
			}
			entries, err := os.ReadDir(filepath.Join(root, "dist"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("incomplete output remains: %v, error=%v", entries, err)
			}
		})
	}
}

func TestExistingOutputAndReservationArePreserved(t *testing.T) {
	for _, name := range []string{"v1.0.0", ".v1.0.0.lock"} {
		t.Run(name, func(t *testing.T) {
			root, licenses := fixture(t)
			path := filepath.Join(root, "dist", name)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("existing output"), 0644); err != nil {
				t.Fatal(err)
			}
			build := func(target, string) error {
				t.Fatal("builder ran despite existing output")
				return nil
			}
			if err := createRelease(root, "v1.0.0", buildInfo{}, licenses, build); err == nil {
				t.Fatal("existing output was accepted")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "existing output" {
				t.Fatalf("existing output changed: %q, error=%v", data, err)
			}
		})
	}
}

func TestInvalidVersionAndHelp(t *testing.T) {
	for _, version := range []string{"", "1.0.0", "v01.0.0", "../private", "v1.0.0/../../data", "v1.0.0-rc.0", "v1.0.0\n"} {
		if err := run([]string{"-version", version}, io.Discard); err == nil {
			t.Errorf("accepted invalid version %q", version)
		}
	}
	if err := run([]string{"-version", "v1.0.0", "extra"}, io.Discard); err == nil {
		t.Error("accepted positional arguments")
	}
	var help bytes.Buffer
	if err := run([]string{"-h"}, &help); err != nil || !strings.Contains(help.String(), "-version") {
		t.Fatalf("help=%s error=%v", help.String(), err)
	}
}

func TestLicenseDocuments(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"LICENSE", "LICENSE-SQLITE", "LICENSE-3RD-PARTY.md", "NOTICE", "COPYING", "PATENTS", "source.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := findLicenses(root, "licenses/example@v1.0.0")
	if err != nil || len(files) != 6 {
		t.Fatalf("license documents=%v, error=%v", files, err)
	}
	if _, err := findLicenses(t.TempDir(), "licenses/empty"); err == nil {
		t.Fatal("missing license documents accepted")
	}
}

func TestArchiveExecutableModeDoesNotDependOnHost(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "minihub"), []byte("binary"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, extension := range []string{".zip", ".tar.gz"} {
		path := filepath.Join(root, "bundle"+extension)
		if err := writeArchive(bundle, path, extension); err != nil {
			t.Fatal(err)
		}
		if mode := readArchive(t, path)["bundle/minihub"].mode.Perm(); mode != 0755 {
			t.Errorf("%s executable mode=%o", extension, mode)
		}
	}
}

func TestBuildEnvironment(t *testing.T) {
	t.Setenv("GOOS", "freebsd")
	t.Setenv("GOARCH", "386")
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("GOFLAGS", "-race")
	t.Setenv("GOAMD64", "v4")
	t.Setenv("GOCACHE", t.TempDir())
	values := map[string]string{}
	for _, entry := range buildEnv(target{"linux", "arm64", ".tar.gz"}) {
		key, value, _ := strings.Cut(entry, "=")
		// Windows also carries drive working directories as =C:=path entries.
		if key == "" && strings.HasPrefix(entry, "=") {
			key, value, _ = strings.Cut(entry[1:], "=")
			key = "=" + key
		}
		if _, exists := values[strings.ToUpper(key)]; exists {
			t.Fatalf("duplicate environment key: %s", key)
		}
		values[strings.ToUpper(key)] = value
	}
	for key, want := range map[string]string{"GOOS": "linux", "GOARCH": "arm64", "CGO_ENABLED": "0", "GOFLAGS": "", "GOAMD64": "v1", "GOARM64": "v8.0", "GOCACHE": os.Getenv("GOCACHE")} {
		if values[key] != want {
			t.Errorf("%s=%q, want %q", key, values[key], want)
		}
	}
}
