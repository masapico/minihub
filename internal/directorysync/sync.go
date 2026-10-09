package directorysync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

type Syncer struct {
	authURL      string
	serviceToken string
	store        storage.Storage
	client       *http.Client
	logger       *slog.Logger
}

func NewSyncer(authURL, serviceToken string, store storage.Storage, logger *slog.Logger) *Syncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{
		authURL:      strings.TrimSuffix(authURL, "/"),
		serviceToken: serviceToken,
		store:        store,
		client:       &http.Client{Timeout: 10 * time.Second},
		logger:       logger,
	}
}

type directoryResponse struct {
	Users []struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Role    string   `json:"role"`
		Groups  []string `json:"groups"`
		Enabled bool     `json:"enabled"`
	} `json:"users"`
	Groups []domain.Group `json:"groups"`
}

func (s *Syncer) Sync(ctx context.Context) error {
	reqURL := s.authURL + "/api/directory"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	if s.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.serviceToken)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch directory from %s: %w", reqURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("directory API returned status %d", resp.StatusCode)
	}

	var data directoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("decode directory response: %w", err)
	}

	// 1. Sync Groups
	for _, g := range data.Groups {
		existing, err := s.store.GetGroup(ctx, g.ID)
		if err != nil || existing == nil {
			g.Version = 1
			if saveErr := s.store.SaveGroup(ctx, &g); saveErr != nil {
				s.logger.Warn("failed to save synced group", "id", g.ID, "error", saveErr)
			}
		} else {
			existing.Name = g.Name
			if saveErr := s.store.SaveGroup(ctx, existing); saveErr != nil {
				s.logger.Warn("failed to update synced group", "id", g.ID, "error", saveErr)
			}
		}
	}

	// 2. Sync Users
	for _, u := range data.Users {
		role := domain.RoleUser
		if u.Role == "admin" {
			role = domain.RoleAdmin
		}

		existing, err := s.store.GetUser(ctx, u.ID)
		if err != nil || existing == nil {
			newUser := &domain.User{
				Version:        1,
				ID:             u.ID,
				Name:           u.Name,
				Role:           role,
				Groups:         u.Groups,
				Enabled:        u.Enabled,
				AuthGeneration: 1,
			}
			if saveErr := s.store.SaveUser(ctx, newUser); saveErr != nil {
				s.logger.Warn("failed to save synced user", "id", u.ID, "error", saveErr)
			}
		} else {
			existing.Name = u.Name
			existing.Role = role
			existing.Groups = u.Groups
			existing.Enabled = u.Enabled
			if saveErr := s.store.SaveUser(ctx, existing); saveErr != nil {
				s.logger.Warn("failed to update synced user", "id", u.ID, "error", saveErr)
			}
		}
	}

	s.logger.Info("directory sync completed", "users", len(data.Users), "groups", len(data.Groups))
	return nil
}

func (s *Syncer) StartLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	// Initial sync
	if err := s.Sync(ctx); err != nil {
		s.logger.Warn("initial directory sync failed (will retry)", "error", err)
	}

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
					s.logger.Warn("directory sync failed", "error", err)
				}
			}
		}
	}()
}

