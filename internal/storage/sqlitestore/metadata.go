package sqlitestore

import (
	"context"
	"errors"
	"github.com/masapico/minihub/internal/domain"
)

func (s *Store) GetUser(ctx context.Context, id string) (*domain.User, error) {
	return get[domain.User](ctx, s, "users", id)
}
func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	return list[domain.User](ctx, s, "users")
}
func (s *Store) SaveUser(ctx context.Context, v *domain.User) error {
	if v == nil {
		return errors.New("User is required")
	}
	if v.Version == 0 {
		v.Version = domain.Version
	}
	return s.save(ctx, "users", v.ID, v)
}
func (s *Store) GetGroup(ctx context.Context, id string) (*domain.Group, error) {
	return get[domain.Group](ctx, s, "groups", id)
}
func (s *Store) ListGroups(ctx context.Context) ([]domain.Group, error) {
	return list[domain.Group](ctx, s, "groups")
}
func (s *Store) SaveGroup(ctx context.Context, v *domain.Group) error {
	if v == nil {
		return errors.New("Group is required")
	}
	if v.Version == 0 {
		v.Version = domain.Version
	}
	return s.save(ctx, "groups", v.ID, v)
}
func (s *Store) DeleteGroup(ctx context.Context, id string) error { return s.remove(ctx, "groups", id) }
func (s *Store) GetChannel(ctx context.Context, id string) (*domain.Channel, error) {
	return get[domain.Channel](ctx, s, "channels", id)
}
func (s *Store) ListChannels(ctx context.Context) ([]domain.Channel, error) {
	return list[domain.Channel](ctx, s, "channels")
}
func (s *Store) SaveChannel(ctx context.Context, v *domain.Channel) error {
	if v == nil {
		return errors.New("Channel is required")
	}
	if v.Version == 0 {
		v.Version = domain.Version
	}
	return s.save(ctx, "channels", v.ID, v)
}
func (s *Store) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	return get[domain.Session](ctx, s, "sessions", id)
}
func (s *Store) SaveSession(ctx context.Context, v *domain.Session) error {
	if v == nil {
		return errors.New("Session is required")
	}
	if v.Version == 0 {
		v.Version = domain.Version
	}
	return s.save(ctx, "sessions", v.TokenHash, v)
}
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	return s.remove(ctx, "sessions", id)
}
func (s *Store) GetSchedule(ctx context.Context, id string) (*domain.Schedule, error) {
	return get[domain.Schedule](ctx, s, "schedules", id)
}
func (s *Store) ListSchedules(ctx context.Context) ([]domain.Schedule, error) {
	return list[domain.Schedule](ctx, s, "schedules")
}
func (s *Store) SaveSchedule(ctx context.Context, v *domain.Schedule) error {
	if v == nil {
		return errors.New("Schedule is required")
	}
	if v.Version == 0 {
		v.Version = domain.Version
	}
	if err := checkIDs(v.ChannelID); err != nil {
		return err
	}
	return s.save(ctx, "schedules", v.ID, v)
}
