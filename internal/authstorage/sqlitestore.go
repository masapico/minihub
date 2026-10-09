package authstorage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/oidc"
	_ "modernc.org/sqlite"
)

type SQLiteStorage struct {
	db *sql.DB
}

const authSchema = `
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE IF NOT EXISTS groups(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE IF NOT EXISTS clients(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
PRAGMA user_version=1;
`

func NewSQLiteStorage(path string) (*SQLiteStorage, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	var mode string
	if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		db.Close()
		return nil, err
	}
	if mode != "wal" {
		db.Close()
		return nil, errors.New("SQLite WAL unavailable")
	}

	if _, err := db.Exec(authSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return &SQLiteStorage{db: db}, nil
}

func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// Users
func (s *SQLiteStorage) GetUser(ctx context.Context, id string) (*domain.User, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM users WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	var u domain.User
	if err := json.Unmarshal([]byte(data), &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *SQLiteStorage) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM users ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []domain.User
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var u domain.User
		if err := json.Unmarshal([]byte(data), &u); err == nil {
			users = append(users, u)
		}
	}
	return users, rows.Err()
}

func (s *SQLiteStorage) SaveUser(ctx context.Context, user *domain.User) error {
	bytes, err := json.Marshal(user)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO users(id, data) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data", user.ID, string(bytes))
	return err
}

func (s *SQLiteStorage) DeleteUser(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrNotExist
	}
	return nil
}

// Groups
func (s *SQLiteStorage) GetGroup(ctx context.Context, id string) (*domain.Group, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM groups WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	var g domain.Group
	if err := json.Unmarshal([]byte(data), &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *SQLiteStorage) ListGroups(ctx context.Context) ([]domain.Group, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM groups ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []domain.Group
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var g domain.Group
		if err := json.Unmarshal([]byte(data), &g); err == nil {
			groups = append(groups, g)
		}
	}
	return groups, rows.Err()
}

func (s *SQLiteStorage) SaveGroup(ctx context.Context, group *domain.Group) error {
	bytes, err := json.Marshal(group)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO groups(id, data) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data", group.ID, string(bytes))
	return err
}

func (s *SQLiteStorage) DeleteGroup(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM groups WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrNotExist
	}
	return nil
}

// Sessions
func (s *SQLiteStorage) GetSession(ctx context.Context, hash string) (*domain.Session, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM sessions WHERE id = ?", hash).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	var sess domain.Session
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *SQLiteStorage) SaveSession(ctx context.Context, session *domain.Session) error {
	bytes, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO sessions(id, data) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data", session.TokenHash, string(bytes))
	return err
}

func (s *SQLiteStorage) DeleteSession(ctx context.Context, hash string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", hash)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *SQLiteStorage) DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, data FROM sessions")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var expiredIDs []string
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			continue
		}
		var sess domain.Session
		if err := json.Unmarshal([]byte(data), &sess); err == nil {
			if now.After(sess.ExpiresAt) {
				expiredIDs = append(expiredIDs, id)
			}
		}
	}

	deleted := 0
	for _, id := range expiredIDs {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id); err == nil {
			deleted++
		}
	}
	return deleted, nil
}

// Clients (OIDC Applications)
func (s *SQLiteStorage) GetClient(ctx context.Context, id string) (*oidc.Client, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM clients WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	var c oidc.Client
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *SQLiteStorage) ListClients(ctx context.Context) ([]*oidc.Client, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM clients ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []*oidc.Client
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var c oidc.Client
		if err := json.Unmarshal([]byte(data), &c); err == nil {
			clients = append(clients, &c)
		}
	}
	return clients, rows.Err()
}

func (s *SQLiteStorage) SaveClient(ctx context.Context, client *oidc.Client) error {
	bytes, err := json.Marshal(client)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO clients(id, data) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data", client.ID, string(bytes))
	return err
}

func (s *SQLiteStorage) DeleteClient(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM clients WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrNotExist
	}
	return nil
}

