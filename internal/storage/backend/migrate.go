package backend

import (
	"context"
	"errors"
	"github.com/masapico/minihub/internal/storage/sqlitestore"
	"os"
	"path/filepath"
)

// Migrate is offline, one-way, and never overwrites source data or a destination.
// Dry runs use a disposable SQLite DB to exercise exactly the same validations.
func Migrate(ctx context.Context, root string, dry bool) (report sqlitestore.ImportReport, err error) {
	l, err := Lock(root)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, l.Close()) }()
	if _, e := os.Stat(filepath.Join(root, ".retention-pending.json")); e == nil {
		return report, errors.New("retention deletion is incomplete; finish it before migration")
	} else if !errors.Is(e, os.ErrNotExist) {
		return report, e
	}
	mark, err := readMarker(root)
	if err != nil {
		return report, err
	}
	if mark == "sqlite" {
		return report, errors.New("directory already uses SQLite")
	}
	target := filepath.Join(root, DBName)
	if _, e := os.Stat(target); e == nil {
		e = sqlitestore.VerifyImportFile(ctx, target)
		if e != nil {
			return report, e
		}
		if !dry {
			err = writeMarker(root, "sqlite")
		}
		return report, err
	} else if !errors.Is(e, os.ErrNotExist) {
		return report, e
	}
	has, err := fileData(root)
	if err != nil {
		return report, err
	}
	if !has {
		return report, errors.New("no file data to migrate")
	}
	stage, err := os.MkdirTemp(root, ".sqlite-migrate-")
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	staged := filepath.Join(stage, DBName)
	s, err := sqlitestore.Open(staged)
	if err != nil {
		return report, err
	}
	report, err = s.ImportFiles(ctx, root)
	if err == nil {
		err = s.VerifyMigration(ctx)
	}
	err = errors.Join(err, s.Close())
	if err != nil {
		return report, err
	}
	if dry {
		return report, nil
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	if err = publishFile(staged, target, false); err != nil {
		return report, err
	}
	// If interrupted here, rerunning migration verifies the completed DB and repairs the marker.
	return report, writeMarker(root, "sqlite")
}
