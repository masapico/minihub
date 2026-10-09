package authserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/authstorage"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/oidc"
)

func TestAuthServer_OIDCAndDirectory(t *testing.T) {
	tempDir := t.TempDir()
	store, err := authstorage.Open(tempDir, "file")
	if err != nil {
		t.Fatalf("authstorage.Open failed: %v", err)
	}
	defer store.Close()

	// Seed user
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	testUser := &domain.User{
		Version:        1,
		ID:             "u001",
		Name:           "Test User",
		Role:           domain.RoleAdmin,
		Groups:         []string{"dev"},
		Enabled:        true,
		PasswordHash:   string(hash),
		AuthGeneration: 1,
	}
	if err := store.SaveUser(context.Background(), testUser); err != nil {
		t.Fatalf("SaveUser failed: %v", err)
	}

	sessions := auth.NewManager(store, false)

	cfg := Defaults()
	cfg.OIDC.Issuer = "http://auth.local"
	cfg.OIDC.ServiceToken = "service-token-123"
	cfg.Clients = []oidc.Client{
		{
			ID:           "minihub",
			Name:         "minihub Chat",
			Secret:       "minihub-secret",
			RedirectURIs: []string{"http://chat.local/callback"},
		},
	}

	srv := NewServer(cfg, store, sessions, nil)
	handler := srv.Routes()

	// 1. Test Directory API
	req := httptest.NewRequest(http.MethodGet, "/api/directory", nil)
	req.Header.Set("Authorization", "Bearer service-token-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Directory API returned %d, expected 200", rec.Code)
	}
	var dirResp struct {
		Users []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"users"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&dirResp); err != nil {
		t.Fatalf("failed to decode directory response: %v", err)
	}
	if len(dirResp.Users) != 1 || dirResp.Users[0].ID != "u001" {
		t.Errorf("unexpected directory users: %+v", dirResp.Users)
	}

	// 2. Test Login to create session
	loginBody := `{"userId":"u001","password":"password123"}`
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed with %d: %s", loginRec.Code, loginRec.Body.String())
	}
	sessionCookie := loginRec.Result().Cookies()[0]

	// 3. Test OIDC Authorize
	authURL := "/oauth/authorize?client_id=minihub&redirect_uri=" + url.QueryEscape("http://chat.local/callback") + "&response_type=code&state=xyz123"
	authReq := httptest.NewRequest(http.MethodGet, authURL, nil)
	authReq.AddCookie(sessionCookie)
	authRec := httptest.NewRecorder()
	handler.ServeHTTP(authRec, authReq)

	if authRec.Code != http.StatusFound {
		t.Fatalf("authorize returned %d, expected 302", authRec.Code)
	}
	location := authRec.Header().Get("Location")
	parsedLoc, err := url.Parse(location)
	if err != nil {
		t.Fatalf("invalid redirect location: %v", err)
	}
	code := parsedLoc.Query().Get("code")
	if code == "" || parsedLoc.Query().Get("state") != "xyz123" {
		t.Fatalf("missing code or state mismatch in location: %s", location)
	}

	// 4. Test OIDC Token Exchange
	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {"minihub"},
		"client_secret": {"minihub-secret"},
		"redirect_uri":  {"http://chat.local/callback"},
	}
	tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRec := httptest.NewRecorder()
	handler.ServeHTTP(tokenRec, tokenReq)

	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token exchange returned %d: %s", tokenRec.Code, tokenRec.Body.String())
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(tokenRec.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}
	if tokenResp.AccessToken == "" {
		t.Fatal("expected access_token, got empty")
	}

	// 5. Test OIDC UserInfo
	userinfoReq := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
	userinfoReq.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	userinfoRec := httptest.NewRecorder()
	handler.ServeHTTP(userinfoRec, userinfoReq)

	if userinfoRec.Code != http.StatusOK {
		t.Fatalf("userinfo returned %d: %s", userinfoRec.Code, userinfoRec.Body.String())
	}
	var userinfoResp struct {
		Sub    string   `json:"sub"`
		Name   string   `json:"name"`
		Role   string   `json:"role"`
		Groups []string `json:"groups"`
	}
	if err := json.NewDecoder(userinfoRec.Body).Decode(&userinfoResp); err != nil {
		t.Fatalf("failed to decode userinfo response: %v", err)
	}
	if userinfoResp.Sub != "u001" || userinfoResp.Name != "Test User" || userinfoResp.Role != "admin" {
		t.Errorf("unexpected userinfo response: %+v", userinfoResp)
	}

	// 6. Test Admin Client CRUD
	// Authenticate admin session
	var loginResp struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.NewDecoder(loginRec.Body).Decode(&loginResp)

	// Add dynamic client
	newClientPayload := `{"id":"wiki","name":"Wiki","secret":"wiki-secret","redirectURIs":["http://wiki.local/callback"],"launchURL":"http://wiki.local"}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/clients", strings.NewReader(newClientPayload))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-CSRF-Token", loginResp.CSRFToken)
	createReq.AddCookie(sessionCookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create client failed: %d: %s", createRec.Code, createRec.Body.String())
	}

	// Verify client is returned in portal apps
	portalReq := httptest.NewRequest(http.MethodGet, "/api/portal/apps", nil)
	portalReq.AddCookie(sessionCookie)
	portalRec := httptest.NewRecorder()
	handler.ServeHTTP(portalRec, portalReq)
	if portalRec.Code != http.StatusOK {
		t.Fatalf("portal apps failed: %d", portalRec.Code)
	}
	if !strings.Contains(portalRec.Body.String(), "wiki") {
		t.Fatalf("portal apps should contain wiki: %s", portalRec.Body.String())
	}

	// Delete client
	delReq := httptest.NewRequest(http.MethodDelete, "/api/admin/clients?id=wiki", nil)
	delReq.Header.Set("X-CSRF-Token", loginResp.CSRFToken)
	delReq.AddCookie(sessionCookie)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete client failed: %d", delRec.Code)
	}

	// Verify client is removed
	portalRec2 := httptest.NewRecorder()
	handler.ServeHTTP(portalRec2, portalReq)
	if strings.Contains(portalRec2.Body.String(), "wiki") {
		t.Fatalf("portal apps should not contain wiki after delete: %s", portalRec2.Body.String())
	}
}
