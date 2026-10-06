// Package sqlitestore implements the chat storage contract using an embedded DB.
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/masapico/minihub/internal/storage"
	_ "modernc.org/sqlite"
)

type Store struct{ read, write *sql.DB }

var _ storage.Storage = (*Store)(nil)
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func checkIDs(ids ...string) error {
	for _, id := range ids {
		if !validID.MatchString(id) {
			return errors.New("invalid storage identifier")
		}
	}
	return nil
}
func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return os.ErrNotExist
	}
	return err
}

// Open requires an exclusively owned data directory (enforced by the runtime).
func Open(path string) (_ *Store, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(filepath.ToSlash(path), "//") {
		return nil, errors.New("SQLite must use a local disk, not a UNC share")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	w, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	s := &Store{write: w}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	var version int
	if err = w.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version != 0 && version != 1 && version != 2 && version != 3 {
		return nil, fmt.Errorf("unsupported SQLite schema version %d", version)
	}
	var mode string
	if err = w.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return nil, err
	}
	if mode != "wal" {
		return nil, errors.New("SQLite WAL unavailable")
	}
	if version == 0 {
		tx, e := w.Begin()
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, err = tx.Exec(schema); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	if version == 1 {
		tx, e := w.Begin()
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`CREATE TABLE polls(id TEXT PRIMARY KEY,data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE poll_response_events(poll_id TEXT NOT NULL,seq INTEGER NOT NULL,user_id TEXT NOT NULL,data TEXT NOT NULL CHECK(json_valid(data)),PRIMARY KEY(poll_id,seq));
CREATE INDEX poll_response_latest ON poll_response_events(poll_id,user_id,seq DESC);
ALTER TABLE messages ADD COLUMN poll_id TEXT;
CREATE UNIQUE INDEX message_poll ON messages(channel_id,poll_id);
PRAGMA user_version=2;`); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	if version == 1 || version == 2 {
		tx, e := w.Begin()
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS withdrawals(kind TEXT NOT NULL,channel_id TEXT NOT NULL,target_id TEXT NOT NULL,data TEXT NOT NULL CHECK(json_valid(data)),PRIMARY KEY(kind,channel_id,target_id)); PRAGMA user_version=3;`); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	s.read, err = sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	s.read.SetMaxOpenConns(4)
	s.read.SetMaxIdleConns(4)
	if err = s.read.Ping(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	var a, b error
	if s.read != nil {
		a = s.read.Close()
	}
	if s.write != nil {
		b = s.write.Close()
	}
	return errors.Join(a, b)
}
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Metadata is small, versioned JSON per entity; histories and state are relational.
// Views expose membership without duplicating the authoritative metadata.
const schema = `
CREATE TABLE users(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE groups(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE channels(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE sessions(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE schedules(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE polls(id TEXT PRIMARY KEY,data TEXT NOT NULL CHECK(json_valid(data)));
CREATE VIEW user_groups AS SELECT u.id user_id,j.value group_id FROM users u,json_each(u.data,'$.groups') j;
CREATE VIEW channel_members AS SELECT c.id channel_id,j.value user_id FROM channels c,json_each(c.data,'$.members') j;
CREATE VIEW channel_groups AS SELECT c.id channel_id,j.value group_id FROM channels c,json_each(c.data,'$.groups') j;
CREATE VIEW channel_managers AS SELECT c.id channel_id,j.value user_id FROM channels c,json_each(c.data,'$.managers') j;
CREATE TABLE counters(kind TEXT NOT NULL,id TEXT NOT NULL,last_seq INTEGER NOT NULL CHECK(last_seq>=0),PRIMARY KEY(kind,id));
CREATE TABLE messages(channel_id TEXT NOT NULL,seq INTEGER NOT NULL CHECK(seq>0),id TEXT NOT NULL UNIQUE,
 ts TEXT NOT NULL, ts_ns INTEGER NOT NULL,user_id TEXT NOT NULL,text TEXT NOT NULL,root INTEGER NOT NULL DEFAULT 0 CHECK(root>=0 AND root<seq),
 mention_ids TEXT, schedule_id TEXT,schedule_event TEXT,poll_id TEXT,
 PRIMARY KEY(channel_id,seq),UNIQUE(channel_id,schedule_id,schedule_event));
CREATE INDEX messages_thread ON messages(channel_id,root,seq,user_id,ts);
CREATE INDEX messages_time ON messages(ts_ns);
CREATE INDEX messages_author ON messages(channel_id,user_id,root,seq);
CREATE UNIQUE INDEX message_poll ON messages(channel_id,poll_id);
CREATE TABLE mention_refs(message_id TEXT NOT NULL REFERENCES messages(id),kind TEXT NOT NULL,target TEXT NOT NULL,PRIMARY KEY(message_id,kind,target));
CREATE INDEX mention_target ON mention_refs(kind,target,message_id);
CREATE TABLE read_state(user_id TEXT NOT NULL,channel_id TEXT NOT NULL,root INTEGER NOT NULL DEFAULT 0,last_seq INTEGER NOT NULL CHECK(last_seq>=0),PRIMARY KEY(user_id,channel_id,root));
CREATE TABLE mention_reads(user_id TEXT NOT NULL,message_id TEXT NOT NULL,read_at TEXT NOT NULL,PRIMARY KEY(user_id,message_id));
CREATE TABLE reaction_events(event_id INTEGER PRIMARY KEY,channel_id TEXT NOT NULL,seq INTEGER NOT NULL,user_id TEXT NOT NULL,key TEXT NOT NULL,active INTEGER NOT NULL,ts TEXT NOT NULL,version INTEGER NOT NULL);
CREATE TABLE reactions(channel_id TEXT NOT NULL,seq INTEGER NOT NULL,user_id TEXT NOT NULL,key TEXT NOT NULL,active INTEGER NOT NULL,PRIMARY KEY(channel_id,seq,key,user_id));
CREATE TABLE response_events(schedule_id TEXT NOT NULL,seq INTEGER NOT NULL,user_id TEXT NOT NULL,data TEXT NOT NULL CHECK(json_valid(data)),PRIMARY KEY(schedule_id,seq));
CREATE INDEX response_latest ON response_events(schedule_id,user_id,seq DESC);
CREATE TABLE poll_response_events(poll_id TEXT NOT NULL,seq INTEGER NOT NULL,user_id TEXT NOT NULL,data TEXT NOT NULL CHECK(json_valid(data)),PRIMARY KEY(poll_id,seq));
CREATE INDEX poll_response_latest ON poll_response_events(poll_id,user_id,seq DESC);
CREATE TABLE migration_complete(id INTEGER PRIMARY KEY CHECK(id=1),manifest TEXT NOT NULL);
CREATE TABLE withdrawals(kind TEXT NOT NULL,channel_id TEXT NOT NULL,target_id TEXT NOT NULL,data TEXT NOT NULL CHECK(json_valid(data)),PRIMARY KEY(kind,channel_id,target_id));
PRAGMA user_version=3;
`

func get[T any](ctx context.Context, s *Store, table, id string) (*T, error) {
	if err := checkIDs(id); err != nil {
		return nil, err
	}
	var data []byte
	if err := s.read.QueryRowContext(ctx, "SELECT data FROM "+table+" WHERE id=?", id).Scan(&data); err != nil {
		return nil, missing(err)
	}
	var v T
	err := json.Unmarshal(data, &v)
	return &v, err
}
func list[T any](ctx context.Context, s *Store, table string) ([]T, error) {
	rows, err := s.read.QueryContext(ctx, "SELECT data FROM "+table+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		var v T
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) save(ctx context.Context, table, id string, v any) error {
	if err := checkIDs(id); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.write.ExecContext(ctx, "INSERT INTO "+table+"(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, string(b))
	return err
}
func (s *Store) remove(ctx context.Context, table, id string) error {
	if err := checkIDs(id); err != nil {
		return err
	}
	_, err := s.write.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id)
	return err
}
func nextSeq(ctx context.Context, tx *sql.Tx, kind, id string) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `INSERT INTO counters(kind,id,last_seq) VALUES(?,?,1) ON CONFLICT(kind,id) DO UPDATE SET last_seq=last_seq+1 RETURNING last_seq`, kind, id).Scan(&seq)
	return seq, err
}
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error) {
	n := 0
	failed := 0
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id,data FROM sessions")
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			var b []byte
			if err = rows.Scan(&id, &b); err != nil {
				rows.Close()
				return err
			}
			var v struct {
				ExpiresAt time.Time `json:"expiresAt"`
			}
			if err = json.Unmarshal(b, &v); err != nil {
				failed++
				continue
			}
			if v.ExpiresAt.IsZero() {
				failed++
				continue
			}
			if !v.ExpiresAt.IsZero() && !v.ExpiresAt.After(now) {
				ids = append(ids, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE id=?", id); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if failed > 0 {
		return n, fmt.Errorf("session cleanup failed for %d records", failed)
	}
	return n, nil
}
