package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

type SSOHandler struct {
	cfg      config.MiniAuthConfig
	basePath string
	store    storage.Storage
	session  SessionEstablisher
	client   *http.Client
}

type SessionEstablisher interface {
	EstablishSession(http.ResponseWriter, *http.Request, *domain.User) (string, string, error)
}

func NewSSOHandler(cfg config.MiniAuthConfig, basePath string, store storage.Storage, session SessionEstablisher) *SSOHandler {
	return &SSOHandler{
		cfg:      cfg,
		basePath: basePath,
		store:    store,
		session:  session,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (h *SSOHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if next == "" {
		next = h.basePath + "/"
	}

	state := url.QueryEscape(next)
	callbackURL := h.buildCallbackURL(r)

	authorizeURL := fmt.Sprintf("%s/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=openid+profile&state=%s",
		h.cfg.URL,
		url.QueryEscape(h.cfg.ClientID),
		url.QueryEscape(callbackURL),
		state,
	)

	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

func (h *SSOHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" {
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}

	callbackURL := h.buildCallbackURL(r)

	// Exchange code for token
	tokenURL := h.cfg.URL + "/oauth/token"
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {h.cfg.ClientID},
		"client_secret": {h.cfg.ClientSecret},
		"redirect_uri":  {callbackURL},
	}

	resp, err := h.client.PostForm(tokenURL, form)
	if err != nil {
		http.Error(w, "failed to exchange token", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "token endpoint returned error", http.StatusBadGateway)
		return
	}

	var tokenData struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenData); err != nil || tokenData.AccessToken == "" {
		http.Error(w, "invalid token response", http.StatusBadGateway)
		return
	}

	// Fetch UserInfo
	userInfoURL := h.cfg.URL + "/oauth/userinfo"
	uReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, userInfoURL, nil)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	uReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)

	uResp, err := h.client.Do(uReq)
	if err != nil || uResp.StatusCode != http.StatusOK {
		http.Error(w, "failed to fetch userinfo", http.StatusBadGateway)
		return
	}
	defer uResp.Body.Close()

	var userInfo struct {
		Sub    string   `json:"sub"`
		Name   string   `json:"name"`
		Role   string   `json:"role"`
		Groups []string `json:"groups"`
	}
	if err := json.NewDecoder(uResp.Body).Decode(&userInfo); err != nil || userInfo.Sub == "" {
		http.Error(w, "invalid userinfo response", http.StatusBadGateway)
		return
	}

	// JIT Provision / Update
	role := domain.RoleUser
	if userInfo.Role == "admin" {
		role = domain.RoleAdmin
	}

	user, err := h.store.GetUser(r.Context(), userInfo.Sub)
	if err != nil || user == nil {
		user = &domain.User{
			Version:        1,
			ID:             userInfo.Sub,
			Name:           userInfo.Name,
			Role:           role,
			Groups:         userInfo.Groups,
			Enabled:        true,
			AuthGeneration: 1,
		}
		_ = h.store.SaveUser(r.Context(), user)
	} else {
		user.Name = userInfo.Name
		user.Role = role
		user.Groups = userInfo.Groups
		user.Enabled = true
		_ = h.store.SaveUser(r.Context(), user)
	}

	// Establish session
	if _, _, err := h.session.EstablishSession(w, r, user); err != nil {
		http.Error(w, "failed to establish session", http.StatusInternalServerError)
		return
	}

	// Redirect to target
	next, _ := url.QueryUnescape(state)
	if next == "" || !strings.HasPrefix(next, "/") || strings.Contains(next, "\\") {
		next = h.basePath + "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func (h *SSOHandler) buildCallbackURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	return fmt.Sprintf("%s://%s%s/api/auth/sso/callback", scheme, host, h.basePath)
}

