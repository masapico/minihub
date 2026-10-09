package authstorage

import (
	"context"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/oidc"
)

type Storage interface {
	// Users
	GetUser(ctx context.Context, id string) (*domain.User, error)
	ListUsers(ctx context.Context) ([]domain.User, error)
	SaveUser(ctx context.Context, user *domain.User) error
	DeleteUser(ctx context.Context, id string) error

	// Groups
	GetGroup(ctx context.Context, id string) (*domain.Group, error)
	ListGroups(ctx context.Context) ([]domain.Group, error)
	SaveGroup(ctx context.Context, group *domain.Group) error
	DeleteGroup(ctx context.Context, id string) error

	// Sessions
	GetSession(ctx context.Context, hash string) (*domain.Session, error)
	SaveSession(ctx context.Context, session *domain.Session) error
	DeleteSession(ctx context.Context, hash string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error)

	// Clients (OIDC Applications)
	GetClient(ctx context.Context, id string) (*oidc.Client, error)
	ListClients(ctx context.Context) ([]*oidc.Client, error)
	SaveClient(ctx context.Context, client *oidc.Client) error
	DeleteClient(ctx context.Context, id string) error

	Close() error
}

