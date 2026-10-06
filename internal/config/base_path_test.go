package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBasePathConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		invalid     bool
	}{
		{input: ""}, {input: "/"},
		{input: "/hub", want: "/hub"},
		{input: "/hub/", want: "/hub"},
		{input: "/apps/team-chat.v1_~", want: "/apps/team-chat.v1_~"},
		{input: "hub", invalid: true}, {input: "https://host/hub", invalid: true},
		{input: "//host", invalid: true}, {input: "/hub//", invalid: true},
		{input: "/hub//team", invalid: true}, {input: "/hub/../team", invalid: true},
		{input: "/hub/.", invalid: true}, {input: "/hub?x=1", invalid: true},
		{input: "/hub#fragment", invalid: true}, {input: "/hub%2Fteam", invalid: true},
		{input: "/hub\\team", invalid: true}, {input: "/hub\n", invalid: true},
		{input: "/社内", invalid: true}, {input: "/hub team", invalid: true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"server": map[string]string{"basePath": tc.input}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "minihub.json")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, loaded, err := Load(path, true)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid base path accepted")
				}
				return
			}
			if err != nil || !loaded || cfg.Server.BasePath != tc.want {
				t.Fatalf("loaded=%v basePath=%q error=%v", loaded, cfg.Server.BasePath, err)
			}
		})
	}
}
