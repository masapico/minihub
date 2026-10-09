package oidc

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidToken   = errors.New("invalid or expired token")
	ErrInvalidCode    = errors.New("invalid or expired authorization code")
	ErrClientNotFound = errors.New("client not found")
	ErrInvalidSecret  = errors.New("invalid client secret")
	ErrInvalidURI     = errors.New("redirect uri mismatch")
)

type Client struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Secret       string   `json:"secret"`
	RedirectURIs []string `json:"redirectURIs"`
	LaunchURL    string   `json:"launchURL,omitempty"`
	Icon         string   `json:"icon,omitempty"`
}

func (c *Client) ValidateRedirectURI(uri string) bool {
	for _, registered := range c.RedirectURIs {
		if registered == uri {
			return true
		}
	}
	return false
}

func (c *Client) ValidateSecret(secret string) bool {
	if c.Secret == "" || secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Secret), []byte(secret)) == 1
}

type AuthCode struct {
	Code        string
	ClientID    string
	UserID      string
	RedirectURI string
	Scope       string
	State       string
	ExpiresAt   time.Time
}

type TokenClaims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"`
	Audience  string   `json:"aud"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Groups    []string `json:"groups,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
}

type Engine struct {
	issuer    string
	jwtSecret []byte
	mu        sync.Mutex
	codes     map[string]AuthCode
	clients   map[string]Client
}

func NewEngine(issuer string, jwtSecret []byte, clients []Client) *Engine {
	clientMap := make(map[string]Client, len(clients))
	for _, c := range clients {
		clientMap[c.ID] = c
	}
	if len(jwtSecret) == 0 {
		jwtSecret = make([]byte, 32)
		_, _ = rand.Read(jwtSecret)
	}
	return &Engine{
		issuer:    strings.TrimSuffix(issuer, "/"),
		jwtSecret: jwtSecret,
		codes:     make(map[string]AuthCode),
		clients:   clientMap,
	}
}

func (e *Engine) GetClient(clientID string) (Client, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.clients[clientID]
	return c, ok
}

func (e *Engine) ListClients() []Client {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := make([]Client, 0, len(e.clients))
	for _, c := range e.clients {
		list = append(list, c)
	}
	return list
}

func (e *Engine) CreateAuthCode(clientID, userID, redirectURI, scope, state string) (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	code := hex.EncodeToString(bytes)

	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	// Clean expired codes
	for k, c := range e.codes {
		if now.After(c.ExpiresAt) {
			delete(e.codes, k)
		}
	}

	e.codes[code] = AuthCode{
		Code:        code,
		ClientID:    clientID,
		UserID:      userID,
		RedirectURI: redirectURI,
		Scope:       scope,
		State:       state,
		ExpiresAt:   now.Add(2 * time.Minute),
	}
	return code, nil
}

func (e *Engine) ExchangeCode(code, clientID, clientSecret, redirectURI string) (AuthCode, error) {
	e.mu.Lock()
	authCode, ok := e.codes[code]
	if ok {
		delete(e.codes, code) // Single-use token
	}
	client, clientOK := e.clients[clientID]
	e.mu.Unlock()

	if !ok || time.Now().After(authCode.ExpiresAt) {
		return AuthCode{}, ErrInvalidCode
	}
	if !clientOK {
		return AuthCode{}, ErrClientNotFound
	}
	if !client.ValidateSecret(clientSecret) {
		return AuthCode{}, ErrInvalidSecret
	}
	if authCode.ClientID != clientID {
		return AuthCode{}, ErrClientNotFound
	}
	if authCode.RedirectURI != redirectURI {
		return AuthCode{}, ErrInvalidURI
	}
	return authCode, nil
}

func (e *Engine) SignJWT(claims TokenClaims) (string, error) {
	headerJSON := `{"alg":"HS256","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)

	signingInput := headerB64 + "." + payloadB64
	mac := hmac.New(sha256.New, e.jwtSecret)
	mac.Write([]byte(signingInput))
	sigB64 := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + sigB64, nil
}

func (e *Engine) VerifyJWT(tokenString string) (TokenClaims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return TokenClaims{}, ErrInvalidToken
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, e.jwtSecret)
	mac.Write([]byte(signingInput))
	expectedSig := mac.Sum(nil)

	actualSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || subtle.ConstantTimeCompare(expectedSig, actualSig) != 1 {
		return TokenClaims{}, ErrInvalidToken
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TokenClaims{}, ErrInvalidToken
	}

	var claims TokenClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return TokenClaims{}, ErrInvalidToken
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return TokenClaims{}, ErrInvalidToken
	}

	return claims, nil
}

func (e *Engine) Issuer() string {
	return e.issuer
}

