//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/shutdown"
	"github.com/masapico/minihub/internal/storage/backend"
)

func TestStopWaitsForDataLockRelease(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "minihub.json")
	if err := os.WriteFile(configPath, []byte(`{"version":1,"storage":{"type":"file"},"server":{"dataDir":"data"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := backend.Lock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	requested, closeEvent, err := shutdown.Listen(dataDir)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	defer closeEvent()
	done := make(chan error, 1)
	go func() { done <- stopServer([]string{"-config", configPath, "-wait", "5s"}) }()
	select {
	case <-requested:
	case <-time.After(2 * time.Second):
		lock.Close()
		t.Fatal("stop command did not signal the server")
	}
	select {
	case err := <-done:
		lock.Close()
		t.Fatalf("stop returned before lock release: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := stopServer([]string{"-config", configPath, "-wait", "1s"}); err != nil {
		t.Fatalf("already stopped: %v", err)
	}
}

func TestStopFailsIfAnotherProcessHoldsDataLock(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "minihub.json")
	if err := os.WriteFile(configPath, []byte(`{"version":1,"storage":{"type":"file"},"server":{"dataDir":"data"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := backend.Lock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = stopServer([]string{"-config", configPath, "-wait", "200ms"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout with busy data directory, got %v", err)
	}
}

func TestStopUsesDefaultConfig(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := stopServer([]string{"-wait", "1s"}); err == nil {
		t.Fatal("missing default config should fail")
	}
	if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("minihub.json", []byte(`{"version":1,"server":{"dataDir":"data"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stopServer([]string{"-wait", "1s"}); err != nil {
		t.Fatal(err)
	}
}
