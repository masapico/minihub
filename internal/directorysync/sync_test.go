package directorysync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
)

func TestSyncer_Sync(t *testing.T) {
	// Fake miniauth Directory API server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/directory" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resp := map[string]any{
			"users": []map[string]any{
				{"id": "u001", "name": "Synced User 1", "role": "admin", "groups": []string{"dev"}, "enabled": true},
				{"id": "u002", "name": "Synced User 2", "role": "user", "groups": []string{"sales"}, "enabled": false},
			},
			"groups": []map[string]any{
				{"id": "dev", "name": "開発チーム"},
				{"id": "sales", "name": "営業チーム"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	tempDir := t.TempDir()
	store, err := filestore.New(tempDir)
	if err != nil {
		t.Fatalf("filestore.New failed: %v", err)
	}

	syncer := NewSyncer(ts.URL, "secret-token", store, nil)
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Verify users
	u1, err := store.GetUser(context.Background(), "u001")
	if err != nil || u1.Name != "Synced User 1" || u1.Role != domain.RoleAdmin || len(u1.Groups) != 1 || u1.Groups[0] != "dev" {
		t.Fatalf("u1 verification failed: %+v, err: %v", u1, err)
	}
	u2, err := store.GetUser(context.Background(), "u002")
	if err != nil || u2.Name != "Synced User 2" || u2.Enabled != false {
		t.Fatalf("u2 verification failed: %+v, err: %v", u2, err)
	}

	// Verify groups
	gDev, err := store.GetGroup(context.Background(), "dev")
	if err != nil || gDev.Name != "開発チーム" {
		t.Fatalf("gDev verification failed: %+v, err: %v", gDev, err)
	}
}

