package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type AIAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  *bool  `json:"enabled,omitempty"`
	URL      string `json:"url"`
	Timeout  string `json:"timeout,omitempty"`
	TokenEnv string `json:"tokenEnv,omitempty"`
}

func (a AIAccount) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }

func (a AIAccount) RequestTimeout() time.Duration {
	if a.Timeout == "" {
		return 120 * time.Second
	}
	d, _ := time.ParseDuration(a.Timeout)
	return d
}

func ValidateAIAccounts(accounts []AIAccount) error {
	ids := map[string]bool{}
	valid := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,60}$`)
	for _, a := range accounts {
		if !valid.MatchString(a.ID) || ids[a.ID] {
			return fmt.Errorf("aiAccounts: invalid or duplicate id %q", a.ID)
		}
		ids[a.ID] = true
		if strings.TrimSpace(a.Name) == "" || utf8.RuneCountInString(a.Name) > 100 {
			return fmt.Errorf("aiAccounts %s: name must contain 1 to 100 characters", a.ID)
		}
		u, err := url.Parse(a.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("aiAccounts %s: invalid HTTP/HTTPS URL", a.ID)
		}
		if a.Timeout != "" {
			if d, err := time.ParseDuration(a.Timeout); err != nil || d <= 0 {
				return fmt.Errorf("aiAccounts %s: timeout must be positive", a.ID)
			}
		}
		if a.IsEnabled() && a.TokenEnv != "" && strings.TrimSpace(os.Getenv(a.TokenEnv)) == "" {
			return fmt.Errorf("aiAccounts %s: token environment variable is missing", a.ID)
		}
	}
	return nil
}
