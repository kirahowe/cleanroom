package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cleanroom/internal/core"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := write(t, `{
		// Comments and trailing commas are allowed.
		"upstreams": {
			"mail": {
				"command": ["bin/mail-source"],
				"labels": ["contaminated", "private"],
				"ask_only": true,
				"reader": ["bin/reader", "{mcp_config}"],
				"tools": {
					"send": {"class": "act", "labels": []},
				},
			},
		},
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mail := cfg.Policy.Upstreams["mail"]
	if mail.Labels != (core.Labels{Contaminated: true, Private: true}) || !mail.AskOnly {
		t.Errorf("mail = %+v", mail)
	}
	if send := mail.Tools["send"]; send.Class != core.Act || send.Labels == nil || *send.Labels != (core.Labels{}) {
		t.Errorf("send = %+v", send)
	}
	if cfg.Dir != filepath.Dir(path) {
		t.Errorf("Dir = %q, want the policy's directory", cfg.Dir)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := map[string]string{
		"misspelt key":   `{"upstreams": {"mail": {"command": ["x"], "lables": ["private"]}}}`,
		"misspelt label": `{"upstreams": {"mail": {"command": ["x"], "labels": ["privat"]}}}`,
		"unknown class":  `{"upstreams": {"mail": {"command": ["x"], "tools": {"send": {"class": "safe"}}}}}`,
		"no upstreams":   `{}`,
		"not json":       `upstreams: mail`,
	}
	for name, content := range tests {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: loaded without error", name)
		} else if !strings.Contains(err.Error(), "policy.jsonc") {
			t.Errorf("%s: error %q does not name the file", name, err)
		}
	}
}
