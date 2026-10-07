package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/httpapi"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func TestAIAccountsHTTPDirectory(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), &domain.User{ID: "alice", Name: "Alice", Enabled: true, Role: domain.RoleUser}); err != nil {
		t.Fatal(err)
	}
	svc := service.New(store)
	t.Setenv("MINIHUB_AI_DIRECTORY_TEST", "private-token")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	disabled := false
	if err := svc.StartAI(context.Background(), []config.AIAccount{
		{ID: "helper", Name: "社内AI", URL: "http://private-ai-host/secret-route", Token: "direct-private-token"},
		{ID: "legacy", Name: "Legacy AI", URL: "http://private-ai-host/secret-route", TokenEnv: "MINIHUB_AI_DIRECTORY_TEST"},
		{ID: "disabled", Name: "停止AI", URL: "http://private-ai-host/", Enabled: &disabled},
	}, logger); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.CloseAI)
	handler := httpapi.New(svc, httpapi.HeaderAuthenticator{}, logger)
	got := request(t, handler, http.MethodGet, "/api/ai-accounts", "alice", nil)
	if got.Code != 200 || got.Body.String() != "[{\"id\":\"helper\",\"name\":\"社内AI\"},{\"id\":\"legacy\",\"name\":\"Legacy AI\"}]\n" {
		t.Fatalf("directory: %d %s", got.Code, got.Body.String())
	}
	for _, secret := range []string{"private-token", "private-ai-host", "secret-route", "token", "disabled"} {
		if strings.Contains(got.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if r := request(t, handler, http.MethodGet, "/api/ai-accounts", "", nil); r.Code != 401 {
		t.Fatalf("anonymous directory: %d", r.Code)
	}
	if r := request(t, handler, http.MethodGet, "/api/ai-accounts", "missing", nil); r.Code != 401 {
		t.Fatalf("unknown user directory: %d", r.Code)
	}
	if r := request(t, handler, http.MethodPost, "/api/ai-accounts", "alice", nil); r.Code != 405 {
		t.Fatalf("method: %d", r.Code)
	}
}
