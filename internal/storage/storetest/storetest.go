// Package storetest selects the storage implementation for shared integration tests.
package storetest

import (
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/filestore"
	"github.com/masapico/minihub/internal/storage/sqlitestore"
	"os"
	"path/filepath"
	"testing"
)

func New(t testing.TB, root string) (storage.Storage, error) {
	t.Helper()
	if os.Getenv("MINIHUB_TEST_STORAGE") == "sqlite" {
		s, err := sqlitestore.Open(filepath.Join(root, "minuhub.db"))
		if err == nil {
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		return s, err
	}
	return filestore.New(root)
}
