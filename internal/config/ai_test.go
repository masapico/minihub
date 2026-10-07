package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAIAccountValidation(t *testing.T) {
	t.Setenv("MINIHUB_MISSING_AI_TEST_TOKEN", "")
	a := AIAccount{ID: "helper", Name: "社内AI", URL: "http://127.0.0.1:8081/ai", Model: "test-model"}
	if err := ValidateAIAccounts([]AIAccount{a}); err != nil {
		t.Fatal(err)
	}
	if !a.IsEnabled() || a.RequestTimeout() != 120*time.Second {
		t.Fatal("invalid defaults")
	}
	for _, change := range []func(*AIAccount){
		func(a *AIAccount) { a.ID = strings.Repeat("a", 62) },
		func(a *AIAccount) { a.ID = "../bad" },
		func(a *AIAccount) { a.Name = " " },
		func(a *AIAccount) { a.Model = "" },
		func(a *AIAccount) { a.Model = " \t " },
		func(a *AIAccount) { a.URL = "ftp://host/ai" },
		func(a *AIAccount) { a.URL = "http://user:secret@host/ai" },
		func(a *AIAccount) { a.URL = "http://host/ai#fragment" },
		func(a *AIAccount) { a.Timeout = "0s" },
		func(a *AIAccount) { a.Timeout = "invalid" },
		func(a *AIAccount) { a.Token = " \t " },
		func(a *AIAccount) { a.Token = "secret\r\ninjected: value" },
		func(a *AIAccount) { a.Token, a.TokenEnv = "secret", "MINIHUB_AI_TEST_TOKEN" },
		func(a *AIAccount) { a.TokenEnv = "MINIHUB_MISSING_AI_TEST_TOKEN" },
	} {
		b := a
		change(&b)
		if err := ValidateAIAccounts([]AIAccount{b}); err == nil {
			t.Fatalf("accepted invalid account: %#v", b)
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatal("validation error leaked token")
		}
	}
	if err := ValidateAIAccounts([]AIAccount{a, a}); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
	t.Setenv("MINIHUB_AI_TEST_TOKEN", "secret")
	a.TokenEnv = "MINIHUB_AI_TEST_TOKEN"
	if err := ValidateAIAccounts([]AIAccount{a}); err != nil {
		t.Fatal(err)
	}
	disabled := false
	a.Enabled = &disabled
	a.TokenEnv = "MINIHUB_MISSING_AI_TEST_TOKEN"
	if err := ValidateAIAccounts([]AIAccount{a}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAIAccountToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "minihub.json")
	body := `{"aiAccounts":[{"id":"helper","name":"社内AI","url":"http://127.0.0.1:8081/v1/chat/completions","model":"test-model","systemPrompt":"簡潔に回答してください。","token":"direct-test-token"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, loaded, err := Load(path, true)
	if err != nil || !loaded {
		t.Fatalf("loaded=%v error=%v", loaded, err)
	}
	if len(cfg.AIAccounts) != 1 || cfg.AIAccounts[0].Token != "direct-test-token" || cfg.AIAccounts[0].TokenEnv != "" || cfg.AIAccounts[0].Model != "test-model" || cfg.AIAccounts[0].SystemPrompt != "簡潔に回答してください。" {
		t.Fatal("AI settings were not loaded")
	}
}
