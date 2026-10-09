package main

import (
	"reflect"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mailpitURL.String() != "http://127.0.0.1:8025" || cfg.smtpAddr != "127.0.0.1:1025" ||
		cfg.from.Address != "assistant@cleanroom.test" || len(cfg.allowed) != 0 {
		t.Errorf("%+v", cfg)
	}
}

func TestParseConfigEnvAndFlags(t *testing.T) {
	e := env(map[string]string{
		"MAILPIT_URL": "http://mp:8025/", "MAILPIT_SMTP": "mp:1025", "MAIL_FROM": "me@example.com",
		"MAIL_ALLOWED_RECIPIENTS": "a@example.com, b@example.com",
	})
	cfg, err := parseConfig(nil, e)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mailpitURL.String() != "http://mp:8025" || cfg.smtpAddr != "mp:1025" || cfg.from.Address != "me@example.com" ||
		!reflect.DeepEqual(cfg.allowed, []string{"a@example.com", "b@example.com"}) {
		t.Errorf("env: %+v", cfg)
	}
	cfg, err = parseConfig([]string{"--from", "f@example.com", "--allow-recipient", "x@example.com", "--allow-recipient", "y@example.com"}, e)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.from.Address != "f@example.com" || !reflect.DeepEqual(cfg.allowed, []string{"x@example.com", "y@example.com"}) {
		t.Errorf("flags: %+v", cfg)
	}
}

func TestParseConfigErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--mailpit-url", "ftp://x"},
		{"--mailpit-url", "nonsense"},
		{"--smtp-addr", "no-port"},
		{"--from", "bad"},
		{"--allow-recipient", "bad"},
		{"--unknown"},
	} {
		if _, err := parseConfig(args, env(nil)); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}
