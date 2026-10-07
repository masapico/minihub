package sqlitestore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/masapico/minihub/internal/domain"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Manifest is a bounded-memory multiset digest of canonical logical records.
// Each record includes its entity kind and key; duplicates also change the count.
type Digest struct {
	Count int64    `json:"count"`
	Sum   [32]byte `json:"sum"`
}
type Manifest map[string]Digest

func (d Manifest) add(kind, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h := sha256.New()
	h.Write([]byte(kind + "\x00" + key + "\x00"))
	h.Write(b)
	sum := h.Sum(nil)
	x := d[kind]
	carry := 0
	for i := 31; i >= 0; i-- {
		n := int(x.Sum[i]) + int(sum[i]) + carry
		x.Sum[i] = byte(n)
		carry = n >> 8
	}
	x.Count++
	d[kind] = x
	return nil
}

type ImportReport struct {
	Records         Manifest `json:"records"`
	IncompleteTails []string `json:"incompleteTails,omitempty"`
}
type stateValue struct {
	LastReadSeq int64 `json:"lastReadSeq"`
}
type fileState struct {
	Version  int                             `json:"version"`
	Channels map[string]stateValue           `json:"channels"`
	Threads  map[string]map[int64]stateValue `json:"threads,omitempty"`
	Mentions map[string]time.Time            `json:"mentions,omitempty"`
}
type readRecord struct {
	User, Channel string
	Root, Seq     int64
}
type mentionReadRecord struct {
	User, Message string
	At            time.Time
}

func decodeStrict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid or unsupported JSON record")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}
func supportedVersion(v int) error {
	if v != 0 && v != domain.Version {
		return errors.New("unsupported record version")
	}
	return nil
}

// ImportFiles never calls file-store recovery methods and never edits the source.
// The caller owns the source directory lock and an unpublished, empty target DB.
func (s *Store) ImportFiles(ctx context.Context, root string) (ImportReport, error) {
	report := ImportReport{Records: Manifest{}}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		reactionID := int64(0)
		for _, dir := range []string{"users", "groups", "channels", "sessions", "schedules", "polls", "state", "withdrawals"} {
			base := filepath.Join(root, dir)
			if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) (readErr error) {
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return errors.New("symbolic links are not supported in migration")
				}
				if entry.IsDir() {
					return nil
				}
				if !entry.Type().IsRegular() {
					return errors.New("non-regular data file")
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				parts := strings.Split(filepath.ToSlash(rel), "/")
				fail := func(err error) error {
					if dir == "sessions" {
						return errors.New("invalid session data (contents and filename omitted)")
					}
					return fmt.Errorf("%s: %w", filepath.ToSlash(rel), err)
				}
				if dir == "withdrawals" && len(parts) == 4 && strings.HasSuffix(parts[3], ".json") {
					channelID, kind, targetID := parts[1], parts[2], strings.TrimSuffix(parts[3], ".json")
					if e := validWithdrawal(kind, channelID, targetID); e != nil {
						return fail(e)
					}
					b, e := os.ReadFile(path)
					if e != nil {
						return fail(e)
					}
					var value domain.Withdrawal
					if e = decodeStrict(b, &value); e != nil {
						return fail(e)
					}
					if value.Kind != kind || value.ChannelID != channelID || value.TargetID != targetID || !value.Valid() || checkIDs(value.ActorID) != nil {
						return fail(errors.New("invalid withdrawal record"))
					}
					if e = supportedVersion(value.Version); e != nil {
						return fail(e)
					}
					if _, e = tx.ExecContext(ctx, "INSERT INTO withdrawals(kind,channel_id,target_id,data) VALUES(?,?,?,?)", kind, channelID, targetID, b); e != nil {
						return fail(e)
					}
					return failIf(report.Records.add("withdrawals", kind+"/"+channelID+"/"+targetID, value), fail)
				}
				if len(parts) == 2 && dir != "channels" && dir != "schedules" && dir != "polls" && strings.HasSuffix(parts[1], ".json") {
					id := strings.TrimSuffix(parts[1], ".json")
					if err = checkIDs(id); err != nil {
						return fail(err)
					}
					b, e := os.ReadFile(path)
					if e != nil {
						return fail(e)
					}
					if dir == "state" {
						var st fileState
						if e = decodeStrict(b, &st); e != nil {
							return fail(e)
						}
						if e = supportedVersion(st.Version); e != nil {
							return fail(e)
						}
						for ch, v := range st.Channels {
							r := readRecord{id, ch, 0, v.LastReadSeq}
							if e = importRead(ctx, tx, r, report.Records); e != nil {
								return fail(e)
							}
						}
						for ch, threads := range st.Threads {
							for root, v := range threads {
								if root < 1 {
									return fail(errors.New("invalid thread root"))
								}
								if e = importRead(ctx, tx, readRecord{id, ch, root, v.LastReadSeq}, report.Records); e != nil {
									return fail(e)
								}
							}
						}
						for message, at := range st.Mentions {
							if e = checkIDs(message); e != nil {
								return fail(e)
							}
							r := mentionReadRecord{id, message, at}
							if _, e = tx.ExecContext(ctx, "INSERT INTO mention_reads(user_id,message_id,read_at) VALUES(?,?,?)", id, message, at.Format(time.RFC3339Nano)); e != nil {
								return fail(e)
							}
							if e = report.Records.add("mention_reads", id+"/"+message, r); e != nil {
								return fail(e)
							}
						}
						return nil
					}
					return failIf(importMeta(ctx, tx, dir, id, b, report.Records), fail)
				}
				if len(parts) == 3 && (dir == "channels" || dir == "schedules" || dir == "polls") && parts[2] == "meta.json" {
					if err = checkIDs(parts[1]); err != nil {
						return fail(err)
					}
					b, e := os.ReadFile(path)
					if e != nil {
						return fail(e)
					}
					return failIf(importMeta(ctx, tx, dir, parts[1], b, report.Records), fail)
				}
				if len(parts) == 3 && dir == "channels" && parts[2] == "sequence.json" {
					if err = checkIDs(parts[1]); err != nil {
						return fail(err)
					}
					var watermark struct {
						LastSeq int64 `json:"lastSeq"`
					}
					b, e := os.ReadFile(path)
					if e != nil {
						return fail(e)
					}
					if e = decodeStrict(b, &watermark); e != nil || watermark.LastSeq < 0 {
						return fail(errors.New("invalid channel sequence watermark"))
					}
					_, e = tx.ExecContext(ctx, "INSERT INTO counters(kind,id,last_seq) VALUES('channel',?,?)", parts[1], watermark.LastSeq)
					return failIf(e, fail)
				}
				kind := ""
				id := ""
				if len(parts) == 4 && dir == "channels" && strings.HasSuffix(parts[3], ".jsonl") && (parts[2] == "messages" || parts[2] == "reactions") {
					if _, e := time.Parse("2006-01-02", strings.TrimSuffix(parts[3], ".jsonl")); e != nil {
						return fail(errors.New("unsupported log filename"))
					}
					kind = parts[2]
					id = parts[1]
				}
				if len(parts) == 3 && dir == "schedules" && parts[2] == "responses.jsonl" {
					kind = "responses"
					id = parts[1]
				}
				if len(parts) == 3 && dir == "polls" && parts[2] == "responses.jsonl" {
					kind = "poll_responses"
					id = parts[1]
				}
				if kind == "" {
					return fail(errors.New("unrecognized data file"))
				}
				if err = checkIDs(id); err != nil {
					return fail(err)
				}
				f, e := os.Open(path)
				if e != nil {
					return fail(e)
				}
				defer func() { readErr = errors.Join(readErr, f.Close()) }()
				reader := bufio.NewReaderSize(f, 64*1024)
				offset := int64(0)
				lastResponseSeq := int64(0)
				for {
					if err = ctx.Err(); err != nil {
						return err
					}
					line, e := reader.ReadBytes('\n')
					if errors.Is(e, io.EOF) {
						if len(line) > 0 {
							report.IncompleteTails = append(report.IncompleteTails, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), offset))
						}
						break
					}
					if e != nil {
						return fail(e)
					}
					switch kind {
					case "messages":
						var m domain.Message
						if e = decodeStrict(line, &m); e == nil {
							e = insertMessage(ctx, tx, id, m)
						}
						if e == nil {
							e = report.Records.add("messages", id+"/"+strconv.FormatInt(m.Seq, 10), m)
						}
					case "reactions":
						var r domain.ReactionEvent
						if e = decodeStrict(line, &r); e == nil {
							e = supportedVersion(r.Version)
						}
						if e == nil {
							e = insertReaction(ctx, tx, id, r)
						}
						if e == nil {
							reactionID++
							e = report.Records.add("reaction_events", id+"/"+strconv.FormatInt(reactionID, 10), r)
						}
					case "responses":
						var r domain.ScheduleResponse
						if e = decodeStrict(line, &r); e == nil {
							e = supportedVersion(r.Version)
						}
						if e == nil && r.Seq <= lastResponseSeq {
							e = errors.New("non-monotonic response sequence")
						}
						lastResponseSeq = r.Seq
						if e == nil {
							e = insertResponse(ctx, tx, id, r)
						}
						if e == nil {
							e = report.Records.add("response_events", id+"/"+strconv.FormatInt(r.Seq, 10), r)
						}
					case "poll_responses":
						var r domain.PollResponse
						if e = decodeStrict(line, &r); e == nil {
							e = supportedVersion(r.Version)
						}
						if e == nil && r.Seq <= lastResponseSeq {
							e = errors.New("non-monotonic poll response sequence")
						}
						lastResponseSeq = r.Seq
						if e == nil {
							var b []byte
							b, e = json.Marshal(r)
							if e == nil {
								_, e = tx.ExecContext(ctx, "INSERT INTO poll_response_events(poll_id,seq,user_id,data) VALUES(?,?,?,?)", id, r.Seq, r.UserID, string(b))
							}
						}
						if e == nil {
							e = report.Records.add("poll_response_events", id+"/"+strconv.FormatInt(r.Seq, 10), r)
						}
					}
					if e != nil {
						return fail(fmt.Errorf("record at byte %d rejected: %w", offset, e))
					}
					offset += int64(len(line))
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO counters SELECT 'channel',channel_id,MAX(seq) FROM messages GROUP BY channel_id ON CONFLICT(kind,id) DO UPDATE SET last_seq=MAX(last_seq,excluded.last_seq);
   INSERT INTO counters SELECT 'schedule',schedule_id,MAX(seq) FROM response_events GROUP BY schedule_id;
   INSERT INTO counters SELECT 'poll',poll_id,MAX(seq) FROM poll_response_events GROUP BY poll_id;`); err != nil {
			return err
		}
		if err := validateReferences(ctx, tx); err != nil {
			return err
		}
		actual, err := databaseManifest(ctx, tx)
		if err != nil {
			return err
		}
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(report.Records)
		if !bytes.Equal(a, b) {
			return errors.New("migration content verification failed")
		}
		var integrity string
		if err = tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
			return err
		}
		if integrity != "ok" {
			return errors.New("SQLite integrity check failed")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO migration_complete(id,manifest) VALUES(1,?)", string(b))
		return err
	})
	return report, err
}
func failIf(err error, wrap func(error) error) error {
	if err == nil {
		return nil
	}
	return wrap(err)
}
func importRead(ctx context.Context, tx *sql.Tx, r readRecord, d Manifest) error {
	if err := checkIDs(r.User, r.Channel); err != nil {
		return err
	}
	if r.Root < 0 || r.Seq < 0 {
		return errors.New("invalid read state")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO read_state(user_id,channel_id,root,last_seq) VALUES(?,?,?,?)", r.User, r.Channel, r.Root, r.Seq); err != nil {
		return err
	}
	return d.add("read_state", fmt.Sprintf("%s/%s/%d", r.User, r.Channel, r.Root), r)
}
func importMeta(ctx context.Context, tx *sql.Tx, kind, id string, b []byte, d Manifest) error {
	var v any
	var actual string
	var version int
	switch kind {
	case "users":
		var x domain.User
		if err := decodeStrict(b, &x); err != nil {
			return err
		}
		actual = x.ID
		version = x.Version
		v = x
	case "groups":
		var x domain.Group
		if err := decodeStrict(b, &x); err != nil {
			return err
		}
		actual = x.ID
		version = x.Version
		v = x
	case "channels":
		var x domain.Channel
		if err := decodeStrict(b, &x); err != nil {
			return err
		}
		actual = x.ID
		version = x.Version
		v = x
	case "sessions":
		var x domain.Session
		if err := decodeStrict(b, &x); err != nil {
			return err
		}
		actual = x.TokenHash
		version = x.Version
		v = x
	case "schedules":
		var x domain.Schedule
		if err := decodeStrict(b, &x); err != nil {
			return err
		}
		actual = x.ID
		version = x.Version
		v = x
	case "polls":
		var x domain.Poll
		if err := decodeStrict(b, &x); err != nil {
			return err
		}
		actual = x.ID
		version = x.Version
		v = x
	default:
		return errors.New("unknown metadata kind")
	}
	if actual != id {
		return errors.New("metadata identifier does not match filename")
	}
	if err := supportedVersion(version); err != nil {
		return err
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+kind+"(id,data) VALUES(?,?)", id, string(canonical)); err != nil {
		return err
	}
	return d.add(kind, id, v)
}
func validateReferences(ctx context.Context, tx *sql.Tx) error {
	var hasAI int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('messages') WHERE name='ai'").Scan(&hasAI); err != nil {
		return err
	}
	missingAuthor := "NOT EXISTS(SELECT 1 FROM users u WHERE u.id=m.user_id)"
	if hasAI != 0 {
		missingAuthor = "(m.ai IS NULL AND " + missingAuthor + ")"
	}
	checks := []struct{ name, query string }{
		{"channel counter", `SELECT 1 FROM messages m GROUP BY channel_id HAVING MAX(seq)>COALESCE((SELECT last_seq FROM counters c WHERE c.kind='channel' AND c.id=m.channel_id),-1) LIMIT 1`},
		{"schedule counter", `SELECT 1 FROM response_events r GROUP BY schedule_id HAVING MAX(seq)<>COALESCE((SELECT last_seq FROM counters c WHERE c.kind='schedule' AND c.id=r.schedule_id),-1) LIMIT 1`},
		{"read watermark", `SELECT 1 FROM read_state r WHERE r.last_seq>COALESCE((SELECT last_seq FROM counters c WHERE c.kind='channel' AND c.id=r.channel_id),0) LIMIT 1`},
		{"message channel/user", "SELECT 1 FROM messages m WHERE NOT EXISTS(SELECT 1 FROM channels c WHERE c.id=m.channel_id) OR " + missingAuthor + " LIMIT 1"},
		{"thread root", `SELECT 1 FROM messages m WHERE root>0 AND NOT EXISTS(SELECT 1 FROM messages p WHERE p.channel_id=m.channel_id AND p.seq=m.root AND p.root=0) LIMIT 1`},
		{"reaction reference", `SELECT 1 FROM reaction_events r WHERE NOT EXISTS(SELECT 1 FROM messages m WHERE m.channel_id=r.channel_id AND m.seq=r.seq) OR NOT EXISTS(SELECT 1 FROM users u WHERE u.id=r.user_id) LIMIT 1`},
		{"response reference", `SELECT 1 FROM response_events r WHERE NOT EXISTS(SELECT 1 FROM schedules s WHERE s.id=r.schedule_id) OR NOT EXISTS(SELECT 1 FROM users u WHERE u.id=r.user_id) LIMIT 1`},
		{"schedule channel", `SELECT 1 FROM schedules s WHERE NOT EXISTS(SELECT 1 FROM channels c WHERE c.id=json_extract(s.data,'$.channelId')) LIMIT 1`},
		{"schedule message", `SELECT 1 FROM messages m WHERE schedule_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM schedules s WHERE s.id=m.schedule_id AND json_extract(s.data,'$.channelId')=m.channel_id) LIMIT 1`},
		{"poll counter", `SELECT 1 FROM poll_response_events r GROUP BY poll_id HAVING MAX(seq)<>COALESCE((SELECT last_seq FROM counters c WHERE c.kind='poll' AND c.id=r.poll_id),-1) LIMIT 1`},
		{"poll response reference", `SELECT 1 FROM poll_response_events r WHERE NOT EXISTS(SELECT 1 FROM polls p WHERE p.id=r.poll_id) OR NOT EXISTS(SELECT 1 FROM users u WHERE u.id=r.user_id) LIMIT 1`},
		{"poll channel", `SELECT 1 FROM polls p WHERE NOT EXISTS(SELECT 1 FROM channels c WHERE c.id=json_extract(p.data,'$.channelId')) LIMIT 1`},
		{"poll message", `SELECT 1 FROM messages m WHERE poll_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM polls p WHERE p.id=m.poll_id AND json_extract(p.data,'$.channelId')=m.channel_id) LIMIT 1`},
		{"group membership", `SELECT 1 FROM user_groups ug WHERE NOT EXISTS(SELECT 1 FROM groups g WHERE g.id=ug.group_id) LIMIT 1`},
		{"channel group", `SELECT 1 FROM channel_groups cg WHERE NOT EXISTS(SELECT 1 FROM groups g WHERE g.id=cg.group_id) LIMIT 1`},
		{"channel membership", `SELECT 1 FROM channel_members cm WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=cm.user_id) LIMIT 1`},
		{"channel manager", `SELECT 1 FROM channel_managers cm WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=cm.user_id) LIMIT 1`},
		{"read owner/channel", `SELECT 1 FROM read_state r WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=r.user_id) OR NOT EXISTS(SELECT 1 FROM channels c WHERE c.id=r.channel_id) LIMIT 1`},
		{"thread read root", `SELECT 1 FROM read_state r WHERE root>0 AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.channel_id=r.channel_id AND m.seq=r.root AND m.root=0) LIMIT 1`},
		{"session owner", `SELECT 1 FROM sessions s WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=json_extract(s.data,'$.userId')) LIMIT 1`},
		{"mention read owner", `SELECT 1 FROM mention_reads r WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=r.user_id) LIMIT 1`},
		{"mention read message", `SELECT 1 FROM mention_reads r WHERE NOT EXISTS(SELECT 1 FROM messages m WHERE m.id=r.message_id) LIMIT 1`},
		{"snapshot mention recipient", `SELECT 1 FROM mention_refs r WHERE kind='user' AND NOT EXISTS(SELECT 1 FROM users u WHERE u.id=r.target) LIMIT 1`},
	}
	for _, c := range checks {
		var n int
		err := tx.QueryRowContext(ctx, c.query).Scan(&n)
		if err == nil {
			return fmt.Errorf("invalid %s", c.name)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign key validation failed")
	}
	return rows.Err()
}
func databaseManifest(ctx context.Context, tx *sql.Tx) (Manifest, error) {
	d := Manifest{}
	for _, kind := range []string{"users", "groups", "channels", "sessions", "schedules", "polls"} {
		rows, err := tx.QueryContext(ctx, "SELECT id,data FROM "+kind)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var b []byte
			if err = rows.Scan(&id, &b); err != nil {
				rows.Close()
				return nil, err
			}
			if err = d.add(kind, id, json.RawMessage(b)); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	var hasAI int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('messages') WHERE name='ai'").Scan(&hasAI); err != nil {
		return nil, err
	}
	columns := messageColumns
	if hasAI == 0 {
		columns = strings.TrimSuffix(columns, ",ai") + ",NULL"
	}
	queries := []struct{ kind, query string }{
		{"messages", "SELECT channel_id," + columns + " FROM messages"},
		{"reaction_events", "SELECT event_id,channel_id,seq,user_id,key,active,ts,version FROM reaction_events"},
		{"response_events", "SELECT schedule_id,seq,data FROM response_events"},
		{"poll_response_events", "SELECT poll_id,seq,data FROM poll_response_events"},
		{"read_state", "SELECT user_id,channel_id,root,last_seq FROM read_state"},
		{"mention_reads", "SELECT user_id,message_id,read_at FROM mention_reads"},
	}
	var withdrawalTable int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='withdrawals'").Scan(&withdrawalTable); err != nil {
		return nil, err
	}
	if withdrawalTable == 1 {
		queries = append(queries, struct{ kind, query string }{"withdrawals", "SELECT kind,channel_id,target_id,data FROM withdrawals"})
	}
	for _, q := range queries {
		rows, err := tx.QueryContext(ctx, q.query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var v any
			switch q.kind {
			case "withdrawals":
				var kind, ch, id string
				var b []byte
				err = rows.Scan(&kind, &ch, &id, &b)
				key = kind + "/" + ch + "/" + id
				v = json.RawMessage(b)
			case "messages":
				var ch, ts string
				var ids, sch, event, poll, ai sql.NullString
				var m domain.Message
				err = rows.Scan(&ch, &m.Seq, &m.ID, &ts, &m.UserID, &m.Text, &m.ThreadRootSeq, &ids, &sch, &event, &poll, &ai)
				if err == nil {
					m.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
				}
				if err == nil && ids.Valid {
					err = json.Unmarshal([]byte(ids.String), &m.MentionUserIDs)
				}
				if sch.Valid {
					m.ScheduleRef = &domain.ScheduleReference{ID: sch.String, Event: event.String}
				}
				if poll.Valid {
					m.PollRef = &domain.PollReference{ID: poll.String}
				}
				if err == nil && ai.Valid {
					err = json.Unmarshal([]byte(ai.String), &m.AI)
				}
				key = ch + "/" + strconv.FormatInt(m.Seq, 10)
				v = m
			case "reaction_events":
				var eid int64
				var ch, ts string
				var r domain.ReactionEvent
				err = rows.Scan(&eid, &ch, &r.MessageSeq, &r.UserID, &r.Key, &r.Active, &ts, &r.Version)
				if err == nil {
					r.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
				}
				key = ch + "/" + strconv.FormatInt(eid, 10)
				v = r
			case "response_events", "poll_response_events":
				var id string
				var seq int64
				var b []byte
				err = rows.Scan(&id, &seq, &b)
				key = id + "/" + strconv.FormatInt(seq, 10)
				v = json.RawMessage(b)
			case "read_state":
				var r readRecord
				err = rows.Scan(&r.User, &r.Channel, &r.Root, &r.Seq)
				key = fmt.Sprintf("%s/%s/%d", r.User, r.Channel, r.Root)
				v = r
			case "mention_reads":
				var r mentionReadRecord
				var ts string
				err = rows.Scan(&r.User, &r.Message, &ts)
				if err == nil {
					r.At, err = time.Parse(time.RFC3339Nano, ts)
				}
				key = r.User + "/" + r.Message
				v = r
			}
			if err != nil {
				rows.Close()
				return nil, err
			}
			if err = d.add(q.kind, key, v); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

// VerifyMigration checks a committed import before publishing, including recovery
// after interruption between database rename and marker replacement.
func (s *Store) VerifyMigration(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var expected string
		if err := tx.QueryRowContext(ctx, "SELECT manifest FROM migration_complete WHERE id=1").Scan(&expected); err != nil {
			return errors.New("database is not a completed import")
		}
		actual, err := databaseManifest(ctx, tx)
		if err != nil {
			return err
		}
		b, err := json.Marshal(actual)
		if err != nil {
			return err
		}
		if string(b) != expected {
			return errors.New("import verification mismatch")
		}
		return validateReferences(ctx, tx)
	})
}
func (d Digest) String() string {
	return fmt.Sprintf("%d records (%s)", d.Count, hex.EncodeToString(d.Sum[:]))
}
