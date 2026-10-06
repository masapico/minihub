package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Inspect opens an existing database read-only. It must never initialize or
// repair a DB supplied as a migration destination or a restored installation.
func inspect(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("database must be a regular file")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "//") {
		return nil, errors.New("SQLite requires a local disk")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err == nil && version != 1 && version != 2 && version != 3 {
		err = errors.New("existing database has an unsupported or missing chat schema")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func OpenExisting(path string) (*Store, error) {
	db, err := inspect(path)
	if err != nil {
		return nil, err
	}
	if err = db.Close(); err != nil {
		return nil, err
	}
	return Open(path)
}
func VerifyImportFile(ctx context.Context, path string) (err error) {
	db, err := inspect(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	s := &Store{read: db, write: db}
	return s.VerifyMigration(ctx)
}
