package sqlitestore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

const messageColumns = `seq,id,ts,user_id,text,root,mention_ids,schedule_id,schedule_event,poll_id,ai,attachments`

type scanner interface{ Scan(...any) error }

func scanMessage(row scanner) (domain.Message, error) {
	var m domain.Message
	var ts string
	var mentions, sch, event, poll, ai, attachments sql.NullString
	err := row.Scan(&m.Seq, &m.ID, &ts, &m.UserID, &m.Text, &m.ThreadRootSeq, &mentions, &sch, &event, &poll, &ai, &attachments)
	if err != nil {
		return m, missing(err)
	}
	m.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return m, err
	}
	if mentions.Valid {
		if err = json.Unmarshal([]byte(mentions.String), &m.MentionUserIDs); err != nil {
			return m, err
		}
	}
	if sch.Valid {
		m.ScheduleRef = &domain.ScheduleReference{ID: sch.String, Event: event.String}
	}
	if poll.Valid {
		m.PollRef = &domain.PollReference{ID: poll.String}
	}
	if ai.Valid {
		if err = json.Unmarshal([]byte(ai.String), &m.AI); err != nil {
			return m, err
		}
	}
	if attachments.Valid {
		if err = json.Unmarshal([]byte(attachments.String), &m.Attachments); err != nil {
			return m, err
		}
	}
	return m, nil
}

var legacyMention = regexp.MustCompile(`(?:^|[[:space:]])@(?:(group|ai):)?([A-Za-z0-9][A-Za-z0-9_-]{0,63})`)

func insertMessage(ctx context.Context, tx *sql.Tx, ch string, m domain.Message) error {
	if err := checkIDs(ch, m.UserID, m.ID); err != nil {
		return err
	}
	if m.Seq < 1 || m.Text == "" || m.Timestamp.IsZero() || m.ThreadRootSeq < 0 || m.ThreadRootSeq >= m.Seq {
		return errors.New("invalid message")
	}
	var mentions, sch, event, poll, ai, attachments any
	if m.AI != nil {
		b, err := json.Marshal(m.AI)
		if err != nil {
			return err
		}
		ai = string(b)
	}
	if len(m.Attachments) > 0 {
		b, err := json.Marshal(m.Attachments)
		if err != nil {
			return err
		}
		attachments = string(b)
	}
	if m.MentionUserIDs != nil {
		b, err := json.Marshal(m.MentionUserIDs)
		if err != nil {
			return err
		}
		mentions = string(b)
	}
	if m.ScheduleRef != nil {
		if err := checkIDs(m.ScheduleRef.ID); err != nil {
			return err
		}
		if m.ScheduleRef.Event == "" {
			return errors.New("schedule event is required")
		}
		sch = m.ScheduleRef.ID
		event = m.ScheduleRef.Event
	}
	if m.PollRef != nil {
		if err := checkIDs(m.PollRef.ID); err != nil {
			return err
		}
		poll = m.PollRef.ID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO messages(channel_id,seq,id,ts,ts_ns,user_id,text,root,mention_ids,schedule_id,schedule_event,poll_id,ai,attachments) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ch, m.Seq, m.ID, m.Timestamp.Format(time.RFC3339Nano), m.Timestamp.UnixNano(), m.UserID, m.Text, m.ThreadRootSeq, mentions, sch, event, poll, ai, attachments)
	if err != nil {
		return err
	}
	refs := [][2]string{}
	if m.MentionUserIDs != nil {
		for _, u := range m.MentionUserIDs {
			if err := checkIDs(u); err != nil {
				return err
			}
			refs = append(refs, [2]string{"user", u})
		}
	} else {
		for _, v := range legacyMention.FindAllStringSubmatch(m.Text, -1) {
			if v[1] == "ai" {
				continue
			}
			kind := "legacy_user"
			if v[1] == "group" {
				kind = "legacy_group"
			}
			refs = append(refs, [2]string{kind, v[2]})
		}
	}
	for _, r := range refs {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO mention_refs(message_id,kind,target) VALUES(?,?,?)", m.ID, r[0], r[1]); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) AddMessage(ctx context.Context, ch string, m domain.Message) (domain.Message, error) {
	if err := checkIDs(ch, m.UserID); err != nil {
		return domain.Message{}, err
	}
	if m.Text == "" || m.ThreadRootSeq < 0 {
		return domain.Message{}, errors.New("invalid message")
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if m.ThreadRootSeq > 0 {
			var root int64
			if err := tx.QueryRowContext(ctx, "SELECT root FROM messages WHERE channel_id=? AND seq=?", ch, m.ThreadRootSeq).Scan(&root); err != nil {
				return missing(err)
			}
			if root != 0 {
				return os.ErrNotExist
			}
		}
		if m.ScheduleRef != nil {
			old, err := scanMessage(tx.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE channel_id=? AND schedule_id=? AND schedule_event=?", ch, m.ScheduleRef.ID, m.ScheduleRef.Event))
			if err == nil {
				m = old
				return nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if m.PollRef != nil {
			old, err := scanMessage(tx.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE channel_id=? AND poll_id=?", ch, m.PollRef.ID))
			if err == nil {
				m = old
				return nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		var err error
		m.Seq, err = nextSeq(ctx, tx, "channel", ch)
		if err != nil {
			return err
		}
		if m.ID == "" {
			var b [16]byte
			if _, err = rand.Read(b[:]); err != nil {
				return err
			}
			m.ID = hex.EncodeToString(b[:])
		}
		if m.Timestamp.IsZero() {
			m.Timestamp = time.Now()
		}
		return insertMessage(ctx, tx, ch, m)
	})
	if err != nil {
		return domain.Message{}, err
	}
	return m, nil
}
func (s *Store) GetMessage(ctx context.Context, ch string, seq int64) (domain.Message, error) {
	if err := checkIDs(ch); err != nil {
		return domain.Message{}, err
	}
	return scanMessage(s.read.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE channel_id=? AND seq=?", ch, seq))
}
func (s *Store) LatestMessageSeq(ctx context.Context, ch string) (int64, error) {
	if err := checkIDs(ch); err != nil {
		return 0, err
	}
	var n int64
	err := s.read.QueryRowContext(ctx, "SELECT COALESCE((SELECT last_seq FROM counters WHERE kind='channel' AND id=?),0)", ch).Scan(&n)
	return n, err
}
func (s *Store) MessageReadCounts(ctx context.Context, ch string, read int64) (int64, int, error) {
	if err := checkIDs(ch); err != nil {
		return 0, 0, err
	}
	var latest int64
	var n int
	err := s.read.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0),COUNT(CASE WHEN seq>? THEN 1 END) FROM messages WHERE channel_id=? AND root=0", read, ch).Scan(&latest, &n)
	return latest, n, err
}

func queryMessages(ctx context.Context, tx *sql.Tx, where string, args []any, order string, limit int) ([]domain.Message, error) {
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE "+where+" ORDER BY seq "+order+" LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) GetMessages(ctx context.Context, ch string, q storage.MessageQuery) (storage.MessagePage, error) {
	page := storage.MessagePage{Messages: []domain.Message{}}
	if err := checkIDs(ch); err != nil {
		return page, err
	}
	n := 0
	for _, v := range []*int64{q.BeforeSeq, q.AfterSeq, q.AroundSeq} {
		if v != nil {
			n++
		}
	}
	if q.Limit < 1 || n > 1 || q.ThreadRootSeq < 0 || q.BeforeSeq != nil && *q.BeforeSeq < 1 || q.AfterSeq != nil && *q.AfterSeq < 0 || q.AroundSeq != nil && *q.AroundSeq < 1 {
		return page, errors.New("invalid message query")
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if q.ThreadRootSeq > 0 {
		var root int64
		if err = tx.QueryRowContext(ctx, "SELECT root FROM messages WHERE channel_id=? AND seq=?", ch, q.ThreadRootSeq).Scan(&root); err != nil {
			return page, missing(err)
		}
		if root != 0 {
			return page, os.ErrNotExist
		}
	}
	base := "channel_id=? AND root=?"
	args := []any{ch, q.ThreadRootSeq}
	where := base
	order := "DESC"
	if q.AroundSeq != nil {
		left, e := queryMessages(ctx, tx, base+" AND seq<?", []any{ch, q.ThreadRootSeq, *q.AroundSeq}, "DESC", q.Limit/2)
		if e != nil {
			return page, e
		}
		slices.Reverse(left)
		right, e := queryMessages(ctx, tx, base+" AND seq>=?", []any{ch, q.ThreadRootSeq, *q.AroundSeq}, "ASC", q.Limit-len(left))
		if e != nil {
			return page, e
		}
		page.Messages = append(left, right...)
	} else {
		if q.AfterSeq != nil {
			where += " AND seq>?"
			args = append(args, *q.AfterSeq)
			order = "ASC"
		} else if q.BeforeSeq != nil {
			where += " AND seq<?"
			args = append(args, *q.BeforeSeq)
		}
		page.Messages, err = queryMessages(ctx, tx, where, args, order, q.Limit)
		if err != nil {
			return page, err
		}
		if order == "DESC" {
			slices.Reverse(page.Messages)
		}
	}
	if len(page.Messages) > 0 {
		first, last := page.Messages[0].Seq, page.Messages[len(page.Messages)-1].Seq
		var before, after bool
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE "+base+" AND seq<?),EXISTS(SELECT 1 FROM messages WHERE "+base+" AND seq>?)", ch, q.ThreadRootSeq, first, ch, q.ThreadRootSeq, last).Scan(&before, &after)
		if err != nil {
			return page, err
		}
		if before && (q.AfterSeq == nil || q.ThreadRootSeq > 0) {
			page.NextBefore = &first
		}
		if after && (q.BeforeSeq == nil && q.AfterSeq != nil || q.AroundSeq != nil || q.ThreadRootSeq > 0) {
			page.NextAfter = &last
		}
	}
	return page, tx.Commit()
}
func (s *Store) ThreadSummaries(ctx context.Context, user, ch string, filters ...storage.ThreadFilter) ([]storage.ThreadSummary, error) {
	if err := checkIDs(user, ch); err != nil {
		return nil, err
	}
	f := storage.ThreadFilter{}
	if len(filters) > 0 {
		f = filters[0]
	}
	args := []any{user, user, user, user, ch}
	where := "m.channel_id=? AND m.root>0"
	if len(f.Roots) > 0 {
		where += " AND m.root IN (" + strings.TrimRight(strings.Repeat("?,", len(f.Roots)), ",") + ")"
		for _, r := range f.Roots {
			args = append(args, r)
		}
	}
	having := ""
	if f.Participating {
		having = " WHERE (p.user_id=? OR a.own>0)"
	}
	args = append(args, user)
	if f.Participating {
		args = append(args, user)
	}
	rows, err := s.read.QueryContext(ctx, `WITH a AS (
 SELECT m.channel_id,m.root,COUNT(*) replies,MAX(m.seq) latest,COALESCE(r.last_seq,0) last_read,
 COUNT(CASE WHEN m.seq>COALESCE(r.last_seq,0) AND m.user_id<>? THEN 1 END) unread,
 COALESCE(MIN(CASE WHEN m.seq>COALESCE(r.last_seq,0) AND m.user_id<>? THEN m.seq END),0) first_unread,
 SUM(m.user_id=?) own
 FROM messages m
 LEFT JOIN read_state r ON r.channel_id=m.channel_id AND r.root=m.root AND r.user_id=?
 WHERE `+where+` GROUP BY m.root)
 SELECT a.root,a.replies,a.latest,a.last_read,a.unread,a.first_unread,(p.user_id=? OR a.own>0),last.ts
 FROM a JOIN messages p INDEXED BY messages_thread ON p.channel_id=a.channel_id AND p.root=0 AND p.seq=a.root
 JOIN messages last INDEXED BY messages_thread ON last.channel_id=a.channel_id AND last.root=a.root AND last.seq=a.latest`+having,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storage.ThreadSummary{}
	for rows.Next() {
		v := storage.ThreadSummary{ChannelID: ch}
		var ts string
		if err = rows.Scan(&v.RootSeq, &v.ReplyCount, &v.LatestSeq, &v.LastReadSeq, &v.UnreadCount, &v.FirstUnreadSeq, &v.Participating, &ts); err != nil {
			return nil, err
		}
		if v.UpdatedAt, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
