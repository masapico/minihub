package filestore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

func (s *FileStorage) findPollMessage(ctx context.Context, ch, id string) (*domain.Message, error) {
	after := int64(0)
	for {
		idx, err := s.positionsLocked(ctx, ch)
		if err != nil {
			return nil, err
		}
		page, err := s.positionPage(ctx, idx, storage.MessageQuery{AfterSeq: &after, Limit: 500})
		if err != nil {
			return nil, err
		}
		for i := range page.Messages {
			m := &page.Messages[i]
			if m.PollRef != nil && m.PollRef.ID == id {
				return m, nil
			}
		}
		if page.NextAfter == nil {
			return nil, nil
		}
		after = *page.NextAfter
	}
}

func (s *FileStorage) pollPath(id string) string { return filepath.Join(s.root, "polls", id) }

func (s *FileStorage) GetPoll(ctx context.Context, id string) (*domain.Poll, error) {
	if err := checkID("poll", id); err != nil {
		return nil, err
	}
	var p domain.Poll
	if err := readJSON(ctx, filepath.Join(s.pollPath(id), "meta.json"), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *FileStorage) ListPolls(ctx context.Context) ([]domain.Poll, error) {
	paths, err := filepath.Glob(filepath.Join(s.root, "polls", "*", "meta.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]domain.Poll, 0, len(paths))
	for _, path := range paths {
		var p domain.Poll
		if err := readJSON(ctx, path, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *FileStorage) SavePoll(ctx context.Context, p *domain.Poll) error {
	if p == nil {
		return errors.New("poll required")
	}
	if err := checkID("poll", p.ID); err != nil {
		return err
	}
	if err := checkID("channel", p.ChannelID); err != nil {
		return err
	}
	if p.Version == 0 {
		p.Version = domain.Version
	}
	l := s.keyedLock(s.scheduleWriters, "poll:"+p.ID)
	l.Lock()
	defer l.Unlock()
	return atomicJSON(ctx, filepath.Join(s.pollPath(p.ID), "meta.json"), p)
}

func readPollEvents(path string) ([]domain.PollResponse, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []domain.PollResponse{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	for scan.Scan() {
		var r domain.PollResponse
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("decode poll responses: %w", err)
		}
		out = append(out, r)
	}
	return out, scan.Err()
}

func (s *FileStorage) AddPollResponse(ctx context.Context, id string, r domain.PollResponse) (domain.PollResponse, error) {
	if err := checkID("poll", id); err != nil {
		return r, err
	}
	if err := checkID("user", r.UserID); err != nil {
		return r, err
	}
	l := s.keyedLock(s.scheduleWriters, "poll:"+id)
	l.Lock()
	defer l.Unlock()
	path := filepath.Join(s.pollPath(id), "responses.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return r, err
	}
	if err := truncateIncompleteTail(path); err != nil {
		return r, err
	}
	events, err := readPollEvents(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	r.Seq = 1
	if len(events) > 0 {
		r.Seq = events[len(events)-1].Seq + 1
	}
	r.Version = domain.Version
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return r, err
	}
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return r, err
	}
	return r, nil
}

func (s *FileStorage) ListPollResponses(ctx context.Context, id string) ([]domain.PollResponse, error) {
	if err := checkID("poll", id); err != nil {
		return nil, err
	}
	l := s.keyedLock(s.scheduleWriters, "poll:"+id)
	l.Lock()
	defer l.Unlock()
	path := filepath.Join(s.pollPath(id), "responses.jsonl")
	if err := truncateIncompleteTail(path); err != nil {
		return nil, err
	}
	events, err := readPollEvents(path)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.PollResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	latest := map[string]domain.PollResponse{}
	for _, r := range events {
		latest[r.UserID] = r
	}
	out := make([]domain.PollResponse, 0, len(latest))
	for _, r := range latest {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}
