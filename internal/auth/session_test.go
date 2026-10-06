package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
)

func setup(t *testing.T) *auth.Manager {
	t.Helper()
	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), &domain.User{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true, PasswordHash: string(hash)}); err != nil {
		t.Fatal(err)
	}
	return auth.NewManager(store, false)
}

func TestLoginSessionCSRFAndLogout(t *testing.T) {
	manager := setup(t)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	loginRequest.RemoteAddr = "192.0.2.1:1234"
	recorder := httptest.NewRecorder()
	userID, csrf, err := manager.Login(recorder, loginRequest, "alice", "correct-password")
	if err != nil {
		t.Fatal(err)
	}
	if userID != "alice" || csrf == "" {
		t.Fatalf("user=%q csrf=%q", userID, csrf)
	}
	response := recorder.Result()
	if len(response.Cookies()) != 1 {
		t.Fatal("session cookie not returned")
	}
	cookie := response.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %#v", cookie)
	}

	authenticated := httptest.NewRequest(http.MethodPost, "/api/channels", nil)
	authenticated.AddCookie(cookie)
	authenticated.Header.Set("X-CSRF-Token", csrf)
	got, err := manager.Authenticate(authenticated)
	if err != nil || got != "alice" {
		t.Fatalf("Authenticate user=%q error=%v", got, err)
	}
	if !manager.ValidateCSRF(authenticated) {
		t.Fatal("valid CSRF token rejected")
	}
	authenticated.Header.Set("X-CSRF-Token", "wrong")
	if manager.ValidateCSRF(authenticated) {
		t.Fatal("invalid CSRF token accepted")
	}

	logoutRecorder := httptest.NewRecorder()
	if err := manager.Logout(logoutRecorder, authenticated); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authenticate(authenticated); err == nil {
		t.Fatal("logged-out session remains valid")
	}
}

func TestSessionSurvivesManagerRestartWithoutPersistingRawToken(t *testing.T) {
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), &domain.User{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true, PasswordHash: string(passwordHash)}); err != nil {
		t.Fatal(err)
	}

	first := auth.NewManager(store, false)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	loginRequest.RemoteAddr = "192.0.2.3:1234"
	recorder := httptest.NewRecorder()
	_, csrf, err := first.Login(recorder, loginRequest, "alice", "correct-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := recorder.Result().Cookies()[0]

	files, err := filepath.Glob(filepath.Join(root, "sessions", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session files=%v error=%v", files, err)
	}
	persisted, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), cookie.Value) || strings.Contains(filepath.Base(files[0]), cookie.Value) {
		t.Fatal("raw session token was persisted")
	}

	restarted := auth.NewManager(store, false)
	if count, err := store.DeleteExpiredSessions(context.Background(), time.Now()); err != nil || count != 0 {
		t.Fatalf("cleanup removed valid session: count=%d error=%v", count, err)
	}
	authenticated := httptest.NewRequest(http.MethodPost, "/api/channels", nil)
	authenticated.AddCookie(cookie)
	authenticated.Header.Set("X-CSRF-Token", csrf)
	if userID, err := restarted.Authenticate(authenticated); err != nil || userID != "alice" {
		t.Fatalf("Authenticate after restart user=%q error=%v", userID, err)
	}
	if !restarted.ValidateCSRF(authenticated) {
		t.Fatal("persisted CSRF token was not restored")
	}
	if err := restarted.Logout(httptest.NewRecorder(), authenticated); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatalf("session file remains after logout: %v", err)
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	manager := setup(t)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	request.RemoteAddr = "192.0.2.2:1234"
	if _, _, err := manager.Login(httptest.NewRecorder(), request, "alice", "wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestAuthenticationGenerationInvalidatesOldSession(t *testing.T) {
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	user := &domain.User{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true, PasswordHash: string(hash)}
	if err := store.SaveUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	manager := auth.NewManager(store, false)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	recorder := httptest.NewRecorder()
	if _, _, err := manager.Login(recorder, loginRequest, "alice", "correct-password"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	request.AddCookie(recorder.Result().Cookies()[0])
	user.AuthGeneration++
	if err := store.SaveUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authenticate(request); err == nil {
		t.Fatal("old session survived password generation change")
	}
}
