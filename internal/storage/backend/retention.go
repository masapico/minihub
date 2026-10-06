package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/filestore"
	"github.com/masapico/minihub/internal/storage/sqlitestore"
)

const sqliteRetentionJournal = ".retention-sqlite-pending.json"

// Prune runs only while holding the same data-directory lock as the server.
func Prune(ctx context.Context, root, kind string, cutoff time.Time, apply bool) (report storage.RetentionReport, err error) {
	lock, err := Lock(root)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	marked, err := readMarker(root)
	if err != nil {
		return report, err
	}
	if kind != "file" && kind != "sqlite" || marked != "" && marked != kind {
		return report, errors.New("storage type does not match data directory")
	}
	if kind == "file" {
		if marked == "" {
			has, e := fileData(root)
			if e != nil {
				return report, e
			}
			if !has {
				return report, errors.New("no file data to prune")
			}
		}
		if _, e := os.Stat(filepath.Join(root, DBName)); e == nil {
			return report, errors.New("SQLite database exists in file-mode directory")
		} else if !errors.Is(e, os.ErrNotExist) {
			return report, e
		}
		s, e := filestore.New(root)
		if e != nil {
			return report, e
		}
		report, err = s.PruneRetention(ctx, cutoff, apply)
		if err == nil && apply && report.Messages > 0 {
			err = recordRetention(root, kind, report)
		}
		return report, err
	}
	if marked != "sqlite" {
		return report, errors.New("SQLite storage marker is required")
	}
	path := filepath.Join(root, sqliteRetentionJournal)
	pendingRun := false
	var planned storage.RetentionReport
	if b, e := os.ReadFile(path); e == nil {
		pendingRun = true
		var pending struct {
			Cutoff time.Time               `json:"cutoff"`
			Report storage.RetentionReport `json:"report"`
		}
		if e = json.Unmarshal(b, &pending); e != nil {
			return report, e
		}
		if !pending.Cutoff.Equal(cutoff) {
			return report, fmt.Errorf("unfinished SQLite retention run uses cutoff %s; rerun with -before %s", pending.Cutoff.Format("2006-01-02"), pending.Cutoff.Format("2006-01-02"))
		}
		planned = pending.Report
	} else if !errors.Is(e, os.ErrNotExist) {
		return report, e
	}
	if pendingRun && !apply {
		return planned, nil
	}
	s, err := sqlitestore.OpenExisting(filepath.Join(root, DBName))
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	if apply && !pendingRun {
		preview, e := s.PruneRetention(ctx, cutoff, false)
		if e != nil {
			return report, e
		}
		if preview.Messages == 0 {
			return preview, nil
		}
		planned = preview
	}
	if apply {
		if _, e := os.Stat(path); errors.Is(e, os.ErrNotExist) {
			b, _ := json.Marshal(struct {
				Cutoff time.Time               `json:"cutoff"`
				Report storage.RetentionReport `json:"report"`
			}{cutoff, planned})
			tmp, e := os.CreateTemp(root, ".retention-sqlite-*.tmp")
			if e != nil {
				return report, e
			}
			defer os.Remove(tmp.Name())
			if e = tmp.Chmod(0o600); e == nil {
				_, e = tmp.Write(b)
			}
			if e == nil {
				e = tmp.Sync()
			}
			e = errors.Join(e, tmp.Close())
			if e != nil {
				return report, e
			}
			if e = publishFile(tmp.Name(), path, false); e != nil {
				return report, e
			}
		} else if e != nil {
			return report, e
		}
	}
	report, err = s.PruneRetention(ctx, cutoff, apply)
	if err != nil {
		return report, fmt.Errorf("SQLite retention: %w", err)
	}
	if apply {
		if planned.Cutoff.Equal(cutoff) {
			report = planned
		}
		err = os.Remove(path)
	}
	if err == nil && apply && report.Messages > 0 {
		err = recordRetention(root, kind, report)
	}
	return report, err
}

func recordRetention(root, kind string, report storage.RetentionReport) error {
	b, err := json.Marshal(struct {
		At      time.Time `json:"at"`
		Storage string    `json:"storage"`
		storage.RetentionReport
	}{time.Now().UTC(), kind, report})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "retention-runs.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
