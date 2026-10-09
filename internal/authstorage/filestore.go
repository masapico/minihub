package authstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/oidc"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type FileStorage struct {
	root string
	mu   sync.RWMutex
}

func NewFileStorage(root string) (*FileStorage, error) {
	if root == "" {
		return nil, errors.New("data directory is required")
	}
	// Create ONLY users, groups, sessions, clients
	for _, dir := range []string{"users", "groups", "sessions", "clients"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, fmt.Errorf("create %s directory: %w", dir, err)
		}
	}
	return &FileStorage{root: root}, nil
}

func (s *FileStorage) Close() error {
	return nil
}

func checkID(kind, id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid %s id %q", kind, id)
	}
	return nil
}

func writeJSONAtomic(path string, v any) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Users
func (s *FileStorage) GetUser(ctx context.Context, id string) (*domain.User, error) {
	if err := checkID("user", id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.root, "users", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var u domain.User
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *FileStorage) ListUsers(ctx context.Context) ([]domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "users"))
	if err != nil {
		return nil, err
	}
	users := make([]domain.User, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !regexJSON.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, "users", e.Name()))
		if err != nil {
			continue
		}
		var u domain.User
		if err := json.Unmarshal(data, &u); err == nil {
			users = append(users, u)
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	return users, nil
}

func (s *FileStorage) SaveUser(ctx context.Context, user *domain.User) error {
	if err := checkID("user", user.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "users", user.ID+".json")
	return writeJSONAtomic(path, user)
}

func (s *FileStorage) DeleteUser(ctx context.Context, id string) error {
	if err := checkID("user", id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "users", id+".json")
	return os.Remove(path)
}

// Groups
func (s *FileStorage) GetGroup(ctx context.Context, id string) (*domain.Group, error) {
	if err := checkID("group", id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.root, "groups", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g domain.Group
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *FileStorage) ListGroups(ctx context.Context) ([]domain.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "groups"))
	if err != nil {
		return nil, err
	}
	groups := make([]domain.Group, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !regexJSON.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, "groups", e.Name()))
		if err != nil {
			continue
		}
		var g domain.Group
		if err := json.Unmarshal(data, &g); err == nil {
			groups = append(groups, g)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups, nil
}

func (s *FileStorage) SaveGroup(ctx context.Context, group *domain.Group) error {
	if err := checkID("group", group.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "groups", group.ID+".json")
	return writeJSONAtomic(path, group)
}

func (s *FileStorage) DeleteGroup(ctx context.Context, id string) error {
	if err := checkID("group", id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "groups", id+".json")
	return os.Remove(path)
}

// Sessions
var regexHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var regexJSON = regexp.MustCompile(`\.json$`)

func (s *FileStorage) GetSession(ctx context.Context, hash string) (*domain.Session, error) {
	if !regexHex64.MatchString(hash) {
		return nil, errors.New("invalid session hash")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.root, "sessions", hash+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sess domain.Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *FileStorage) SaveSession(ctx context.Context, session *domain.Session) error {
	if !regexHex64.MatchString(session.TokenHash) {
		return errors.New("invalid session hash")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "sessions", session.TokenHash+".json")
	return writeJSONAtomic(path, session)
}

func (s *FileStorage) DeleteSession(ctx context.Context, hash string) error {
	if !regexHex64.MatchString(hash) {
		return errors.New("invalid session hash")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "sessions", hash+".json")
	return os.Remove(path)
}

func (s *FileStorage) DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "sessions"))
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, e := range entries {
		if e.IsDir() || !regexJSON.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(s.root, "sessions", e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sess domain.Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue
		}
		if now.After(sess.ExpiresAt) {
			if err := os.Remove(path); err == nil {
				deleted++
			}
		}
	}
	return deleted, nil
}

// Clients (OIDC Applications)
func (s *FileStorage) GetClient(ctx context.Context, id string) (*oidc.Client, error) {
	if err := checkID("client", id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.root, "clients", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c oidc.Client
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *FileStorage) ListClients(ctx context.Context) ([]*oidc.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "clients"))
	if err != nil {
		return nil, err
	}
	clients := make([]*oidc.Client, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !regexJSON.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, "clients", e.Name()))
		if err != nil {
			continue
		}
		var c oidc.Client
		if err := json.Unmarshal(data, &c); err == nil {
			clients = append(clients, &c)
		}
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ID < clients[j].ID })
	return clients, nil
}

func (s *FileStorage) SaveClient(ctx context.Context, client *oidc.Client) error {
	if err := checkID("client", client.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "clients", client.ID+".json")
	return writeJSONAtomic(path, client)
}

func (s *FileStorage) DeleteClient(ctx context.Context, id string) error {
	if err := checkID("client", id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "clients", id+".json")
	return os.Remove(path)
}

