package service

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/masapico/minihub/internal/domain"
)

func (s *Service) withdrawal(ctx context.Context, kind, channelID, targetID string) (*domain.Withdrawal, error) {
	w, err := s.storage.GetWithdrawal(ctx, kind, channelID, targetID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return w, err
}

func (s *Service) canWithdraw(ctx context.Context, actorID, channelID, authorID string) error {
	if err := s.canRead(ctx, actorID, channelID); err != nil {
		return err
	}
	if actorID == authorID {
		return nil
	}
	actor, err := s.user(ctx, actorID)
	if err != nil {
		return err
	}
	channel, err := s.channel(ctx, channelID)
	if err != nil {
		return err
	}
	if actor.Role == domain.RoleAdmin || isChannelManager(channel, actorID) && channelMember(actor, channel) {
		return nil
	}
	return ErrForbidden
}

func (s *Service) saveWithdrawal(ctx context.Context, kind, channelID, targetID, actorID string) (*domain.Withdrawal, error) {
	w, err := s.withdrawal(ctx, kind, channelID, targetID)
	if err != nil || w != nil {
		return w, err
	}
	w = &domain.Withdrawal{Version: domain.Version, Kind: kind, ChannelID: channelID, TargetID: targetID, ActorID: actorID, At: time.Now()}
	if err := s.storage.SaveWithdrawal(ctx, *w); err != nil {
		return nil, err
	}
	return s.withdrawal(ctx, kind, channelID, targetID)
}

func (s *Service) messageWithdrawal(ctx context.Context, channelID string, m domain.Message) (*domain.Withdrawal, error) {
	w, err := s.withdrawal(ctx, "message", channelID, strconv.FormatInt(m.Seq, 10))
	if err != nil || w != nil {
		return w, err
	}
	if m.PollRef != nil {
		return s.withdrawal(ctx, "poll", channelID, m.PollRef.ID)
	}
	if m.ScheduleRef != nil {
		return s.withdrawal(ctx, "schedule", channelID, m.ScheduleRef.ID)
	}
	return nil, nil
}

func (s *Service) publicMessage(ctx context.Context, channelID string, m domain.Message) (domain.Message, error) {
	w, err := s.messageWithdrawal(ctx, channelID, m)
	if err != nil || w == nil {
		return m, err
	}
	m.Text = ""
	m.MentionUserIDs = nil
	m.WithdrawnAt = &w.At
	m.WithdrawnBy = w.ActorID
	m.WithdrawnKind = w.Kind
	return m, nil
}

func (s *Service) WithdrawMessage(ctx context.Context, actorID, channelID string, seq int64) (domain.Message, error) {
	if seq < 1 {
		return domain.Message{}, ErrInvalid
	}
	lock := s.channelLock("withdraw:message:" + channelID + ":" + strconv.FormatInt(seq, 10))
	lock.Lock()
	defer lock.Unlock()
	if err := s.canRead(ctx, actorID, channelID); err != nil {
		return domain.Message{}, err
	}
	m, err := s.storage.GetMessage(ctx, channelID, seq)
	if errors.Is(err, os.ErrNotExist) {
		return domain.Message{}, ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	if m.PollRef != nil || m.ScheduleRef != nil {
		return domain.Message{}, ErrInvalid
	}
	if err := s.canWithdraw(ctx, actorID, channelID, m.UserID); err != nil {
		return domain.Message{}, err
	}
	if _, err := s.saveWithdrawal(ctx, "message", channelID, strconv.FormatInt(seq, 10), actorID); err != nil {
		return domain.Message{}, err
	}
	s.notifyMessageChanged(ctx, channelID, seq)
	return s.publicMessage(ctx, channelID, m)
}

func (s *Service) RestoreMessage(ctx context.Context, actorID, channelID string, seq int64) (domain.Message, error) {
	if seq < 1 {
		return domain.Message{}, ErrInvalid
	}
	id := strconv.FormatInt(seq, 10)
	lock := s.channelLock("withdraw:message:" + channelID + ":" + id)
	lock.Lock()
	defer lock.Unlock()
	if err := s.canRead(ctx, actorID, channelID); err != nil {
		return domain.Message{}, err
	}
	m, err := s.storage.GetMessage(ctx, channelID, seq)
	if errors.Is(err, os.ErrNotExist) {
		return domain.Message{}, ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	if m.PollRef != nil || m.ScheduleRef != nil {
		return domain.Message{}, ErrInvalid
	}
	if err := s.canWithdraw(ctx, actorID, channelID, m.UserID); err != nil {
		return domain.Message{}, err
	}
	if err := s.storage.RestoreWithdrawal(ctx, "message", channelID, id, actorID, time.Now()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Message{}, ErrInvalid
		}
		return domain.Message{}, err
	}
	s.notifyMessageChanged(ctx, channelID, seq)
	return s.publicMessage(ctx, channelID, m)
}

func (s *Service) WithdrawPoll(ctx context.Context, actorID, id string) (PollDetail, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	p, err := s.poll(ctx, actorID, id)
	if err != nil {
		return PollDetail{}, err
	}
	if err := s.canWithdraw(ctx, actorID, p.ChannelID, p.CreatedBy); err != nil {
		return PollDetail{}, err
	}
	if _, err := s.saveWithdrawal(ctx, "poll", p.ChannelID, id, actorID); err != nil {
		return PollDetail{}, err
	}
	s.notifyPoll(ctx, p)
	if p.AnnouncementSeq > 0 {
		s.notifyMessageChanged(ctx, p.ChannelID, p.AnnouncementSeq)
	}
	return s.GetPoll(ctx, actorID, id)
}

func (s *Service) RestorePoll(ctx context.Context, actorID, id string) (PollDetail, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	p, err := s.poll(ctx, actorID, id)
	if err != nil {
		return PollDetail{}, err
	}
	if err := s.canWithdraw(ctx, actorID, p.ChannelID, p.CreatedBy); err != nil {
		return PollDetail{}, err
	}
	if err := s.storage.RestoreWithdrawal(ctx, "poll", p.ChannelID, id, actorID, time.Now()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PollDetail{}, ErrInvalid
		}
		return PollDetail{}, err
	}
	s.notifyPoll(ctx, p)
	if p.AnnouncementSeq > 0 {
		s.notifyMessageChanged(ctx, p.ChannelID, p.AnnouncementSeq)
	}
	return s.GetPoll(ctx, actorID, id)
}

func (s *Service) WithdrawSchedule(ctx context.Context, actorID, id string) (ScheduleDetail, error) {
	lock := s.channelLock("schedule:" + id)
	lock.Lock()
	defer lock.Unlock()
	p, _, err := s.schedule(ctx, actorID, id)
	if err != nil {
		return ScheduleDetail{}, err
	}
	if p.Status == domain.ScheduleDraft {
		return ScheduleDetail{}, ErrInvalid
	}
	if err := s.canWithdraw(ctx, actorID, p.ChannelID, p.CreatedBy); err != nil {
		return ScheduleDetail{}, err
	}
	if _, err := s.saveWithdrawal(ctx, "schedule", p.ChannelID, id, actorID); err != nil {
		return ScheduleDetail{}, err
	}
	s.notifySchedule(ctx, p)
	for _, seq := range []int64{p.AnnouncementSeq, p.FinalAnnouncementSeq} {
		if seq > 0 {
			s.notifyMessageChanged(ctx, p.ChannelID, seq)
		}
	}
	return s.GetSchedule(ctx, actorID, id)
}

func (s *Service) RestoreSchedule(ctx context.Context, actorID, id string) (ScheduleDetail, error) {
	lock := s.channelLock("schedule:" + id)
	lock.Lock()
	defer lock.Unlock()
	p, _, err := s.schedule(ctx, actorID, id)
	if err != nil {
		return ScheduleDetail{}, err
	}
	if err := s.canWithdraw(ctx, actorID, p.ChannelID, p.CreatedBy); err != nil {
		return ScheduleDetail{}, err
	}
	if err := s.storage.RestoreWithdrawal(ctx, "schedule", p.ChannelID, id, actorID, time.Now()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ScheduleDetail{}, ErrInvalid
		}
		return ScheduleDetail{}, err
	}
	s.notifySchedule(ctx, p)
	for _, seq := range []int64{p.AnnouncementSeq, p.FinalAnnouncementSeq} {
		if seq > 0 {
			s.notifyMessageChanged(ctx, p.ChannelID, seq)
		}
	}
	return s.GetSchedule(ctx, actorID, id)
}

type messageChangePublisher interface {
	MessageChanged(context.Context, string, int64)
}

func (s *Service) notifyMessageChanged(ctx context.Context, channelID string, seq int64) {
	if p, ok := s.publisher.(messageChangePublisher); ok {
		p.MessageChanged(ctx, channelID, seq)
	}
}
