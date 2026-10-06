package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/shutdown"
)

func stopServer(args []string) error {
	if runtime.GOOS != "windows" {
		return errors.New("stop is available only on Windows")
	}
	flags := flag.NewFlagSet("stop", flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultFilename, "configuration file")
	wait := flags.Duration("wait", time.Minute, "maximum time to wait for storage shutdown")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *wait <= 0 {
		return errors.New("usage: minihub stop [-config file] [-wait 60s]")
	}
	path, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(path, true)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	info, err := os.Stat(cfg.Server.DataDir)
	if err != nil {
		return fmt.Errorf("inspect data directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("configured data path is not a directory")
	}
	deadline := time.Now().Add(*wait)
	requested := false
	for {
		if !requested {
			sent, err := shutdown.Request(cfg.Server.DataDir)
			if err != nil {
				return fmt.Errorf("request shutdown: %w", err)
			}
			requested = sent
		}
		free, err := storageLockFree(cfg.Server.DataDir)
		if err != nil {
			return fmt.Errorf("check storage lock: %w", err)
		}
		if free {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for minihub to release %s", cfg.Server.DataDir)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func storageLockFree(dataDir string) (bool, error) {
	lock := flock.New(filepath.Join(dataDir, ".minihub.lock"))
	ok, err := lock.TryLock()
	if err == nil && ok {
		err = lock.Unlock()
	}
	return ok, errors.Join(err, lock.Close())
}
