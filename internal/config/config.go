package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Config struct {
	AIAccounts    []AIAccount   `json:"aiAccounts,omitempty"`
	Storage       Storage       `json:"storage"`
	Version       int           `json:"version"`
	Server        Server        `json:"server"`
	UI            UI            `json:"ui"`
	Features      Features      `json:"features"`
	Notifications Notifications `json:"notifications"`
	Retention     Retention     `json:"retention"`
	Logging       Logging       `json:"logging"`
	InitialAdmin  *InitialAdmin `json:"initialAdmin,omitempty"`
}
type Storage struct {
	Type string `json:"type"`
}
type UI struct {
	WorkspaceTitle string `json:"workspaceTitle"`
	LoginMessage   string `json:"loginMessage"`
}
type Server struct {
	TLS           TLS    `json:"tls"`
	ListenAddress string `json:"listenAddress"`
	BasePath      string `json:"basePath"`
	DataDir       string `json:"dataDir"`
	SecureCookie  bool   `json:"secureCookie"`
	SessionTTL    string `json:"sessionTTL"`
}
type Features struct {
	SelfPasswordChange bool   `json:"selfPasswordChange"`
	NetworkPathMode    string `json:"networkPathMode"`
}
type TLS struct {
	Enabled  bool   `json:"enabled"`
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}
type Notifications struct {
	OSNotificationsEnabled bool `json:"osNotificationsEnabled"`
	MentionRetentionDays   int  `json:"mentionRetentionDays"`
}
type Retention struct {
	Days int `json:"days"`
}
type Logging struct {
	Level string `json:"level"`
}
type InitialAdmin struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

const DefaultFilename = "minihub.json"

func Defaults() Config {
	return Config{
		Storage: Storage{Type: "file"},
		Version: 1,
		Server:  Server{ListenAddress: "127.0.0.1:8080", DataDir: "data", SessionTTL: "12h"},
		UI: UI{
			WorkspaceTitle: "minihub",
			LoginMessage:   "ユーザーIDとパスワードを入力してください。",
		},
		Features:      Features{NetworkPathMode: "copy"},
		Notifications: Notifications{MentionRetentionDays: 365, OSNotificationsEnabled: true},
		Logging:       Logging{Level: "info"},
	}
}

func Load(path string, explicit bool) (Config, bool, error) {
	cfg := Defaults()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, false, fmt.Errorf("decode config: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cfg, false, errors.New("config must contain one JSON value")
	}
	if cfg.Version != 1 {
		return cfg, false, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	if err := ValidateAIAccounts(cfg.AIAccounts); err != nil {
		return cfg, false, err
	}
	if cfg.Storage.Type != "file" && cfg.Storage.Type != "sqlite" {
		return cfg, false, errors.New("storage.type must be file or sqlite")
	}
	cfg.Server.BasePath, err = NormalizeBasePath(cfg.Server.BasePath)
	if err != nil {
		return cfg, false, err
	}
	if cfg.Server.TLS.Enabled && (cfg.Server.TLS.CertFile == "" || cfg.Server.TLS.KeyFile == "") {
		return cfg, false, errors.New("server.tls.certFile and server.tls.keyFile are required when TLS is enabled")
	}
	for _, file := range []*string{&cfg.Server.TLS.CertFile, &cfg.Server.TLS.KeyFile} {
		if *file != "" && !filepath.IsAbs(*file) {
			*file = filepath.Join(filepath.Dir(path), *file)
		}
	}
	if cfg.Server.ListenAddress == "" {
		cfg.Server.ListenAddress = "127.0.0.1:8080"
	}
	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = "data"
	}
	if cfg.Server.SessionTTL == "" {
		cfg.Server.SessionTTL = "12h"
	}
	if cfg.UI.WorkspaceTitle == "" {
		cfg.UI.WorkspaceTitle = "minihub"
	}
	if cfg.UI.LoginMessage == "" {
		cfg.UI.LoginMessage = "ユーザーIDとパスワードを入力してください。"
	}
	if cfg.Features.NetworkPathMode == "" {
		cfg.Features.NetworkPathMode = "copy"
	}
	if cfg.Notifications.MentionRetentionDays == 0 {
		cfg.Notifications.MentionRetentionDays = 365
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	ttl, err := time.ParseDuration(cfg.Server.SessionTTL)
	if err != nil {
		return cfg, false, fmt.Errorf("invalid server.sessionTTL: %w", err)
	}
	if ttl <= 0 {
		return cfg, false, errors.New("server.sessionTTL must be positive")
	}
	if cfg.Notifications.MentionRetentionDays < 1 {
		return cfg, false, errors.New("notifications.mentionRetentionDays must be positive")
	}
	if cfg.Retention.Days < 0 {
		return cfg, false, errors.New("retention.days must be zero or positive")
	}
	if cfg.Logging.Level != "debug" && cfg.Logging.Level != "info" && cfg.Logging.Level != "warn" && cfg.Logging.Level != "error" {
		return cfg, false, errors.New("logging.level must be debug, info, warn, or error")
	}
	if cfg.Features.NetworkPathMode != "disabled" && cfg.Features.NetworkPathMode != "copy" && cfg.Features.NetworkPathMode != "open-and-copy" {
		return cfg, false, errors.New("features.networkPathMode must be disabled, copy, or open-and-copy")
	}
	secretField := ""
	if cfg.InitialAdmin != nil && cfg.InitialAdmin.Password != "" {
		secretField = "initialAdmin.password"
	}
	for _, a := range cfg.AIAccounts {
		if a.Token != "" {
			secretField = "aiAccounts.token"
			break
		}
	}
	if secretField != "" {
		if runtime.GOOS != "windows" {
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0o077 != 0 {
				return cfg, false, fmt.Errorf("config containing %s must have permissions 0600", secretField)
			}
		}
	}
	if !filepath.IsAbs(cfg.Server.DataDir) {
		cfg.Server.DataDir = filepath.Join(filepath.Dir(path), cfg.Server.DataDir)
	}
	return cfg, true, nil
}

// NormalizeBasePath accepts an absolute URL path, not a URL or an encoded path.
// The empty string and "/" select the legacy deployment at the host root.
func NormalizeBasePath(value string) (string, error) {
	if value == "" || value == "/" {
		return "", nil
	}
	invalid := errors.New("server.basePath must be an absolute path with segments containing only ASCII letters, digits, '.', '_', '~', or '-'; empty segments, '.' and '..' are not allowed")
	if !strings.HasPrefix(value, "/") {
		return "", invalid
	}
	value = strings.TrimSuffix(value, "/")
	for _, segment := range strings.Split(value[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", invalid
		}
		for _, c := range segment {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._~-", c)) {
				return "", invalid
			}
		}
	}
	return value, nil
}
