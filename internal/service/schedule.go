package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/masapico/minihub/internal/domain"
)

const (
	MaxScheduleTitleRunes       = 100
	MaxScheduleDescriptionRunes = 2000
	MaxScheduleCommentRunes     = 500
	MaxScheduleCandidates       = 50
)

type SchedulePublisher interface {
	ScheduleChanged(context.Context, string, string, int64)
}

type ScheduleAggregate struct {
	CandidateID string `json:"candidateId"`
	Yes         int    `json:"yes"`
	Maybe       int    `json:"maybe"`
	No          int    `json:"no"`
}

type ScheduleDetail struct {
	Schedule           domain.Schedule           `json:"schedule"`
	Responses          []domain.ScheduleResponse `json:"responses"`
	Aggregates         []ScheduleAggregate       `json:"aggregates"`
	CanManage          bool                      `json:"canManage"`
	CanWithdraw        bool                      `json:"canWithdraw"`
	CanRestore         bool                      `json:"canRestore"`
	AcceptingResponses bool                      `json:"acceptingResponses"`
}

type SchedulePage struct {
	Schedules  []domain.Schedule `json:"schedules"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type NewSchedule struct {
	ChannelID        string
	Title            string
	Description      string
	Candidates       []domain.ScheduleCandidate
	ResponseDeadline *time.Time
}

type UpdateSchedule struct {
	Revision         int64
	Title            string
	Description      string
	Candidates       []domain.ScheduleCandidate
	ResponseDeadline *time.Time
}

func scheduleID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func normalizeSchedule(input NewSchedule) (string, string, []domain.ScheduleCandidate, error) {
	title := strings.TrimSpace(input.Title)
	description := strings.TrimSpace(input.Description)
	if title == "" || utf8.RuneCountInString(title) > MaxScheduleTitleRunes {
		return "", "", nil, fmt.Errorf("%w: タイトルは必須で、%d文字以内にしてください", ErrInvalid, MaxScheduleTitleRunes)
	}
	if utf8.RuneCountInString(description) > MaxScheduleDescriptionRunes {
		return "", "", nil, fmt.Errorf("%w: 説明は%d文字以内にしてください", ErrInvalid, MaxScheduleDescriptionRunes)
	}
	if len(input.Candidates) < 2 || len(input.Candidates) > MaxScheduleCandidates {
		return "", "", nil, fmt.Errorf("%w: 候補は2件以上%d件以下で指定してください", ErrInvalid, MaxScheduleCandidates)
	}
	candidates := append([]domain.ScheduleCandidate(nil), input.Candidates...)
	seenID, seenValue := map[string]bool{}, map[string]bool{}
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.ID == "" {
			id, err := scheduleID()
			if err != nil {
				return "", "", nil, err
			}
			candidate.ID = id
		}
		if !validUserID.MatchString(candidate.ID) || seenID[candidate.ID] {
			return "", "", nil, fmt.Errorf("%w: 候補IDは重複できません", ErrInvalid)
		}
		seenID[candidate.ID] = true
		if _, err := time.Parse("2006-01-02", candidate.Date); err != nil {
			return "", "", nil, fmt.Errorf("%w: 候補日の日付は YYYY-MM-DD 形式で指定してください", ErrInvalid)
		}
		if candidate.StartTime == "" && candidate.EndTime != "" {
			return "", "", nil, fmt.Errorf("%w: 終了時刻を指定する場合は開始時刻も指定してください", ErrInvalid)
		}
		for _, value := range []string{candidate.StartTime, candidate.EndTime} {
			if value != "" {
				if _, err := time.Parse("15:04", value); err != nil {
					return "", "", nil, fmt.Errorf("%w: 候補時刻は HH:MM 形式で指定してください", ErrInvalid)
				}
			}
		}
		if candidate.EndTime != "" && candidate.EndTime <= candidate.StartTime {
			return "", "", nil, fmt.Errorf("%w: 終了時刻は開始時刻より後にしてください", ErrInvalid)
		}
		key := candidate.Date + "\x00" + candidate.StartTime + "\x00" + candidate.EndTime
		if seenValue[key] {
			return "", "", nil, fmt.Errorf("%w: 同じ日時の候補は重複して登録できません", ErrInvalid)
		}
		seenValue[key] = true
	}
	return title, description, candidates, nil
}

func (s *Service) CreateSchedule(ctx context.Context, userID string, input NewSchedule) (*domain.Schedule, error) {
	if err := s.canPost(ctx, userID, input.ChannelID); err != nil {
		return nil, err
	}
	title, description, candidates, err := normalizeSchedule(input)
	if err != nil {
		return nil, err
	}
	id, err := scheduleID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	schedule := &domain.Schedule{Version: domain.Version, Revision: 1, ID: id, ChannelID: input.ChannelID, Title: title, Description: description, TimeZone: "Asia/Tokyo", Candidates: candidates, ResponseDeadline: input.ResponseDeadline, Status: domain.ScheduleDraft, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if err := s.storage.SaveSchedule(ctx, schedule); err != nil {
		return nil, err
	}
	return schedule, nil
}

func (s *Service) schedule(ctx context.Context, userID, id string) (*domain.Schedule, *domain.User, error) {
	user, err := s.user(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	schedule, err := s.storage.GetSchedule(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if err := s.canRead(ctx, userID, schedule.ChannelID); err != nil {
		return nil, nil, ErrNotFound
	}
	if schedule.Status == domain.ScheduleDraft && schedule.CreatedBy != userID && user.Role != domain.RoleAdmin {
		return nil, nil, ErrNotFound
	}
	return schedule, user, nil
}

func effectiveSchedule(schedule domain.Schedule, now time.Time) domain.Schedule {
	if schedule.Status == domain.ScheduleOpen && schedule.ResponseDeadline != nil && !now.Before(*schedule.ResponseDeadline) {
		schedule.Status = domain.ScheduleClosed
	}
	return schedule
}

func (s *Service) GetSchedule(ctx context.Context, userID, id string) (ScheduleDetail, error) {
	schedule, user, err := s.schedule(ctx, userID, id)
	if err != nil {
		return ScheduleDetail{}, err
	}
	withdrawn, err := s.withdrawal(ctx, "schedule", schedule.ChannelID, id)
	if err != nil {
		return ScheduleDetail{}, err
	}
	if withdrawn != nil {
		copy := *schedule
		copy.Title, copy.Description, copy.Candidates, copy.ResponseDeadline, copy.FinalCandidateID = "", "", nil, nil, ""
		copy.Status = domain.ScheduleStatusWithdrawn
		return ScheduleDetail{Schedule: copy, CanRestore: s.canWithdraw(ctx, userID, schedule.ChannelID, schedule.CreatedBy) == nil}, nil
	}
	responses, err := s.storage.ListScheduleResponses(ctx, id)
	if err != nil {
		return ScheduleDetail{}, err
	}
	view := effectiveSchedule(*schedule, time.Now())
	aggregates := make([]ScheduleAggregate, len(view.Candidates))
	for i, candidate := range view.Candidates {
		aggregates[i].CandidateID = candidate.ID
	}
	index := make(map[string]int, len(aggregates))
	for i := range aggregates {
		index[aggregates[i].CandidateID] = i
	}
	for _, response := range responses {
		for candidateID, choice := range response.Choices {
			i, ok := index[candidateID]
			if !ok {
				continue
			}
			switch choice {
			case domain.ScheduleYes:
				aggregates[i].Yes++
			case domain.ScheduleMaybe:
				aggregates[i].Maybe++
			case domain.ScheduleNo:
				aggregates[i].No++
			}
		}
	}
	return ScheduleDetail{Schedule: view, Responses: responses, Aggregates: aggregates, CanManage: schedule.CreatedBy == userID || user.Role == domain.RoleAdmin, CanWithdraw: s.canWithdraw(ctx, userID, schedule.ChannelID, schedule.CreatedBy) == nil, AcceptingResponses: view.Status == domain.ScheduleOpen}, nil
}

func (s *Service) ListSchedules(ctx context.Context, userID, cursor string, limit int) (SchedulePage, error) {
	return s.ListSchedulesByPeriod(ctx, userID, cursor, limit, "all")
}

// scheduleIsPast compares calendar dates in Japan, independent of response status.
func scheduleIsPast(schedule domain.Schedule, now time.Time) bool {
	date := ""
	for _, candidate := range schedule.Candidates {
		if schedule.Status == domain.ScheduleFinalized && candidate.ID != schedule.FinalCandidateID {
			continue
		}
		if _, err := time.Parse("2006-01-02", candidate.Date); err != nil {
			return false
		}
		if candidate.Date > date {
			date = candidate.Date
		}
	}
	return date != "" && date < now.In(time.FixedZone("JST", 9*60*60)).Format("2006-01-02")
}

func (s *Service) ListSchedulesByPeriod(ctx context.Context, userID, cursor string, limit int, period string) (SchedulePage, error) {
	if period != "" && period != "all" && period != "upcoming" && period != "past" {
		return SchedulePage{}, fmt.Errorf("%w: 予定の期間指定が正しくありません", ErrInvalid)
	}
	user, err := s.user(ctx, userID)
	if err != nil {
		return SchedulePage{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return SchedulePage{}, fmt.Errorf("%w: 取得件数は1以上100以下で指定してください", ErrInvalid)
	}
	offset := 0
	if cursor != "" {
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return SchedulePage{}, fmt.Errorf("%w: カーソルが正しくありません", ErrInvalid)
		}
	}
	all, err := s.storage.ListSchedules(ctx)
	if err != nil {
		return SchedulePage{}, err
	}
	visible := make([]domain.Schedule, 0, len(all))
	now := time.Now()
	for _, item := range all {
		if withdrawn, err := s.withdrawal(ctx, "schedule", item.ChannelID, item.ID); err != nil {
			return SchedulePage{}, err
		} else if withdrawn != nil {
			continue
		}
		if s.canRead(ctx, userID, item.ChannelID) != nil {
			continue
		}
		if item.Status == domain.ScheduleDraft && item.CreatedBy != userID && user.Role != domain.RoleAdmin {
			continue
		}
		past := scheduleIsPast(item, now)
		if period == "past" && !past || period == "upcoming" && past {
			continue
		}
		visible = append(visible, effectiveSchedule(item, now))
	}
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].UpdatedAt.Equal(visible[j].UpdatedAt) {
			return visible[i].ID < visible[j].ID
		}
		return visible[i].UpdatedAt.After(visible[j].UpdatedAt)
	})
	if offset > len(visible) {
		offset = len(visible)
	}
	end := offset + limit
	next := ""
	if end < len(visible) {
		next = strconv.Itoa(end)
	} else {
		end = len(visible)
	}
	return SchedulePage{Schedules: visible[offset:end], NextCursor: next}, nil
}

func (s *Service) manageSchedule(ctx context.Context, userID, id string) (*domain.Schedule, error) {
	schedule, user, err := s.schedule(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if schedule.CreatedBy != userID && user.Role != domain.RoleAdmin {
		return nil, ErrForbidden
	}
	if withdrawn, err := s.withdrawal(ctx, "schedule", schedule.ChannelID, id); err != nil {
		return nil, err
	} else if withdrawn != nil {
		return nil, ErrInvalid
	}
	return schedule, nil
}

func checkRevision(schedule *domain.Schedule, revision int64) error {
	if revision != schedule.Revision {
		return fmt.Errorf("%w: 予定調整が更新されています。再読み込みしてください", ErrStale)
	}
	return nil
}

func (s *Service) UpdateSchedule(ctx context.Context, userID, id string, input UpdateSchedule) (*domain.Schedule, error) {
	lock := s.channelLock("schedule:" + id)
	lock.Lock()
	defer lock.Unlock()
	schedule, err := s.manageSchedule(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if schedule.Status == domain.ScheduleFinalized {
		return nil, fmt.Errorf("%w: 確定済みの予定調整は編集できません", ErrInvalid)
	}
	if err := checkRevision(schedule, input.Revision); err != nil {
		return nil, err
	}
	title, description, candidates, err := normalizeSchedule(NewSchedule{Title: input.Title, Description: input.Description, Candidates: input.Candidates})
	if err != nil {
		return nil, err
	}
	responses, err := s.storage.ListScheduleResponses(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(responses) > 0 && !sameCandidates(schedule.Candidates, candidates) {
		return nil, fmt.Errorf("%w: 回答が登録された後は候補を変更できません", ErrInvalid)
	}
	schedule.Title, schedule.Description, schedule.Candidates, schedule.ResponseDeadline = title, description, candidates, input.ResponseDeadline
	schedule.Revision++
	schedule.UpdatedAt = time.Now()
	if err := s.storage.SaveSchedule(ctx, schedule); err != nil {
		return nil, err
	}
	s.notifySchedule(ctx, schedule)
	return schedule, nil
}

func sameCandidates(a, b []domain.ScheduleCandidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Service) PublishSchedule(ctx context.Context, userID, id string, revision int64) (*domain.Schedule, error) {
	lock := s.channelLock("schedule:" + id)
	lock.Lock()
	defer lock.Unlock()
	schedule, err := s.manageSchedule(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := checkRevision(schedule, revision); err != nil {
		return nil, err
	}
	if schedule.Status != domain.ScheduleDraft && !(schedule.Status == domain.ScheduleOpen && schedule.AnnouncementSeq == 0) {
		return nil, fmt.Errorf("%w: 下書きの予定調整だけを公開できます", ErrInvalid)
	}
	if err := s.canPost(ctx, userID, schedule.ChannelID); err != nil {
		return nil, err
	}
	if schedule.ResponseDeadline != nil && !time.Now().Before(*schedule.ResponseDeadline) {
		return nil, fmt.Errorf("%w: 回答期限には未来の日時を指定してください", ErrInvalid)
	}
	if schedule.Status == domain.ScheduleDraft {
		schedule.Status = domain.ScheduleOpen
		schedule.Revision++
		schedule.UpdatedAt = time.Now()
		if err := s.storage.SaveSchedule(ctx, schedule); err != nil {
			return nil, err
		}
	}
	message, err := s.postScheduleMessage(ctx, userID, schedule, "published", "予定調整を公開しました: "+schedule.Title)
	if err != nil {
		s.notifySchedule(ctx, schedule)
		return schedule, fmt.Errorf("publish announcement: %w", err)
	}
	schedule.AnnouncementSeq = message.Seq
	schedule.Revision++
	schedule.UpdatedAt = time.Now()
	if err := s.storage.SaveSchedule(ctx, schedule); err != nil {
		return nil, err
	}
	s.notifySchedule(ctx, schedule)
	return schedule, nil
}

func (s *Service) postScheduleMessage(ctx context.Context, userID string, schedule *domain.Schedule, event, text string) (domain.Message, error) {
	message, err := s.storage.AddMessage(ctx, schedule.ChannelID, domain.Message{UserID: userID, Text: text, ScheduleRef: &domain.ScheduleReference{ID: schedule.ID, Event: event}})
	if err == nil && s.publisher != nil {
		s.publisher.NewMessage(ctx, schedule.ChannelID, message.Seq)
	}
	return message, err
}

func (s *Service) CloseSchedule(ctx context.Context, userID, id string, revision int64) (*domain.Schedule, error) {
	return s.setScheduleStatus(ctx, userID, id, revision, domain.ScheduleClosed, nil)
}

func (s *Service) ReopenSchedule(ctx context.Context, userID, id string, revision int64, deadline *time.Time) (*domain.Schedule, error) {
	if deadline != nil && !time.Now().Before(*deadline) {
		return nil, fmt.Errorf("%w: 回答期限には未来の日時を指定してください", ErrInvalid)
	}
	return s.setScheduleStatus(ctx, userID, id, revision, domain.ScheduleOpen, deadline)
}

func (s *Service) setScheduleStatus(ctx context.Context, userID, id string, revision int64, status domain.ScheduleStatus, deadline *time.Time) (*domain.Schedule, error) {
	lock := s.channelLock("schedule:" + id)
	lock.Lock()
	defer lock.Unlock()
	schedule, err := s.manageSchedule(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := checkRevision(schedule, revision); err != nil {
		return nil, err
	}
	if schedule.Status == domain.ScheduleDraft || schedule.Status == domain.ScheduleFinalized {
		return nil, fmt.Errorf("%w: 予定調整を指定された状態へ変更できません", ErrInvalid)
	}
	schedule.Status = status
	if status == domain.ScheduleOpen {
		schedule.ResponseDeadline = deadline
	}
	schedule.Revision++
	schedule.UpdatedAt = time.Now()
	if err := s.storage.SaveSchedule(ctx, schedule); err != nil {
		return nil, err
	}
	s.notifySchedule(ctx, schedule)
	return schedule, nil
}

func (s *Service) FinalizeSchedule(ctx context.Context, userID, id string, revision int64, candidateID string) (*domain.Schedule, error) {
	lock := s.channelLock("schedule:" + id)
	lock.Lock()
	defer lock.Unlock()
	schedule, err := s.manageSchedule(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := checkRevision(schedule, revision); err != nil {
		return nil, err
	}
	if err := s.canPost(ctx, userID, schedule.ChannelID); err != nil {
		return nil, err
	}
	if schedule.Status == domain.ScheduleDraft || schedule.Status == domain.ScheduleFinalized && !(schedule.FinalCandidateID == candidateID && schedule.FinalAnnouncementSeq == 0) {
		return nil, fmt.Errorf("%w: この予定調整は確定できません", ErrInvalid)
	}
	var chosen *domain.ScheduleCandidate
	for i := range schedule.Candidates {
		if schedule.Candidates[i].ID == candidateID {
			chosen = &schedule.Candidates[i]
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("%w: 指定された候補は存在しません", ErrInvalid)
	}
	if schedule.Status != domain.ScheduleFinalized {
		schedule.Status = domain.ScheduleFinalized
		schedule.FinalCandidateID = candidateID
		schedule.Revision++
		schedule.UpdatedAt = time.Now()
		if err := s.storage.SaveSchedule(ctx, schedule); err != nil {
			return nil, err
		}
	}
	label := chosen.Date
	if chosen.StartTime != "" {
		label += " " + chosen.StartTime
		if chosen.EndTime != "" {
			label += "〜" + chosen.EndTime
		}
	}
	message, err := s.postScheduleMessage(ctx, userID, schedule, "finalized", "予定を確定しました: "+schedule.Title+"（"+label+"）")
	if err != nil {
		s.notifySchedule(ctx, schedule)
		return schedule, fmt.Errorf("final announcement: %w", err)
	}
	schedule.FinalAnnouncementSeq = message.Seq
	schedule.Revision++
	schedule.UpdatedAt = time.Now()
	if err := s.storage.SaveSchedule(ctx, schedule); err != nil {
		return nil, err
	}
	s.notifySchedule(ctx, schedule)
	return schedule, nil
}

func (s *Service) SetScheduleResponse(ctx context.Context, userID, id string, revision int64, choices map[string]domain.ScheduleChoice, comment string) (domain.ScheduleResponse, error) {
	lock := s.channelLock("schedule:" + id)
	lock.Lock()
	defer lock.Unlock()
	schedule, _, err := s.schedule(ctx, userID, id)
	if err != nil {
		return domain.ScheduleResponse{}, err
	}
	if withdrawn, err := s.withdrawal(ctx, "schedule", schedule.ChannelID, id); err != nil {
		return domain.ScheduleResponse{}, err
	} else if withdrawn != nil {
		return domain.ScheduleResponse{}, ErrInvalid
	}
	if err := checkRevision(schedule, revision); err != nil {
		return domain.ScheduleResponse{}, err
	}
	if effectiveSchedule(*schedule, time.Now()).Status != domain.ScheduleOpen {
		return domain.ScheduleResponse{}, fmt.Errorf("%w: この予定調整は回答を受け付けていません", ErrInvalid)
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > MaxScheduleCommentRunes {
		return domain.ScheduleResponse{}, fmt.Errorf("%w: コメントは%d文字以内にしてください", ErrInvalid, MaxScheduleCommentRunes)
	}
	if len(choices) != len(schedule.Candidates) {
		return domain.ScheduleResponse{}, fmt.Errorf("%w: すべての候補に回答してください", ErrInvalid)
	}
	for _, candidate := range schedule.Candidates {
		choice, ok := choices[candidate.ID]
		if !ok || choice != domain.ScheduleYes && choice != domain.ScheduleMaybe && choice != domain.ScheduleNo {
			return domain.ScheduleResponse{}, fmt.Errorf("%w: 候補への回答が正しくありません", ErrInvalid)
		}
	}
	response, err := s.storage.AddScheduleResponse(ctx, id, domain.ScheduleResponse{UserID: userID, Choices: choices, Comment: comment})
	if err != nil {
		return domain.ScheduleResponse{}, err
	}
	s.notifySchedule(ctx, schedule)
	return response, nil
}

func (s *Service) notifySchedule(ctx context.Context, schedule *domain.Schedule) {
	if publisher, ok := s.publisher.(SchedulePublisher); ok {
		publisher.ScheduleChanged(ctx, schedule.ChannelID, schedule.ID, schedule.Revision)
	}
}
