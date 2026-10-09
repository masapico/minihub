package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/authserver"
	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/directorysync"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/httpapi"
	"github.com/masapico/minihub/internal/oidc"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage/filestore"
)

func TestSSO_EndToEndIntegration(t *testing.T) {
	// 1. Setup miniauth Storage & Server
	authDataDir := t.TempDir()
	authStore, err := filestore.New(authDataDir)
	if err != nil {
		t.Fatalf("authStore init failed: %v", err)
	}

	// Register user in miniauth
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	authStore.SaveUser(context.Background(), &domain.User{
		Version:        1,
		ID:             "u100",
		Name:           "SSO 太郎",
		Role:           domain.RoleAdmin,
		Groups:         []string{"dev", "infra"},
		Enabled:        true,
		PasswordHash:   string(hash),
		AuthGeneration: 1,
	})
	authStore.SaveGroup(context.Background(), &domain.Group{Version: 1, ID: "dev", Name: "開発部"})
	authStore.SaveGroup(context.Background(), &domain.Group{Version: 1, ID: "infra", Name: "インフラ部"})

	authSessions := auth.NewManager(authStore, false)

	authCfg := authserver.Defaults()
	authCfg.OIDC.ServiceToken = "service-secret"
	authCfg.Clients = []oidc.Client{
		{
			ID:           "minihub-chat",
			Name:         "minihub Chat",
			Secret:       "chat-client-secret",
			RedirectURIs: []string{"http://127.0.0.1:8080/api/auth/sso/callback"},
		},
	}

	var routes http.Handler
	miniauthTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.ServeHTTP(w, r)
	}))
	defer miniauthTS.Close()

	authCfg.OIDC.Issuer = miniauthTS.URL
	srv := authserver.NewServer(authCfg, authStore, authSessions, nil)
	routes = srv.Routes()
	authServerURL := miniauthTS.URL

	// 2. Setup minihub Storage & Server
	hubDataDir := t.TempDir()
	hubStore, err := filestore.New(hubDataDir)
	if err != nil {
		t.Fatalf("hubStore init failed: %v", err)
	}

	hubSessions := auth.NewManager(hubStore, false)
	hubSvc := service.New(hubStore)

	miniauthClientCfg := config.MiniAuthConfig{
		URL:          authServerURL,
		ClientID:     "minihub-chat",
		ClientSecret: "chat-client-secret",
		ServiceToken: "service-secret",
	}

	ssoHandler := httpapi.NewSSOHandler(miniauthClientCfg, "", hubStore, hubSessions)
	hubAPIHandler := httpapi.New(hubSvc, hubSessions, nil)
	hubAPIHandler.(*httpapi.API).SetSSOHandler(ssoHandler)

	hubMux := http.NewServeMux()
	hubMux.Handle("/api/", hubAPIHandler)
	hubTS := httptest.NewServer(hubMux)
	defer hubTS.Close()

	// 3. Test Directory Sync from miniauth to minihub
	syncer := directorysync.NewSyncer(authServerURL, "service-secret", hubStore, nil)
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("directory sync failed: %v", err)
	}

	// Verify that user u100 and groups dev, infra are present in hubStore
	syncedUser, err := hubStore.GetUser(context.Background(), "u100")
	if err != nil || syncedUser.Name != "SSO 太郎" || syncedUser.Role != domain.RoleAdmin {
		t.Fatalf("synced user verification failed: %+v, err: %v", syncedUser, err)
	}
	syncedGroup, err := hubStore.GetGroup(context.Background(), "dev")
	if err != nil || syncedGroup.Name != "開発部" {
		t.Fatalf("synced group verification failed: %+v, err: %v", syncedGroup, err)
	}

	// 4. Test SSO Login Flow
	// Step 4.1: Login to miniauth to simulate an authenticated browser
	loginReq := httptest.NewRequest(http.MethodPost, authServerURL+"/api/auth/login", nil)
	loginRec := httptest.NewRecorder()
	authSessions.Login(loginRec, loginReq, "u100", "password123")
	if len(loginRec.Result().Cookies()) == 0 {
		t.Fatal("failed to get miniauth login cookie")
	}
	miniauthCookie := loginRec.Result().Cookies()[0]

	// Step 4.2: User initiates SSO login from minihub: GET /api/auth/sso/login?next=/channels
	ssoLoginReq, _ := http.NewRequest(http.MethodGet, hubTS.URL+"/api/auth/sso/login?next=/channels", nil)
	ssoLoginReq.Host = "127.0.0.1:8080"
	ssoLoginRec := httptest.NewRecorder()
	hubMux.ServeHTTP(ssoLoginRec, ssoLoginReq)

	if ssoLoginRec.Code != http.StatusFound {
		t.Fatalf("sso login returned %d, expected 302", ssoLoginRec.Code)
	}
	authRedirectURL := ssoLoginRec.Header().Get("Location")

	// Step 4.3: User is redirected to miniauth /oauth/authorize (with miniauth session cookie)
	authReq, _ := http.NewRequest(http.MethodGet, authRedirectURL, nil)
	authReq.AddCookie(miniauthCookie)
	authRec := httptest.NewRecorder()
	miniauthTS.Config.Handler.ServeHTTP(authRec, authReq)

	if authRec.Code != http.StatusFound {
		t.Fatalf("miniauth authorize returned %d, expected 302", authRec.Code)
	}
	callbackRedirectURL := authRec.Header().Get("Location")
	parsedCallbackURL, err := url.Parse(callbackRedirectURL)
	if err != nil {
		t.Fatalf("invalid callback URL: %v", err)
	}

	// Step 4.4: User is redirected back to minihub /api/auth/sso/callback?code=...&state=...
	callbackReq, _ := http.NewRequest(http.MethodGet, hubTS.URL+parsedCallbackURL.RequestURI(), nil)
	callbackReq.Host = "127.0.0.1:8080"
	callbackRec := httptest.NewRecorder()
	hubMux.ServeHTTP(callbackRec, callbackReq)

	if callbackRec.Code != http.StatusFound {
		t.Fatalf("sso callback returned %d: %s", callbackRec.Code, callbackRec.Body.String())
	}
	if callbackRec.Header().Get("Location") != "/channels" {
		t.Errorf("expected redirect to /channels, got %s", callbackRec.Header().Get("Location"))
	}
	if len(callbackRec.Result().Cookies()) == 0 {
		t.Fatal("expected minihub session cookie, got none")
	}
	minihubSessionCookie := callbackRec.Result().Cookies()[0]

	// Step 4.5: User accesses minihub /api/me using the minihub session cookie
	meReq, _ := http.NewRequest(http.MethodGet, hubTS.URL+"/api/me", nil)
	meReq.AddCookie(minihubSessionCookie)
	meRec := httptest.NewRecorder()
	hubMux.ServeHTTP(meRec, meReq)

	if meRec.Code != http.StatusOK {
		t.Fatalf("/api/me returned %d: %s", meRec.Code, meRec.Body.String())
	}
	var meData struct {
		ID   string      `json:"id"`
		Name string      `json:"name"`
		Role domain.Role `json:"role"`
	}
	_ = json.NewDecoder(meRec.Body).Decode(&meData)
	if meData.ID != "u100" || meData.Name != "SSO 太郎" || meData.Role != domain.RoleAdmin {
		t.Errorf("unexpected /api/me response: %+v", meData)
	}
}

