package filestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/masapico/minihub/internal/domain"
)

func (s *FileStorage) withdrawalPath(kind, channelID, targetID string) (string, error) {
	if kind != "message" && kind != "poll" && kind != "schedule" {
		return "", errors.New("invalid withdrawal kind")
	}
	for _, id := range []string{channelID, targetID} {
		if err := checkID("withdrawal", id); err != nil {
			return "", err
		}
	}
	return filepath.Join(s.root, "withdrawals", channelID, kind, targetID+".json"), nil
}

func (s *FileStorage) GetWithdrawal(ctx context.Context, kind, channelID, targetID string) (*domain.Withdrawal, error) {
	path, err := s.withdrawalPath(kind, channelID, targetID)
	if err != nil {
		return nil, err
	}
	value, err := s.readWithdrawal(ctx, path, kind, channelID, targetID)
	if err != nil {
		return nil, err
	}
	if !value.Active() {
		return nil, os.ErrNotExist
	}
	return value, nil
}

func (s *FileStorage) readWithdrawal(ctx context.Context, path, kind, channelID, targetID string) (*domain.Withdrawal, error) {
	var value domain.Withdrawal
	if err := readJSON(ctx, path, &value); err != nil {
		return nil, err
	}
	if value.Kind != kind || value.ChannelID != channelID || value.TargetID != targetID || !value.Valid() {
		return nil, errors.New("invalid withdrawal record")
	}
	return &value, nil
}

func (s *FileStorage) SaveWithdrawal(ctx context.Context, value domain.Withdrawal) error {
	path, err := s.withdrawalPath(value.Kind, value.ChannelID, value.TargetID)
	if err != nil {
		return err
	}
	if err := checkID("actor", value.ActorID); err != nil {
		return err
	}
	lock := s.keyedLock(s.scheduleWriters, "withdrawal:"+value.Kind+":"+value.ChannelID+":"+value.TargetID)
	lock.Lock()
	defer lock.Unlock()
	current, err := s.readWithdrawal(ctx, path, value.Kind, value.ChannelID, value.TargetID)
	if err == nil {
		if current.Active() {
			return nil
		}
		current.Append("withdraw", value.ActorID, value.At)
		return atomicJSON(ctx, path, current)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicJSON(ctx, path, value)
}

func (s *FileStorage) RestoreWithdrawal(ctx context.Context, kind, channelID, targetID, actorID string, at time.Time) error {
	path, err := s.withdrawalPath(kind, channelID, targetID)
	if err != nil {
		return err
	}
	if err := checkID("actor", actorID); err != nil {
		return err
	}
	lock := s.keyedLock(s.scheduleWriters, "withdrawal:"+kind+":"+channelID+":"+targetID)
	lock.Lock()
	defer lock.Unlock()
	value, err := s.readWithdrawal(ctx, path, kind, channelID, targetID)
	if err != nil {
		return err
	}
	if !value.Active() {
		return nil
	}
	value.Append("restore", actorID, at)
	return atomicJSON(ctx, path, value)
}
