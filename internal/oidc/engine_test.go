package oidc

import (
	"testing"
	"time"
)

func TestEngine_AuthCodeAndToken(t *testing.T) {
	clients := []Client{
		{
			ID:           "test-client",
			Name:         "Test Client",
			Secret:       "super-secret",
			RedirectURIs: []string{"http://localhost:3000/callback"},
		},
	}
	engine := NewEngine("http://auth.local", []byte("secret-key-32-bytes-long-padding!!"), clients)

	// Auth code creation
	code, err := engine.CreateAuthCode("test-client", "u001", "http://localhost:3000/callback", "openid profile", "state123")
	if err != nil {
		t.Fatalf("CreateAuthCode failed: %v", err)
	}

	// Code exchange with wrong secret
	_, err = engine.ExchangeCode(code, "test-client", "wrong-secret", "http://localhost:3000/callback")
	if err == nil {
		t.Fatalf("expected error for wrong secret, got nil")
	}

	// Code exchange with correct credentials
	// Note: First attempt failed, code was single-use and deleted!
	// Re-create code
	code, err = engine.CreateAuthCode("test-client", "u001", "http://localhost:3000/callback", "openid profile", "state123")
	if err != nil {
		t.Fatalf("CreateAuthCode failed: %v", err)
	}

	authCode, err := engine.ExchangeCode(code, "test-client", "super-secret", "http://localhost:3000/callback")
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}
	if authCode.UserID != "u001" {
		t.Errorf("expected UserID u001, got %s", authCode.UserID)
	}

	// Try reuse code (should fail)
	_, err = engine.ExchangeCode(code, "test-client", "super-secret", "http://localhost:3000/callback")
	if err == nil {
		t.Errorf("expected error reusing code, got nil")
	}

	// JWT signing and verifying
	now := time.Now().Unix()
	claims := TokenClaims{
		Issuer:    engine.Issuer(),
		Subject:   "u001",
		Audience:  "test-client",
		Name:      "Test User",
		Role:      "admin",
		Groups:    []string{"dev", "admin"},
		Scope:     "openid profile",
		IssuedAt:  now,
		ExpiresAt: now + 3600,
	}

	jwtStr, err := engine.SignJWT(claims)
	if err != nil {
		t.Fatalf("SignJWT failed: %v", err)
	}

	verified, err := engine.VerifyJWT(jwtStr)
	if err != nil {
		t.Fatalf("VerifyJWT failed: %v", err)
	}
	if verified.Subject != "u001" || verified.Name != "Test User" || verified.Role != "admin" {
		t.Errorf("verified claims mismatch: %+v", verified)
	}
}

