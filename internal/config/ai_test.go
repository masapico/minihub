package config

import (
	"strings"
	"testing"
	"time"
)

func TestAIAccountValidation(t *testing.T) {
	a := AIAccount{ID: "helper", Name: "社内AI", URL: "http://127.0.0.1:8081/ai"}
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
		func(a *AIAccount) { a.URL = "ftp://host/ai" },
		func(a *AIAccount) { a.URL = "http://user:secret@host/ai" },
		func(a *AIAccount) { a.URL = "http://host/ai#fragment" },
		func(a *AIAccount) { a.Timeout = "0s" },
		func(a *AIAccount) { a.Timeout = "invalid" },
		func(a *AIAccount) { a.TokenEnv = "MINIHUB_MISSING_AI_TEST_TOKEN" },
	} {
		b := a
		change(&b)
		if err := ValidateAIAccounts([]AIAccount{b}); err == nil {
			t.Fatalf("accepted invalid account: %#v", b)
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
