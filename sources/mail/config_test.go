package main

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mailpitURL.String() != "http://127.0.0.1:8025" {
		t.Errorf("%+v", cfg)
	}
}

func TestParseConfigEnvAndFlags(t *testing.T) {
	e := env(map[string]string{"MAILPIT_URL": "http://mp:8025/"})
	cfg, err := parseConfig(nil, e)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mailpitURL.String() != "http://mp:8025" {
		t.Errorf("env: %+v", cfg)
	}
	cfg, err = parseConfig([]string{"--mailpit-url", "http://flag:8025"}, e)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mailpitURL.String() != "http://flag:8025" {
		t.Errorf("flags: %+v", cfg)
	}
}

// The flags that configured sending are gone with the send tool, so a
// policy that still passes them fails at startup.
func TestParseConfigErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--mailpit-url", "ftp://x"},
		{"--mailpit-url", "nonsense"},
		{"--smtp-addr", "127.0.0.1:1025"},
		{"--from", "me@example.com"},
		{"--allow-recipient", "a@example.com"},
		{"--unknown"},
	} {
		if _, err := parseConfig(args, env(nil)); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}
