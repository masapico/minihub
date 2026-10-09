package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/domain"
)

const (
	AdminID   = "admin"
	AdminName = "管理者"
)

type InitialAdmin struct{ ID, Name, Password string }

type UserStore interface {
	ListUsers(ctx context.Context) ([]domain.User, error)
	SaveUser(ctx context.Context, user *domain.User) error
}

// EnsureInitialAdmin creates the initial administrator when the user store is
// empty. It never changes an existing installation.
func EnsureInitialAdmin(ctx context.Context, store UserStore, password string) (bool, error) {
	return EnsureInitialAdminConfig(ctx, store, InitialAdmin{ID: AdminID, Name: AdminName, Password: password})
}

func EnsureInitialAdminConfig(ctx context.Context, store UserStore, initial InitialAdmin) (bool, error) {
	users, err := store.ListUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("list users: %w", err)
	}
	if len(users) != 0 {
		return false, nil
	}
	if initial.ID == "" {
		initial.ID = AdminID
	}
	if initial.Name == "" {
		initial.Name = AdminName
	}
	if initial.Password == "" {
		return false, errors.New("no users exist: configure initialAdmin.password or set MINIHUB_ADMIN_PASSWORD (8 characters, at most 72 bytes)")
	}
	if initial.Password == "CHANGE-ME" {
		return false, errors.New("replace the CHANGE-ME initial administrator password before startup")
	}
	if len([]rune(initial.Password)) < 8 || len([]byte(initial.Password)) > 72 {
		return false, errors.New("initial administrator password must be at least 8 characters and at most 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(initial.Password), bcrypt.DefaultCost)
	if err != nil {
		return false, fmt.Errorf("hash initial administrator password: %w", err)
	}
	if err := store.SaveUser(ctx, &domain.User{
		Version:      domain.Version,
		ID:           initial.ID,
		Name:         initial.Name,
		Role:         domain.RoleAdmin,
		Enabled:      true,
		PasswordHash: string(hash),
	}); err != nil {
		return false, fmt.Errorf("save initial administrator: %w", err)
	}
	return true, nil
}
