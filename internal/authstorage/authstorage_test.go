package authstorage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/authstorage"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/oidc"
)

func TestAuthStorage_FileStore_NoChatDirs(t *testing.T) {
	tempDir := t.TempDir()
	store, err := authstorage.Open(tempDir, "file")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Verify required dirs exist
	for _, dir := range []string{"users", "groups", "sessions", "clients"} {
		if _, err := os.Stat(filepath.Join(tempDir, dir)); err != nil {
			t.Errorf("expected %s dir to exist, err: %v", dir, err)
		}
	}

	// Verify chat-specific dirs DO NOT exist
	for _, forbidden := range []string{"channels", "schedules", "state", "messages"} {
		if _, err := os.Stat(filepath.Join(tempDir, forbidden)); !errorsIsNotExist(err) {
			t.Errorf("forbidden chat dir %s should not exist, err: %v", forbidden, err)
		}
	}

	testCRUD(t, store)
}

func TestAuthStorage_SQLiteStore(t *testing.T) {
	tempDir := t.TempDir()
	store, err := authstorage.Open(tempDir, "sqlite")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Verify miniauth.db was created
	dbPath := filepath.Join(tempDir, "miniauth.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected miniauth.db to exist: %v", err)
	}

	testCRUD(t, store)
}

func testCRUD(t *testing.T, store authstorage.Storage) {
	ctx := context.Background()

	// 1. User CRUD
	u := &domain.User{
		Version:        1,
		ID:             "u1",
		Name:           "User 1",
		Role:           domain.RoleUser,
		Groups:         []string{"g1"},
		Enabled:        true,
		PasswordHash:   "hash123",
		AuthGeneration: 1,
	}
	if err := store.SaveUser(ctx, u); err != nil {
		t.Fatalf("SaveUser failed: %v", err)
	}
	gotU, err := store.GetUser(ctx, "u1")
	if err != nil || gotU.Name != "User 1" {
		t.Fatalf("GetUser failed: %v, got: %+v", err, gotU)
	}
	users, err := store.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("ListUsers failed: %v, len: %d", err, len(users))
	}

	// 2. Group CRUD
	g := &domain.Group{Version: 1, ID: "g1", Name: "Group 1"}
	if err := store.SaveGroup(ctx, g); err != nil {
		t.Fatalf("SaveGroup failed: %v", err)
	}
	gotG, err := store.GetGroup(ctx, "g1")
	if err != nil || gotG.Name != "Group 1" {
		t.Fatalf("GetGroup failed: %v, got: %+v", err, gotG)
	}

	// 3. Client CRUD
	c := &oidc.Client{
		ID:           "app1",
		Name:         "Application 1",
		Secret:       "secret123",
		RedirectURIs: []string{"http://localhost/cb"},
		LaunchURL:    "http://localhost/",
		Icon:         "chat",
	}
	if err := store.SaveClient(ctx, c); err != nil {
		t.Fatalf("SaveClient failed: %v", err)
	}
	gotC, err := store.GetClient(ctx, "app1")
	if err != nil || gotC.Name != "Application 1" || gotC.Secret != "secret123" {
		t.Fatalf("GetClient failed: %v, got: %+v", err, gotC)
	}
	clients, err := store.ListClients(ctx)
	if err != nil || len(clients) != 1 {
		t.Fatalf("ListClients failed: %v, len: %d", err, len(clients))
	}

	// Update client
	c.Name = "Updated App 1"
	if err := store.SaveClient(ctx, c); err != nil {
		t.Fatalf("SaveClient update failed: %v", err)
	}
	gotC2, _ := store.GetClient(ctx, "app1")
	if gotC2.Name != "Updated App 1" {
		t.Errorf("expected Updated App 1, got %s", gotC2.Name)
	}

	// 4. Session CRUD
	sess := &domain.Session{
		Version:   1,
		TokenHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		UserID:    "u1",
		CSRFToken: "csrf123",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}
	gotS, err := store.GetSession(ctx, sess.TokenHash)
	if err != nil || gotS.UserID != "u1" {
		t.Fatalf("GetSession failed: %v, got: %+v", err, gotS)
	}

	// Cleanup Client
	if err := store.DeleteClient(ctx, "app1"); err != nil {
		t.Fatalf("DeleteClient failed: %v", err)
	}
	if _, err := store.GetClient(ctx, "app1"); !errorsIsNotExist(err) {
		t.Fatalf("expected not exist for deleted client, got %v", err)
	}
}

func errorsIsNotExist(err error) bool {
	return os.IsNotExist(err)
}

