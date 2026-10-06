package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/masapico/minihub/internal/domain"
	"time"
)

func (s *Store) GetPoll(ctx context.Context, id string) (*domain.Poll, error) {
	return get[domain.Poll](ctx, s, "polls", id)
}
func (s *Store) ListPolls(ctx context.Context) ([]domain.Poll, error) {
	return list[domain.Poll](ctx, s, "polls")
}
func (s *Store) SavePoll(ctx context.Context, p *domain.Poll) error {
	if p == nil {
		return errors.New("poll required")
	}
	if p.Version == 0 {
		p.Version = domain.Version
	}
	if err := checkIDs(p.ChannelID); err != nil {
		return err
	}
	return s.save(ctx, "polls", p.ID, p)
}
func (s *Store) AddPollResponse(ctx context.Context, id string, r domain.PollResponse) (domain.PollResponse, error) {
	if err := checkIDs(id, r.UserID); err != nil {
		return r, err
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		r.Seq, err = nextSeq(ctx, tx, "poll", id)
		if err != nil {
			return err
		}
		r.Version = domain.Version
		if r.UpdatedAt.IsZero() {
			r.UpdatedAt = time.Now()
		}
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO poll_response_events(poll_id,seq,user_id,data) VALUES(?,?,?,?)", id, r.Seq, r.UserID, string(b))
		return err
	})
	return r, err
}
func (s *Store) ListPollResponses(ctx context.Context, id string) ([]domain.PollResponse, error) {
	if err := checkIDs(id); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT data FROM poll_response_events e WHERE poll_id=? AND seq=(SELECT MAX(seq) FROM poll_response_events newer WHERE newer.poll_id=e.poll_id AND newer.user_id=e.user_id) ORDER BY user_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PollResponse{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var r domain.PollResponse
		if err := json.Unmarshal([]byte(b), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
