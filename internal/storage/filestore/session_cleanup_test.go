package filestore_test

import (
	"context"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDeleteExpiredSessions(t *testing.T) {
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for id, expires := range map[string]time.Time{"expired": now.Add(-time.Second), "boundary": now, "valid": now.Add(time.Hour), "missing": {}} {
		if err := store.SaveSession(context.Background(), &domain.Session{TokenHash: id, ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sessions", "secret-broken.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sessions", ".tmp-kept"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	count, err := store.DeleteExpiredSessions(context.Background(), now)
	if count != 2 || err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), root) {
		t.Fatalf("count=%d error=%v", count, err)
	}
	for _, id := range []string{"expired", "boundary"} {
		if _, err := os.Stat(filepath.Join(root, "sessions", id+".json")); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", id, err)
		}
	}
	for _, name := range []string{"valid.json", "missing.json", "secret-broken.json", ".tmp-kept"} {
		if _, err := os.Stat(filepath.Join(root, "sessions", name)); err != nil {
			t.Fatal(err)
		}
	}
	if count, _ := store.DeleteExpiredSessions(context.Background(), now); count != 0 {
		t.Fatalf("repeat removed %d", count)
	}
}

func TestSessionCleanupDeletionFailureContinues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory modes do not prevent deletion on Windows; covered by the Windows sharing test")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.SaveSession(context.Background(), &domain.Session{TokenHash: "expired", ExpiresAt: now}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "sessions")
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if count, err := store.DeleteExpiredSessions(context.Background(), now); count != 0 || err == nil {
		t.Fatalf("count=%d error=%v", count, err)
	}
}
