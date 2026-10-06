// Package backend owns process-level storage selection and directory locking.
package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/filestore"
	"github.com/masapico/minihub/internal/storage/sqlitestore"
)

const DBName = "minuhub.db"
const markerName = "storage.json"

type marker struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
}
type Handle struct {
	storage.Storage
	close func() error
	lock  *flock.Flock
}

func (h *Handle) Close() error {
	var err error
	if h.close != nil {
		err = h.close()
	}
	return errors.Join(err, h.lock.Close())
}
func Lock(root string) (*flock.Flock, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	l := flock.New(filepath.Join(root, ".minihub.lock"), flock.SetPermissions(0600))
	ok, err := l.TryLock()
	if err != nil {
		l.Close()
		return nil, err
	}
	if !ok {
		l.Close()
		return nil, errors.New("data directory is in use; stop minihub before migration or restart")
	}
	return l, nil
}
func readMarker(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, markerName))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var m marker
	if err = json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	if m.Version != 1 || (m.Type != "file" && m.Type != "sqlite") {
		return "", errors.New("unsupported storage marker")
	}
	return m.Type, nil
}
func writeMarker(root, kind string) error {
	b, err := json.Marshal(marker{1, kind})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".storage-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return publishFile(name, filepath.Join(root, markerName), true)
}
func fileData(root string) (bool, error) {
	for _, dir := range []string{"users", "groups", "channels", "state", "sessions", "schedules"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if len(entries) > 0 {
			return true, nil
		}
	}
	return false, nil
}
func Open(root, kind string) (_ *Handle, err error) {
	if kind != "file" && kind != "sqlite" {
		return nil, errors.New("invalid storage type")
	}
	l, err := Lock(root)
	if err != nil {
		return nil, err
	}
	h := &Handle{lock: l}
	defer func() {
		if err != nil {
			h.Close()
		}
	}()
	if _, e := os.Stat(filepath.Join(root, filestore.RetentionJournal)); e == nil {
		return nil, errors.New("retention deletion is incomplete; rerun the retention command before starting")
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if _, e := os.Stat(filepath.Join(root, ".retention-sqlite-pending.json")); e == nil {
		return nil, errors.New("SQLite retention cleanup is incomplete; rerun the retention command before starting")
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	mark, err := readMarker(root)
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(filepath.Join(root, DBName))
	dbExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if mark != "" && mark != kind {
		return nil, fmt.Errorf("storage.type=%s does not match data directory (%s); use the migration procedure", kind, mark)
	}
	if kind == "file" {
		if dbExists {
			return nil, errors.New("SQLite database exists; refusing to open retained file data")
		}
		h.Storage, err = filestore.New(root)
	} else {
		if mark == "sqlite" && !dbExists {
			return nil, errors.New("SQLite database is missing; refusing to initialize an empty replacement")
		}
		if mark == "" {
			has, e := fileData(root)
			if e != nil {
				return nil, e
			}
			if has {
				return nil, errors.New("file data exists; run migrate -to sqlite before changing storage.type")
			}
			if dbExists {
				return nil, errors.New("unmarked SQLite database; restore its storage.json or complete migration")
			}
		}
		var s *sqlitestore.Store
		if dbExists {
			s, err = sqlitestore.OpenExisting(filepath.Join(root, DBName))
		} else {
			s, err = sqlitestore.Open(filepath.Join(root, DBName))
		}
		if err == nil {
			h.Storage = s
			h.close = s.Close
		}
	}
	if err != nil {
		return nil, err
	}
	if mark == "" {
		if err = writeMarker(root, kind); err != nil {
			return nil, err
		}
	}
	return h, nil
}
