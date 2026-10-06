package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/masapico/minihub/internal/domain"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type PollCount struct {
	OptionID string  `json:"optionId"`
	Votes    int     `json:"votes"`
	Percent  float64 `json:"percent"`
}
type PollDetail struct {
	Poll               domain.Poll          `json:"poll"`
	Counts             []PollCount          `json:"counts"`
	Respondents        int                  `json:"respondents"`
	TotalVotes         int                  `json:"totalVotes"`
	MyResponse         *domain.PollResponse `json:"myResponse,omitempty"`
	CanManage          bool                 `json:"canManage"`
	CanWithdraw        bool                 `json:"canWithdraw"`
	CanRestore         bool                 `json:"canRestore"`
	AcceptingResponses bool                 `json:"acceptingResponses"`
	CanVote            bool                 `json:"canVote"`
}
type NewPoll struct {
	RequestID   string     `json:"requestId"`
	ChannelID   string     `json:"channelId"`
	Question    string     `json:"question"`
	Description string     `json:"description"`
	Options     []string   `json:"options"`
	Multiple    bool       `json:"multiple"`
	Deadline    *time.Time `json:"deadline"`
}

type PollPublisher interface {
	PollChanged(context.Context, string, string, int64)
}

func (s *Service) notifyPoll(ctx context.Context, p *domain.Poll) {
	if pub, ok := s.publisher.(PollPublisher); ok {
		pub.PollChanged(ctx, p.ChannelID, p.ID, p.Revision)
	}
}

func (s *Service) CreatePoll(ctx context.Context, user string, in NewPoll) (PollDetail, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	if err := s.canPost(ctx, user, in.ChannelID); err != nil {
		return PollDetail{}, err
	}
	question := strings.TrimSpace(in.Question)
	description := strings.TrimSpace(in.Description)
	if question == "" || utf8.RuneCountInString(question) > 200 || utf8.RuneCountInString(description) > 2000 || len(in.Options) < 2 || len(in.Options) > 10 {
		return PollDetail{}, fmt.Errorf("%w: 質問・説明・選択肢を確認してください", ErrInvalid)
	}
	opts := make([]domain.PollOption, 0, len(in.Options))
	seen := map[string]bool{}
	for i, v := range in.Options {
		v = strings.TrimSpace(v)
		if v == "" || utf8.RuneCountInString(v) > 100 || seen[v] {
			return PollDetail{}, fmt.Errorf("%w: 選択肢は空欄・重複なしの100文字以内にしてください", ErrInvalid)
		}
		seen[v] = true
		opts = append(opts, domain.PollOption{ID: fmt.Sprintf("o%d", i+1), Text: v})
	}
	id := in.RequestID
	if id != "" && !validUserID.MatchString(id) {
		return PollDetail{}, fmt.Errorf("%w: リクエストIDが不正です", ErrInvalid)
	}
	if id == "" {
		var err error
		id, err = scheduleID()
		if err != nil {
			return PollDetail{}, err
		}
	}
	now := time.Now()
	p := &domain.Poll{Version: domain.Version, Revision: 1, ID: id, ChannelID: in.ChannelID, Question: question, Description: description, Options: opts, Multiple: in.Multiple, Deadline: in.Deadline, Status: "open", CreatedBy: user, CreatedAt: now, UpdatedAt: now}
	existing, err := s.storage.GetPoll(ctx, id)
	if err == nil {
		if existing.CreatedBy != user || existing.ChannelID != in.ChannelID || existing.Question != question || existing.Description != description || existing.Multiple != in.Multiple || len(existing.Options) != len(opts) || !samePollDeadline(existing.Deadline, in.Deadline) {
			return PollDetail{}, ErrConflict
		}
		for i := range opts {
			if existing.Options[i] != opts[i] {
				return PollDetail{}, ErrConflict
			}
		}
		p = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return PollDetail{}, err
	}
	if existing == nil {
		if in.Deadline != nil && !time.Now().Before(*in.Deadline) {
			return PollDetail{}, fmt.Errorf("%w: 締切は未来にしてください", ErrInvalid)
		}
		if err := s.storage.SavePoll(ctx, p); err != nil {
			return PollDetail{}, err
		}
	}
	if p.AnnouncementSeq > 0 {
		return s.GetPoll(ctx, user, id)
	}
	m, err := s.storage.AddMessage(ctx, in.ChannelID, domain.Message{UserID: user, Text: "アンケートを開始しました: " + question, PollRef: &domain.PollReference{ID: id}})
	if err != nil {
		return PollDetail{}, err
	}
	p.AnnouncementSeq = m.Seq
	p.Revision++
	p.UpdatedAt = time.Now()
	if err := s.storage.SavePoll(ctx, p); err != nil {
		return PollDetail{}, err
	}
	if s.publisher != nil {
		s.publisher.NewMessage(ctx, in.ChannelID, m.Seq)
	}
	s.notifyPoll(ctx, p)
	return s.GetPoll(ctx, user, id)
}

func samePollDeadline(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (s *Service) poll(ctx context.Context, user, id string) (*domain.Poll, error) {
	p, err := s.storage.GetPoll(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.canRead(ctx, user, p.ChannelID); err != nil {
		return nil, ErrNotFound
	}
	return p, nil
}
func (s *Service) GetPoll(ctx context.Context, user, id string) (PollDetail, error) {
	p, err := s.poll(ctx, user, id)
	if err != nil {
		return PollDetail{}, err
	}
	withdrawn, err := s.withdrawal(ctx, "poll", p.ChannelID, id)
	if err != nil {
		return PollDetail{}, err
	}
	if withdrawn != nil {
		copy := *p
		copy.Question, copy.Description, copy.Options, copy.Deadline = "", "", nil, nil
		copy.Status = "withdrawn"
		return PollDetail{Poll: copy, CanRestore: s.canWithdraw(ctx, user, p.ChannelID, p.CreatedBy) == nil}, nil
	}
	responses, err := s.storage.ListPollResponses(ctx, id)
	if err != nil {
		return PollDetail{}, err
	}
	viewer, err := s.user(ctx, user)
	if err != nil {
		return PollDetail{}, err
	}
	canVote := s.canPost(ctx, user, p.ChannelID) == nil
	d := PollDetail{Poll: *p, Counts: make([]PollCount, len(p.Options)), CanManage: p.CreatedBy == user || viewer.Role == domain.RoleAdmin, CanWithdraw: s.canWithdraw(ctx, user, p.ChannelID, p.CreatedBy) == nil, CanVote: canVote, AcceptingResponses: p.Status == "open" && (p.Deadline == nil || time.Now().Before(*p.Deadline))}
	if !d.AcceptingResponses {
		d.Poll.Status = "closed"
	}
	for i, o := range p.Options {
		d.Counts[i].OptionID = o.ID
	}
	for _, r := range responses {
		d.Respondents++
		d.TotalVotes += len(r.OptionIDs)
		if r.UserID == user {
			copy := r
			d.MyResponse = &copy
		}
		for _, id := range r.OptionIDs {
			for i := range d.Counts {
				if d.Counts[i].OptionID == id {
					d.Counts[i].Votes++
				}
			}
		}
	}
	if d.Respondents > 0 {
		for i := range d.Counts {
			d.Counts[i].Percent = math.Round(float64(d.Counts[i].Votes)*1000/float64(d.Respondents)) / 10
		}
	}
	return d, nil
}
func (s *Service) ListPolls(ctx context.Context, user, channel string, activeOnly bool) ([]PollDetail, error) {
	all, err := s.storage.ListPolls(ctx)
	if err != nil {
		return nil, err
	}
	out := []PollDetail{}
	for _, p := range all {
		if withdrawn, err := s.withdrawal(ctx, "poll", p.ChannelID, p.ID); err != nil {
			return nil, err
		} else if withdrawn != nil {
			continue
		}
		if channel != "" && p.ChannelID != channel {
			continue
		}
		if activeOnly && (p.Status != "open" || (p.Deadline != nil && !time.Now().Before(*p.Deadline))) {
			continue
		}
		d, err := s.GetPoll(ctx, user, p.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Poll.CreatedAt.After(out[j].Poll.CreatedAt) })
	return out, nil
}
func (s *Service) VotePoll(ctx context.Context, user, id string, revision int64, ids []string) (PollDetail, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	p, err := s.poll(ctx, user, id)
	if err != nil {
		return PollDetail{}, err
	}
	if withdrawn, err := s.withdrawal(ctx, "poll", p.ChannelID, id); err != nil {
		return PollDetail{}, err
	} else if withdrawn != nil {
		return PollDetail{}, ErrInvalid
	}
	if err := s.canPost(ctx, user, p.ChannelID); err != nil {
		return PollDetail{}, err
	}
	if p.Status != "open" || (p.Deadline != nil && !time.Now().Before(*p.Deadline)) {
		return PollDetail{}, fmt.Errorf("%w: アンケートは終了しました", ErrInvalid)
	}
	if len(ids) == 0 || (!p.Multiple && len(ids) != 1) || len(ids) > len(p.Options) {
		return PollDetail{}, fmt.Errorf("%w: 選択肢を確認してください", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		valid := false
		for _, o := range p.Options {
			if o.ID == id {
				valid = true
				break
			}
		}
		if !valid || seen[id] {
			return PollDetail{}, fmt.Errorf("%w: 選択肢を確認してください", ErrInvalid)
		}
		seen[id] = true
	}
	responses, err := s.storage.ListPollResponses(ctx, id)
	if err != nil {
		return PollDetail{}, err
	}
	var current int64
	for _, r := range responses {
		if r.UserID == user {
			current = r.Seq
		}
	}
	if current != revision {
		return PollDetail{}, fmt.Errorf("%w: 回答が更新されています。再読み込みしてください", ErrStale)
	}
	if _, err := s.storage.AddPollResponse(ctx, id, domain.PollResponse{UserID: user, OptionIDs: ids, UpdatedAt: time.Now()}); err != nil {
		return PollDetail{}, err
	}
	s.notifyPoll(ctx, p)
	return s.GetPoll(ctx, user, id)
}
func (s *Service) ClosePoll(ctx context.Context, user, id string, revision int64) (PollDetail, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	p, err := s.poll(ctx, user, id)
	if err != nil {
		return PollDetail{}, err
	}
	if withdrawn, err := s.withdrawal(ctx, "poll", p.ChannelID, id); err != nil {
		return PollDetail{}, err
	} else if withdrawn != nil {
		return PollDetail{}, ErrInvalid
	}
	viewer, err := s.user(ctx, user)
	if err != nil {
		return PollDetail{}, err
	}
	if p.CreatedBy != user && viewer.Role != domain.RoleAdmin {
		return PollDetail{}, ErrForbidden
	}
	if p.Revision != revision {
		return PollDetail{}, ErrStale
	}
	if p.Status != "open" {
		return PollDetail{}, ErrInvalid
	}
	p.Status = "closed"
	p.Revision++
	p.UpdatedAt = time.Now()
	if err := s.storage.SavePoll(ctx, p); err != nil {
		return PollDetail{}, err
	}
	s.notifyPoll(ctx, p)
	return s.GetPoll(ctx, user, id)
}
