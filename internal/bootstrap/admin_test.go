package bootstrap

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func TestEnsureInitialAdminCreatesAdmin(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := EnsureInitialAdmin(context.Background(), store, "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected administrator to be created")
	}
	admin, err := store.GetUser(context.Background(), AdminID)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Name != AdminName || admin.Role != domain.RoleAdmin || !admin.Enabled {
		t.Fatalf("unexpected administrator: %+v", admin)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte("initial-password")); err != nil {
		t.Fatalf("stored password hash does not match: %v", err)
	}
}

func TestEnsureInitialAdminRequiresPasswordForEmptyStore(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := EnsureInitialAdmin(context.Background(), store, "")
	if created || err == nil || !strings.Contains(err.Error(), "MINIHUB_ADMIN_PASSWORD") {
		t.Fatalf("created=%v err=%v", created, err)
	}
}

func TestEnsureInitialAdminDoesNotChangeExistingInstallation(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing := &domain.User{ID: "u0001", Name: "Existing", Role: domain.RoleUser, Enabled: true}
	if err := store.SaveUser(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureInitialAdmin(context.Background(), store, "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("must not create an administrator when users already exist")
	}
	if _, err := store.GetUser(context.Background(), AdminID); err == nil {
		t.Fatal("administrator was unexpectedly created")
	}
}
