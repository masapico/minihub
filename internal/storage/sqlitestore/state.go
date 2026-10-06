package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/masapico/minihub/internal/domain"
	"strings"
	"time"
)

func (s *Store) GetReadState(ctx context.Context, user, ch string) (int64, error) {
	if err := checkIDs(user, ch); err != nil {
		return 0, err
	}
	var seq int64
	err := s.read.QueryRowContext(ctx, "SELECT COALESCE((SELECT last_seq FROM read_state WHERE user_id=? AND channel_id=? AND root=0),0)", user, ch).Scan(&seq)
	return seq, err
}
func (s *Store) GetReadStates(ctx context.Context, user string) (map[string]int64, error) {
	if err := checkIDs(user); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, "SELECT channel_id,last_seq FROM read_state WHERE user_id=? AND root=0", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var ch string
		var seq int64
		if err = rows.Scan(&ch, &seq); err != nil {
			return nil, err
		}
		out[ch] = seq
	}
	return out, rows.Err()
}
func (s *Store) setRead(ctx context.Context, user, ch string, root, seq int64) error {
	if err := checkIDs(user, ch); err != nil {
		return err
	}
	if seq < 0 {
		return errors.New("invalid read sequence")
	}
	_, err := s.write.ExecContext(ctx, `INSERT INTO read_state(user_id,channel_id,root,last_seq) VALUES(?,?,?,?) ON CONFLICT(user_id,channel_id,root) DO UPDATE SET last_seq=MAX(last_seq,excluded.last_seq)`, user, ch, root, seq)
	return err
}
func (s *Store) SetReadState(ctx context.Context, user, ch string, seq int64) error {
	return s.setRead(ctx, user, ch, 0, seq)
}
func (s *Store) SetThreadReadState(ctx context.Context, user, ch string, root, seq int64) error {
	m, err := s.GetMessage(ctx, ch, seq)
	if err != nil {
		return err
	}
	if root < 1 || m.ThreadRootSeq != root {
		return errors.New("invalid thread read position")
	}
	return s.setRead(ctx, user, ch, root, seq)
}
func (s *Store) SetMentionRead(ctx context.Context, user, id string, at time.Time) error {
	if err := checkIDs(user, id); err != nil {
		return err
	}
	_, err := s.write.ExecContext(ctx, "INSERT OR IGNORE INTO mention_reads(user_id,message_id,read_at) VALUES(?,?,?)", user, id, at.Format(time.RFC3339Nano))
	return err
}
func (s *Store) ListMentions(ctx context.Context, user string) ([]domain.Mention, error) {
	if _, err := s.GetUser(ctx, user); err != nil {
		return nil, err
	}
	// Legacy group mentions intentionally follow current group membership, matching file storage.
	rows, err := s.read.QueryContext(ctx, `SELECT m.channel_id,m.seq,m.id,m.ts,m.user_id,m.text,m.root,m.mention_ids,m.schedule_id,m.schedule_event,r.read_at
 FROM messages m LEFT JOIN mention_reads r ON r.message_id=m.id AND r.user_id=?
 WHERE m.id IN (SELECT message_id FROM mention_refs WHERE (kind IN ('user','legacy_user') AND target=?) OR (kind='legacy_group' AND target IN (SELECT group_id FROM user_groups WHERE user_id=?))) ORDER BY m.ts_ns DESC,m.id`, user, user, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Mention{}
	for rows.Next() {
		var v domain.Mention
		var ts string
		var ids, sch, event, read sql.NullString
		m := &v.Message
		if err = rows.Scan(&v.ChannelID, &m.Seq, &m.ID, &ts, &m.UserID, &m.Text, &m.ThreadRootSeq, &ids, &sch, &event, &read); err != nil {
			return nil, err
		}
		if m.Timestamp, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, err
		}
		if ids.Valid {
			if err = json.Unmarshal([]byte(ids.String), &m.MentionUserIDs); err != nil {
				return nil, err
			}
		}
		if sch.Valid {
			m.ScheduleRef = &domain.ScheduleReference{ID: sch.String, Event: event.String}
		}
		if read.Valid {
			t, e := time.Parse(time.RFC3339Nano, read.String)
			if e != nil {
				return nil, e
			}
			v.ReadAt = &t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func insertReaction(ctx context.Context, tx *sql.Tx, ch string, e domain.ReactionEvent) error {
	if err := checkIDs(ch, e.UserID); err != nil {
		return err
	}
	if e.MessageSeq < 1 || e.Key == "" {
		return errors.New("invalid reaction")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO reaction_events(channel_id,seq,user_id,key,active,ts,version) VALUES(?,?,?,?,?,?,?)", ch, e.MessageSeq, e.UserID, e.Key, e.Active, e.Timestamp.Format(time.RFC3339Nano), e.Version); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO reactions(channel_id,seq,user_id,key,active) VALUES(?,?,?,?,?) ON CONFLICT(channel_id,seq,key,user_id) DO UPDATE SET active=excluded.active`, ch, e.MessageSeq, e.UserID, e.Key, e.Active)
	return err
}
func (s *Store) SetReaction(ctx context.Context, ch string, e domain.ReactionEvent) (bool, error) {
	if err := checkIDs(ch, e.UserID); err != nil {
		return false, err
	}
	if e.MessageSeq < 1 || e.Key == "" {
		return false, errors.New("invalid reaction")
	}
	changed := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var active bool
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT active FROM reactions WHERE channel_id=? AND seq=? AND key=? AND user_id=?),0)", ch, e.MessageSeq, e.Key, e.UserID).Scan(&active); err != nil {
			return err
		}
		if active == e.Active {
			return nil
		}
		e.Version = domain.Version
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now()
		}
		if err := insertReaction(ctx, tx, ch, e); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}
func (s *Store) GetReactions(ctx context.Context, ch string, after int64) ([]domain.ReactionState, error) {
	if err := checkIDs(ch); err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, errors.New("invalid reaction cursor")
	}
	rows, err := s.read.QueryContext(ctx, "SELECT seq,key,user_id FROM reactions WHERE channel_id=? AND seq>? AND active=1 ORDER BY seq,key,user_id", ch, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReactionState
	for rows.Next() {
		var seq int64
		var key, user string
		if err = rows.Scan(&seq, &key, &user); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].MessageSeq != seq || out[len(out)-1].Key != key {
			out = append(out, domain.ReactionState{MessageSeq: seq, Key: key, UserIDs: []string{}})
		}
		out[len(out)-1].UserIDs = append(out[len(out)-1].UserIDs, user)
	}
	return out, rows.Err()
}
func (s *Store) GetReactionsForMessages(ctx context.Context, ch string, seqs []int64) ([]domain.ReactionState, error) {
	if err := checkIDs(ch); err != nil {
		return nil, err
	}
	if len(seqs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(seqs)+1)
	args = append(args, ch)
	marks := make([]string, len(seqs))
	for i, seq := range seqs {
		if seq < 1 {
			return nil, errors.New("invalid message sequence")
		}
		marks[i] = "?"
		args = append(args, seq)
	}
	rows, err := s.read.QueryContext(ctx, "SELECT seq,key,user_id FROM reactions WHERE channel_id=? AND seq IN ("+strings.Join(marks, ",")+") AND active=1 ORDER BY seq,key,user_id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReactionState
	for rows.Next() {
		var seq int64
		var key, user string
		if err := rows.Scan(&seq, &key, &user); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].MessageSeq != seq || out[len(out)-1].Key != key {
			out = append(out, domain.ReactionState{MessageSeq: seq, Key: key})
		}
		out[len(out)-1].UserIDs = append(out[len(out)-1].UserIDs, user)
	}
	return out, rows.Err()
}

func (s *Store) GetReactionUsers(ctx context.Context, ch string, seq int64, key string) ([]string, error) {
	if err := checkIDs(ch); err != nil {
		return nil, err
	}
	if seq < 1 || key == "" {
		return nil, errors.New("invalid reaction")
	}
	rows, err := s.read.QueryContext(ctx, "SELECT user_id FROM reactions WHERE channel_id=? AND seq=? AND key=? AND active=1 ORDER BY user_id", ch, seq, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func insertResponse(ctx context.Context, tx *sql.Tx, id string, r domain.ScheduleResponse) error {
	if err := checkIDs(id, r.UserID); err != nil {
		return err
	}
	if r.Seq < 1 {
		return errors.New("invalid response sequence")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO response_events(schedule_id,seq,user_id,data) VALUES(?,?,?,?)", id, r.Seq, r.UserID, string(b))
	return err
}
func (s *Store) AddScheduleResponse(ctx context.Context, id string, r domain.ScheduleResponse) (domain.ScheduleResponse, error) {
	if err := checkIDs(id, r.UserID); err != nil {
		return domain.ScheduleResponse{}, err
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		r.Seq, err = nextSeq(ctx, tx, "schedule", id)
		if err != nil {
			return err
		}
		r.Version = domain.Version
		if r.UpdatedAt.IsZero() {
			r.UpdatedAt = time.Now()
		}
		return insertResponse(ctx, tx, id, r)
	})
	if err != nil {
		return domain.ScheduleResponse{}, err
	}
	return r, nil
}
func (s *Store) ListScheduleResponses(ctx context.Context, id string) ([]domain.ScheduleResponse, error) {
	if err := checkIDs(id); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT data FROM response_events e WHERE schedule_id=? AND seq=(SELECT MAX(seq) FROM response_events newer WHERE newer.schedule_id=e.schedule_id AND newer.user_id=e.user_id) ORDER BY user_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ScheduleResponse{}
	for rows.Next() {
		var b []byte
		var r domain.ScheduleResponse
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
