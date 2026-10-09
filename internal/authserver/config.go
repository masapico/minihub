package authserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/oidc"
)

type Config struct {
	Version      int                  `json:"version"`
	Server       config.Server        `json:"server"`
	Storage      config.Storage       `json:"storage"`
	UI           config.UI            `json:"ui"`
	OIDC         OIDCConfig           `json:"oidc"`
	Clients      []oidc.Client        `json:"clients,omitempty"`
	InitialAdmin *config.InitialAdmin `json:"initialAdmin,omitempty"`
}

type OIDCConfig struct {
	Issuer       string `json:"issuer"`
	JWTSecret    string `json:"jwtSecret,omitempty"`
	ServiceToken string `json:"serviceToken,omitempty"` // Token for minihub Directory API sync
}

const DefaultConfigFilename = "miniauth.json"

func Defaults() Config {
	return Config{
		Version: 1,
		Server: config.Server{
			ListenAddress: "127.0.0.1:8090",
			DataDir:       "auth_data",
			SessionTTL:    "24h",
		},
		Storage: config.Storage{Type: "file"},
		UI: config.UI{
			WorkspaceTitle: "社内ポータル & 認証",
			LoginMessage:   "社内共通アカウントでログインしてください。",
		},
		OIDC: OIDCConfig{
			Issuer: "http://127.0.0.1:8090",
		},
	}
}

func LoadConfig(path string, explicit bool) (Config, bool, error) {
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
	if cfg.Storage.Type != "file" && cfg.Storage.Type != "sqlite" {
		return cfg, false, errors.New("storage.type must be file or sqlite")
	}

	cfg.Server.BasePath, err = config.NormalizeBasePath(cfg.Server.BasePath)
	if err != nil {
		return cfg, false, err
	}

	if cfg.Server.ListenAddress == "" {
		cfg.Server.ListenAddress = "127.0.0.1:8090"
	}
	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = "auth_data"
	}
	if cfg.Server.SessionTTL == "" {
		cfg.Server.SessionTTL = "24h"
	}
	if cfg.UI.WorkspaceTitle == "" {
		cfg.UI.WorkspaceTitle = "社内ポータル & 認証"
	}
	if cfg.OIDC.Issuer == "" {
		cfg.OIDC.Issuer = "http://" + cfg.Server.ListenAddress
	}
	cfg.OIDC.Issuer = strings.TrimSuffix(cfg.OIDC.Issuer, "/")

	_, err = time.ParseDuration(cfg.Server.SessionTTL)
	if err != nil {
		return cfg, false, fmt.Errorf("invalid server.sessionTTL: %w", err)
	}

	if !filepath.IsAbs(cfg.Server.DataDir) {
		cfg.Server.DataDir = filepath.Join(filepath.Dir(path), cfg.Server.DataDir)
	}

	return cfg, true, nil
}

