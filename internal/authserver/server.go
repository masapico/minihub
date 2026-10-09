package authserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"context"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/authstorage"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/oidc"
)

type Server struct {
	cfg          Config
	store        authstorage.Storage
	sessions     *auth.Manager
	oidc         *oidc.Engine
	logger       *slog.Logger
	serviceToken string
}

func NewServer(cfg Config, store authstorage.Storage, sessions *auth.Manager, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	jwtSecret := []byte(cfg.OIDC.JWTSecret)
	engine := oidc.NewEngine(cfg.OIDC.Issuer, jwtSecret, nil)

	// Load existing clients from store
	savedClients, err := store.ListClients(context.Background())
	if err == nil {
		for _, c := range savedClients {
			engine.RegisterClient(*c)
		}
	}
	// Import config clients if not already present
	for _, c := range cfg.Clients {
		if _, err := store.GetClient(context.Background(), c.ID); err != nil {
			clientCopy := c
			_ = store.SaveClient(context.Background(), &clientCopy)
			engine.RegisterClient(clientCopy)
		}
	}

	return &Server{
		cfg:          cfg,
		store:        store,
		sessions:     sessions,
		oidc:         engine,
		logger:       logger,
		serviceToken: cfg.OIDC.ServiceToken,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// OIDC Endpoints
	mux.HandleFunc("/.well-known/openid-configuration", s.handleOpenIDConfiguration)
	mux.HandleFunc("/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/oauth/token", s.handleToken)
	mux.HandleFunc("/oauth/userinfo", s.handleUserInfo)

	// Directory Sync API for minihub
	mux.HandleFunc("/api/directory", s.handleDirectory)

	// Session & Me APIs
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/me/password", s.handleChangePassword)
	mux.HandleFunc("/api/portal/apps", s.handlePortalApps)

	// Admin APIs
	mux.HandleFunc("/api/admin/users", s.handleAdminUsers)
	mux.HandleFunc("/api/admin/users/bulk", s.handleAdminUsersBulk)
	mux.HandleFunc("/api/admin/groups", s.handleAdminGroups)
	mux.HandleFunc("/api/admin/clients", s.handleAdminClients)

	return mux
}

// OIDC Discovery
func (s *Server) handleOpenIDConfiguration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	issuer := s.oidc.Issuer()
	resp := map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"userinfo_endpoint":                     issuer + "/oauth/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"HS256"},
		"scopes_supported":                      []string{"openid", "profile"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	}
	writeJSON(w, http.StatusOK, resp)
}

// OIDC Authorize Endpoint
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientID := r.URL.Query().Get("client_id")
	redirectURI := r.URL.Query().Get("redirect_uri")
	responseType := r.URL.Query().Get("response_type")
	scope := r.URL.Query().Get("scope")
	state := r.URL.Query().Get("state")

	client, ok := s.oidc.GetClient(clientID)
	if !ok {
		http.Error(w, "invalid client_id", http.StatusBadRequest)
		return
	}
	if !client.ValidateRedirectURI(redirectURI) {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	if responseType != "code" {
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	}

	// Check if authenticated
	userID, err := s.sessions.Authenticate(r)
	if err != nil {
		// Redirect to login with next parameter
		loginURL := s.cfg.Server.BasePath + "/login?next=" + url.QueryEscape(s.cfg.Server.BasePath+r.URL.RequestURI())
		http.Redirect(w, r, loginURL, http.StatusFound)
		return
	}

	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil || !user.Enabled {
		http.Error(w, "account disabled or not found", http.StatusForbidden)
		return
	}

	// Auto-approve: generate code and redirect back immediately
	code, err := s.oidc.CreateAuthCode(clientID, user.ID, redirectURI, scope, state)
	if err != nil {
		s.logger.Error("failed to create auth code", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	target, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect uri", http.StatusBadRequest)
		return
	}
	q := target.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	target.RawQuery = q.Encode()

	http.Redirect(w, r, target.String(), http.StatusFound)
}

// OIDC Token Endpoint
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var clientID, clientSecret, code, redirectURI, grantType string

	// Try Basic Auth first
	username, password, hasBasic := r.BasicAuth()
	if hasBasic {
		clientID = username
		clientSecret = password
	}

	_ = r.ParseForm()
	if clientID == "" {
		clientID = r.FormValue("client_id")
	}
	if clientSecret == "" {
		clientSecret = r.FormValue("client_secret")
	}
	code = r.FormValue("code")
	redirectURI = r.FormValue("redirect_uri")
	grantType = r.FormValue("grant_type")

	if grantType != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}

	authCode, err := s.oidc.ExchangeCode(code, clientID, clientSecret, redirectURI)
	if err != nil {
		s.logger.Warn("code exchange failed", "error", err, "client", clientID)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}

	user, err := s.store.GetUser(r.Context(), authCode.UserID)
	if err != nil || !user.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}

	now := time.Now().Unix()
	claims := oidc.TokenClaims{
		Issuer:    s.oidc.Issuer(),
		Subject:   user.ID,
		Audience:  clientID,
		Name:      user.Name,
		Role:      string(user.Role),
		Groups:    user.Groups,
		Scope:     authCode.Scope,
		IssuedAt:  now,
		ExpiresAt: now + 3600, // 1 hour
	}

	tokenStr, err := s.oidc.SignJWT(claims)
	if err != nil {
		s.logger.Error("failed to sign token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	resp := map[string]any{
		"access_token": tokenStr,
		"id_token":     tokenStr,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"scope":        authCode.Scope,
	}
	writeJSON(w, http.StatusOK, resp)
}

// OIDC UserInfo Endpoint
func (s *Server) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authHeader := r.Header.Get("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	claims, err := s.oidc.VerifyJWT(parts[1])
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	user, err := s.store.GetUser(r.Context(), claims.Subject)
	if err != nil || !user.Enabled {
		http.Error(w, "user disabled or not found", http.StatusUnauthorized)
		return
	}

	resp := map[string]any{
		"sub":                user.ID,
		"name":               user.Name,
		"preferred_username": user.ID,
		"role":               string(user.Role),
		"groups":             user.Groups,
	}
	writeJSON(w, http.StatusOK, resp)
}

// Directory API for minihub sync
func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify service token
	if s.serviceToken != "" {
		authHeader := r.Header.Get("Authorization")
		expected := "Bearer " + s.serviceToken
		if subtle.ConstantTimeCompare([]byte(authHeader), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	type DirectoryUser struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Role    string   `json:"role"`
		Groups  []string `json:"groups,omitempty"`
		Enabled bool     `json:"enabled"`
	}

	dirUsers := make([]DirectoryUser, len(users))
	for i, u := range users {
		dirUsers[i] = DirectoryUser{
			ID:      u.ID,
			Name:    u.Name,
			Role:    string(u.Role),
			Groups:  u.Groups,
			Enabled: u.Enabled,
		}
	}

	resp := map[string]any{
		"users":  dirUsers,
		"groups": groups,
	}
	writeJSON(w, http.StatusOK, resp)
}

// Session Login
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		UserID   string `json:"userId"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストを読み取れませんでした"})
		return
	}

	userID, csrf, err := s.sessions.Login(w, r, req.UserID, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "ログイン試行回数が上限を超えました。しばらく待ってからお試しください"})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ユーザーIDまたはパスワードが違います"})
		return
	}

	user, _ := s.store.GetUser(r.Context(), userID)
	writeJSON(w, http.StatusOK, map[string]any{
		"userId":    user.ID,
		"name":      user.Name,
		"role":      user.Role,
		"csrfToken": csrf,
	})
}

// Session Logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = s.sessions.Logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// Me
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID, err := s.sessions.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	csrf, _ := s.sessions.CSRFToken(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"userId":    user.ID,
		"name":      user.Name,
		"role":      user.Role,
		"groups":    user.Groups,
		"csrfToken": csrf,
	})
}

// Change Password
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, err := s.sessions.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.sessions.ValidateCSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストを読み取れませんでした"})
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "新しいパスワードは8文字以上で指定してください"})
		return
	}

	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "現在のパスワードが正しくありません"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	user.PasswordHash = string(hash)
	user.AuthGeneration++
	if err := s.store.SaveUser(r.Context(), user); err != nil {
		http.Error(w, "failed to save user", http.StatusInternalServerError)
		return
	}
	_ = s.sessions.RefreshCurrentSession(r, user.AuthGeneration)
	w.WriteHeader(http.StatusNoContent)
}

// Portal Apps
func (s *Server) handlePortalApps(w http.ResponseWriter, r *http.Request) {
	_, err := s.sessions.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	clients, err := s.store.ListClients(r.Context())
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	type AppItem struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		LaunchURL string `json:"launchURL"`
		Icon      string `json:"icon"`
	}
	apps := make([]AppItem, 0, len(clients))
	for _, c := range clients {
		launchURL := c.LaunchURL
		if launchURL == "" && len(c.RedirectURIs) > 0 {
			launchURL = c.RedirectURIs[0]
		}
		icon := c.Icon
		if icon == "" {
			icon = "box-arrow-up-right"
		}
		apps = append(apps, AppItem{
			ID:        c.ID,
			Name:      c.Name,
			LaunchURL: launchURL,
			Icon:      icon,
		})
	}
	writeJSON(w, http.StatusOK, apps)
}

// Admin Users CRUD
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	userID, err := s.sessions.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	adminUser, _ := s.store.GetUser(r.Context(), userID)
	if adminUser.Role != domain.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := s.store.ListUsers(r.Context())
		if err != nil {
			http.Error(w, "failed to list users", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, users)
	case http.MethodPost:
		if !s.sessions.ValidateCSRF(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
			return
		}
		var req struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Password string   `json:"password"`
			Role     string   `json:"role"`
			Groups   []string `json:"groups"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストを読み取れませんでした"})
			return
		}
		req.ID = strings.TrimSpace(req.ID)
		req.Name = strings.TrimSpace(req.Name)
		if req.ID == "" || req.Name == "" || len(req.Password) < 8 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID・名前・8文字以上のパスワードを指定してください"})
			return
		}
		role := domain.RoleUser
		if req.Role == "admin" {
			role = domain.RoleAdmin
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		newUser := &domain.User{
			Version:        1,
			ID:             req.ID,
			Name:           req.Name,
			Role:           role,
			Groups:         req.Groups,
			Enabled:        true,
			PasswordHash:   string(hash),
			AuthGeneration: 1,
		}
		if err := s.store.SaveUser(r.Context(), newUser); err != nil {
			http.Error(w, "failed to save user", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, newUser)
	case http.MethodPut:
		if !s.sessions.ValidateCSRF(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
			return
		}
		var req struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Password string   `json:"password,omitempty"`
			Role     string   `json:"role"`
			Groups   []string `json:"groups"`
			Enabled  bool     `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストを読み取れませんでした"})
			return
		}
		user, err := s.store.GetUser(r.Context(), req.ID)
		if err != nil {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		user.Name = req.Name
		if req.Role == "admin" {
			user.Role = domain.RoleAdmin
		} else {
			user.Role = domain.RoleUser
		}
		user.Groups = req.Groups
		user.Enabled = req.Enabled
		if req.Password != "" {
			if len(req.Password) < 8 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "パスワードは8文字以上で指定してください"})
				return
			}
			hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
			user.PasswordHash = string(hash)
			user.AuthGeneration++
		}
		if err := s.store.SaveUser(r.Context(), user); err != nil {
			http.Error(w, "failed to save user", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, user)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Admin Bulk Users
func (s *Server) handleAdminUsersBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, err := s.sessions.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	adminUser, _ := s.store.GetUser(r.Context(), userID)
	if adminUser.Role != domain.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !s.sessions.ValidateCSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(body, &req)

	lines := strings.Split(req.Text, "\n")
	created := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		name := strings.TrimSpace(parts[1])
		pw := strings.TrimSpace(parts[2])
		role := domain.RoleUser
		if len(parts) >= 4 && strings.TrimSpace(parts[3]) == "admin" {
			role = domain.RoleAdmin
		}
		var groups []string
		if len(parts) >= 5 && strings.TrimSpace(parts[4]) != "" {
			groups = strings.Split(strings.TrimSpace(parts[4]), ",")
		}
		if len(pw) < 8 {
			continue
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		u := &domain.User{
			Version:        1,
			ID:             id,
			Name:           name,
			Role:           role,
			Groups:         groups,
			Enabled:        true,
			PasswordHash:   string(hash),
			AuthGeneration: 1,
		}
		if err := s.store.SaveUser(r.Context(), u); err == nil {
			created++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created})
}

// Admin Groups CRUD
func (s *Server) handleAdminGroups(w http.ResponseWriter, r *http.Request) {
	userID, err := s.sessions.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	adminUser, _ := s.store.GetUser(r.Context(), userID)
	if adminUser.Role != domain.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		groups, err := s.store.ListGroups(r.Context())
		if err != nil {
			http.Error(w, "failed to list groups", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, groups)
	case http.MethodPost:
		if !s.sessions.ValidateCSRF(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
			return
		}
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストを読み取れませんでした"})
			return
		}
		req.ID = strings.TrimSpace(req.ID)
		req.Name = strings.TrimSpace(req.Name)
		if req.ID == "" || req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "グループIDと名前を指定してください"})
			return
		}
		group := &domain.Group{Version: 1, ID: req.ID, Name: req.Name}
		if err := s.store.SaveGroup(r.Context(), group); err != nil {
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, group)
	case http.MethodDelete:
		if !s.sessions.ValidateCSRF(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
			return
		}
		groupID := r.URL.Query().Get("id")
		if groupID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		_ = s.store.DeleteGroup(r.Context(), groupID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Admin Clients CRUD
func (s *Server) handleAdminClients(w http.ResponseWriter, r *http.Request) {
	userID, err := s.sessions.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	adminUser, _ := s.store.GetUser(r.Context(), userID)
	if adminUser.Role != domain.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		clients, err := s.store.ListClients(r.Context())
		if err != nil {
			http.Error(w, "failed to list clients", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, clients)
	case http.MethodPost:
		if !s.sessions.ValidateCSRF(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
			return
		}
		var req oidc.Client
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストを読み取れませんでした"})
			return
		}
		req.ID = strings.TrimSpace(req.ID)
		req.Name = strings.TrimSpace(req.Name)
		req.Secret = strings.TrimSpace(req.Secret)
		req.LaunchURL = strings.TrimSpace(req.LaunchURL)
		req.Icon = strings.TrimSpace(req.Icon)
		if req.ID == "" || req.Name == "" || req.Secret == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "クライアントID、名前、シークレットを入力してください"})
			return
		}
		cleanURIs := make([]string, 0, len(req.RedirectURIs))
		for _, u := range req.RedirectURIs {
			u = strings.TrimSpace(u)
			if u != "" {
				cleanURIs = append(cleanURIs, u)
			}
		}
		if len(cleanURIs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リダイレクトURIを少なくとも1つ入力してください"})
			return
		}
		req.RedirectURIs = cleanURIs

		if err := s.store.SaveClient(r.Context(), &req); err != nil {
			http.Error(w, "failed to save client", http.StatusInternalServerError)
			return
		}
		s.oidc.RegisterClient(req)
		writeJSON(w, http.StatusCreated, req)
	case http.MethodDelete:
		if !s.sessions.ValidateCSRF(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "セキュリティトークンが無効です"})
			return
		}
		clientID := strings.TrimSpace(r.URL.Query().Get("id"))
		if clientID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		if err := s.store.DeleteClient(r.Context(), clientID); err != nil {
			http.Error(w, "failed to delete client", http.StatusInternalServerError)
			return
		}
		s.oidc.DeleteClient(clientID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
