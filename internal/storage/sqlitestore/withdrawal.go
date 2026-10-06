package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/masapico/minihub/internal/domain"
)

func validWithdrawal(kind, channelID, targetID string) error {
	if kind != "message" && kind != "poll" && kind != "schedule" {
		return errors.New("invalid withdrawal kind")
	}
	return checkIDs(channelID, targetID)
}

func (s *Store) GetWithdrawal(ctx context.Context, kind, channelID, targetID string) (*domain.Withdrawal, error) {
	if err := validWithdrawal(kind, channelID, targetID); err != nil {
		return nil, err
	}
	value, err := readWithdrawal(ctx, s.read, kind, channelID, targetID)
	if err != nil {
		return nil, err
	}
	if !value.Active() {
		return nil, os.ErrNotExist
	}
	return value, nil
}

type withdrawalQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readWithdrawal(ctx context.Context, q withdrawalQuerier, kind, channelID, targetID string) (*domain.Withdrawal, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, "SELECT data FROM withdrawals WHERE kind=? AND channel_id=? AND target_id=?", kind, channelID, targetID).Scan(&data); err != nil {
		return nil, missing(err)
	}
	var value domain.Withdrawal
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value.Kind != kind || value.ChannelID != channelID || value.TargetID != targetID || !value.Valid() {
		return nil, errors.New("invalid withdrawal record")
	}
	return &value, nil
}

func (s *Store) SaveWithdrawal(ctx context.Context, value domain.Withdrawal) error {
	if err := validWithdrawal(value.Kind, value.ChannelID, value.TargetID); err != nil {
		return err
	}
	if err := checkIDs(value.ActorID); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current, err := readWithdrawal(ctx, tx, value.Kind, value.ChannelID, value.TargetID)
		if errors.Is(err, os.ErrNotExist) {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO withdrawals(kind,channel_id,target_id,data) VALUES(?,?,?,?)", value.Kind, value.ChannelID, value.TargetID, data)
			return err
		}
		if err != nil {
			return err
		}
		if current.Active() {
			return nil
		}
		current.Append("withdraw", value.ActorID, value.At)
		return updateWithdrawal(ctx, tx, *current)
	})
}

func updateWithdrawal(ctx context.Context, tx *sql.Tx, value domain.Withdrawal) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE withdrawals SET data=? WHERE kind=? AND channel_id=? AND target_id=?", data, value.Kind, value.ChannelID, value.TargetID)
	return err
}

func (s *Store) RestoreWithdrawal(ctx context.Context, kind, channelID, targetID, actorID string, at time.Time) error {
	if err := validWithdrawal(kind, channelID, targetID); err != nil {
		return err
	}
	if err := checkIDs(actorID); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		value, err := readWithdrawal(ctx, tx, kind, channelID, targetID)
		if err != nil {
			return err
		}
		if !value.Active() {
			return nil
		}
		value.Append("restore", actorID, at)
		return updateWithdrawal(ctx, tx, *value)
	})
}
