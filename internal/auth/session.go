package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrRateLimited        = errors.New("too many login attempts")
)

const CookieName = "minihub_session"

type session struct {
	UserID         string
	CSRFToken      string
	ExpiresAt      time.Time
	AuthGeneration uint64
}

type failures struct {
	Count int
	Since time.Time
}

type Manager struct {
	storage    storage.Storage
	secure     bool
	cookiePath string
	ttl        time.Duration
	mu         sync.Mutex
	sessions   map[string]session
	failures   map[string]failures
	dummyHash  []byte
}

func NewManager(store storage.Storage, secureCookie bool) *Manager {
	return NewManagerWithTTL(store, secureCookie, 12*time.Hour)
}

func NewManagerWithTTL(store storage.Storage, secureCookie bool, ttl time.Duration) *Manager {
	return NewManagerWithCookiePath(store, secureCookie, ttl, "/")
}

// NewManagerWithCookiePath scopes login and logout cookies to the deployment.
func NewManagerWithCookiePath(store storage.Storage, secureCookie bool, ttl time.Duration, cookiePath string) *Manager {
	dummyHash, _ := bcrypt.GenerateFromPassword([]byte("invalid-password-placeholder"), bcrypt.DefaultCost)
	return &Manager{storage: store, secure: secureCookie, cookiePath: cookiePath, ttl: ttl, sessions: make(map[string]session), failures: make(map[string]failures), dummyHash: dummyHash}
}

func (m *Manager) Login(w http.ResponseWriter, r *http.Request, userID, password string) (string, string, error) {
	key := remoteHost(r.RemoteAddr) + "|" + userID
	if m.rateLimited(key) {
		return "", "", ErrRateLimited
	}
	user, err := m.storage.GetUser(r.Context(), userID)
	passwordHash := m.dummyHash
	validUser := err == nil && user.Enabled && user.PasswordHash != ""
	if validUser {
		passwordHash = []byte(user.PasswordHash)
	}
	passwordErr := bcrypt.CompareHashAndPassword(passwordHash, []byte(password))
	if !validUser || passwordErr != nil {
		m.recordFailure(key)
		return "", "", ErrInvalidCredentials
	}
	return m.EstablishSession(w, r, user)
}

func (m *Manager) EstablishSession(w http.ResponseWriter, r *http.Request, user *domain.User) (string, string, error) {
	token, err := randomToken()
	if err != nil {
		return "", "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	m.mu.Lock()
	key := remoteHost(r.RemoteAddr) + "|" + user.ID
	delete(m.failures, key)
	hash := tokenHash(token)
	current := session{UserID: user.ID, CSRFToken: csrf, ExpiresAt: now.Add(m.ttl), AuthGeneration: user.AuthGeneration}
	if err := m.storage.SaveSession(r.Context(), &domain.Session{TokenHash: hash, UserID: current.UserID, CSRFToken: current.CSRFToken, ExpiresAt: current.ExpiresAt, AuthGeneration: current.AuthGeneration}); err != nil {
		m.mu.Unlock()
		return "", "", err
	}
	m.sessions[hash] = current
	m.removeExpiredLocked(now)
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: m.cookiePath, MaxAge: int(m.ttl.Seconds()), Expires: now.Add(m.ttl), HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteStrictMode})
	return user.ID, csrf, nil
}

func (m *Manager) Authenticate(r *http.Request) (string, error) {
	_, current, err := m.lookup(r)
	if err != nil {
		return "", err
	}
	return current.UserID, nil
}

// RefreshCurrentSession keeps the password-changing browser signed in while
// every session carrying the previous authentication generation becomes invalid.
func (m *Manager) RefreshCurrentSession(r *http.Request, generation uint64) error {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return ErrInvalidCredentials
	}
	hash := tokenHash(cookie.Value)
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.sessions[hash]
	if !ok {
		persisted, loadErr := m.storage.GetSession(r.Context(), hash)
		if loadErr != nil {
			return ErrInvalidCredentials
		}
		current = session{UserID: persisted.UserID, CSRFToken: persisted.CSRFToken, ExpiresAt: persisted.ExpiresAt, AuthGeneration: persisted.AuthGeneration}
	}
	current.AuthGeneration = generation
	if err := m.storage.SaveSession(r.Context(), &domain.Session{Version: domain.Version, TokenHash: hash, UserID: current.UserID, CSRFToken: current.CSRFToken, ExpiresAt: current.ExpiresAt, AuthGeneration: generation}); err != nil {
		return err
	}
	m.sessions[hash] = current
	return nil
}

func (m *Manager) CSRFToken(r *http.Request) (string, bool) {
	_, current, err := m.lookup(r)
	return current.CSRFToken, err == nil
}

func (m *Manager) ValidateCSRF(r *http.Request) bool {
	_, current, err := m.lookup(r)
	provided := r.Header.Get("X-CSRF-Token")
	return err == nil && current.CSRFToken != "" && len(provided) == len(current.CSRFToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(current.CSRFToken)) == 1
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) error {
	var deleteErr error
	if cookie, err := r.Cookie(CookieName); err == nil {
		hash := tokenHash(cookie.Value)
		m.mu.Lock()
		delete(m.sessions, hash)
		deleteErr = m.storage.DeleteSession(r.Context(), hash)
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: m.cookiePath, MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteStrictMode})
	return deleteErr
}

func (m *Manager) lookup(r *http.Request) (string, session, error) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return "", session{}, ErrInvalidCredentials
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	hash := tokenHash(cookie.Value)
	current, ok := m.sessions[hash]
	if !ok {
		persisted, loadErr := m.storage.GetSession(r.Context(), hash)
		if loadErr != nil {
			if errors.Is(loadErr, os.ErrNotExist) {
				return "", session{}, ErrInvalidCredentials
			}
			return "", session{}, loadErr
		}
		current = session{UserID: persisted.UserID, CSRFToken: persisted.CSRFToken, ExpiresAt: persisted.ExpiresAt, AuthGeneration: persisted.AuthGeneration}
		m.sessions[hash] = current
	}
	if !time.Now().Before(current.ExpiresAt) {
		delete(m.sessions, hash)
		if err := m.storage.DeleteSession(r.Context(), hash); err != nil {
			return "", session{}, err
		}
		return "", session{}, ErrInvalidCredentials
	}
	user, userErr := m.storage.GetUser(r.Context(), current.UserID)
	if userErr != nil || !user.Enabled || user.AuthGeneration != current.AuthGeneration {
		delete(m.sessions, hash)
		_ = m.storage.DeleteSession(r.Context(), hash)
		return "", session{}, ErrInvalidCredentials
	}
	return hash, current, nil
}

func (m *Manager) rateLimited(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.failures[key]
	return f.Count >= 5 && time.Since(f.Since) < time.Minute
}

func (m *Manager) recordFailure(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.failures[key]
	if f.Since.IsZero() || time.Since(f.Since) >= time.Minute {
		f = failures{Since: time.Now()}
	}
	f.Count++
	m.failures[key] = f
}

func (m *Manager) removeExpiredLocked(now time.Time) {
	for token, current := range m.sessions {
		if now.After(current.ExpiresAt) {
			delete(m.sessions, token)
		}
	}
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func remoteHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return address
}

// VerifyEnabled is useful for long-lived connections after session lookup.
func (m *Manager) VerifyEnabled(ctx context.Context, userID string) bool {
	user, err := m.storage.GetUser(ctx, userID)
	return err == nil && user.Enabled
}
