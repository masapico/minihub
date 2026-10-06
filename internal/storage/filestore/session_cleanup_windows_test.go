//go:build windows

package filestore_test

import (
	"context"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
	"golang.org/x/sys/windows"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionCleanupSharingFailureContinues(t *testing.T) {
	root := t.TempDir()
	s, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"locked", "removable"} {
		if err = s.SaveSession(context.Background(), &domain.Session{TokenHash: id, ExpiresAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	name, err := windows.UTF16PtrFromString(filepath.Join(root, "sessions", "locked.json"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	count, err := s.DeleteExpiredSessions(context.Background(), now)
	if count != 1 || err == nil || strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), root) {
		t.Fatalf("count=%d error=%v", count, err)
	}
}
