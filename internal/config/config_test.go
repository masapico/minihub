package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStorageSelection(t *testing.T) {
	for _, kind := range []string{"file", "sqlite", "", "mysql"} {
		t.Run(fmt.Sprintf("type_%s", kind), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"version":1,"storage":{"type":%q}}`, kind)), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := Load(path, true)
			if kind == "file" || kind == "sqlite" {
				if err != nil || cfg.Storage.Type != kind {
					t.Fatal(cfg, err)
				}
			} else if err == nil {
				t.Fatal("invalid type accepted")
			}
		})
	}
	if Defaults().Storage.Type != "file" {
		t.Fatal("legacy default changed")
	}
}

func TestMissingDefaultConfigUsesDefaults(t *testing.T) {
	cfg, loaded, err := Load(filepath.Join(t.TempDir(), "minihub.json"), false)
	if err != nil || loaded || cfg.Server.ListenAddress != "127.0.0.1:8080" || cfg.UI.WorkspaceTitle != "minihub" || cfg.UI.LoginMessage == "" || cfg.Features.SelfPasswordChange || cfg.Features.NetworkPathMode != "copy" {
		t.Fatalf("loaded=%v config=%+v error=%v", loaded, cfg, err)
	}
}

func TestTLSAndOSNotificationConfig(t *testing.T) {
	dir := t.TempDir()
	absolute := filepath.Join(dir, "absolute.crt")
	for _, tc := range []struct {
		name, body                  string
		invalid, tls, notifications bool
		cert, key                   string
	}{
		{name: "legacy", body: `{"version":1}`, notifications: true},
		{name: "enabled", body: `{"server":{"tls":{"enabled":true,"certFile":"certs/server.crt","keyFile":"certs/server.key"}}}`, tls: true, notifications: true, cert: filepath.Join(dir, "certs", "server.crt"), key: filepath.Join(dir, "certs", "server.key")},
		{name: "absolute", body: fmt.Sprintf(`{"server":{"tls":{"enabled":true,"certFile":%q,"keyFile":%q}}}`, absolute, absolute), tls: true, notifications: true, cert: absolute, key: absolute},
		{name: "disabled", body: `{"server":{"tls":{"enabled":false}},"notifications":{"osNotificationsEnabled":false}}`},
		{name: "explicit notification permission", body: `{"notifications":{"osNotificationsEnabled":true}}`, notifications: true},
		{name: "missing pair", body: `{"server":{"tls":{"enabled":true}}}`, invalid: true},
		{name: "missing key", body: `{"server":{"tls":{"enabled":true,"certFile":"server.crt"}}}`, invalid: true},
		{name: "missing cert", body: `{"server":{"tls":{"enabled":true,"keyFile":"server.key"}}}`, invalid: true},
		{name: "invalid notification type", body: `{"notifications":{"osNotificationsEnabled":"false"}}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "minihub.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := Load(path, true)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid config accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Server.TLS.Enabled != tc.tls || cfg.Server.TLS.CertFile != tc.cert || cfg.Server.TLS.KeyFile != tc.key || cfg.Notifications.OSNotificationsEnabled != tc.notifications {
				t.Fatalf("unexpected config: %+v", cfg)
			}
		})
	}
	if !Defaults().Notifications.OSNotificationsEnabled || Defaults().Server.TLS.Enabled {
		t.Fatal("legacy defaults changed")
	}
}

func TestLoadConfigAndResolveDataDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "minihub.json")
	body := `{"version":1,"server":{"listenAddress":":9090","dataDir":"chat-data","sessionTTL":"24h"},"ui":{"workspaceTitle":"社内チャット","loginMessage":"会社のアカウントでログインしてください。"},"features":{"selfPasswordChange":true,"networkPathMode":"open-and-copy"},"notifications":{"mentionRetentionDays":30},"logging":{"level":"debug"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, loaded, err := Load(path, true)
	if err != nil || !loaded {
		t.Fatalf("loaded=%v error=%v", loaded, err)
	}
	if cfg.Server.DataDir != filepath.Join(dir, "chat-data") || cfg.UI.WorkspaceTitle != "社内チャット" || cfg.UI.LoginMessage != "会社のアカウントでログインしてください。" || !cfg.Features.SelfPasswordChange || cfg.Features.NetworkPathMode != "open-and-copy" {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestNetworkPathModeValidation(t *testing.T) {
	for _, mode := range []string{"disabled", "copy", "open-and-copy"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "minihub.json")
			body := `{"version":1,"features":{"networkPathMode":"` + mode + `"}}`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := Load(path, true)
			if err != nil || cfg.Features.NetworkPathMode != mode {
				t.Fatalf("mode=%q config=%+v error=%v", mode, cfg, err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "minihub.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"features":{"networkPathMode":"automatic"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path, true); err == nil {
		t.Fatal("unknown network path mode accepted")
	}
}

func TestRetentionDaysValidation(t *testing.T) {
	for _, body := range []string{`{"version":1,"retention":{"days":0}}`, `{"version":1,"retention":{"days":365}}`} {
		path := filepath.Join(t.TempDir(), "minihub.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path, true); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "minihub.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"retention":{"days":-1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path, true); err == nil {
		t.Fatal("negative retention days accepted")
	}
}

func TestPlainCredentialsRequirePrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not enforced on Windows")
	}
	for _, body := range []string{
		`{"version":1,"initialAdmin":{"id":"admin","name":"Admin","password":"password"}}`,
		`{"aiAccounts":[{"id":"helper","name":"Helper","url":"http://localhost/ai","token":"direct-test-token"}]}`,
	} {
		path := filepath.Join(t.TempDir(), "minihub.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path, true); err == nil {
			t.Fatal("insecure config permissions accepted")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path, true); err != nil {
			t.Fatal(err)
		}
	}
}
