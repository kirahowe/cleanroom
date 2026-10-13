package main

import "testing"

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mailpitURL.String() != "http://127.0.0.1:8025" {
		t.Errorf("default: %+v", cfg)
	}
	cfg, err = parseConfig([]string{"--mailpit-url", "http://flag:8025/"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mailpitURL.String() != "http://flag:8025" {
		t.Errorf("flag: %+v", cfg)
	}
}

// The environment is not read, so a stray MAILPIT_URL cannot point the
// server at another mailbox.
func TestParseConfigIgnoresTheEnvironment(t *testing.T) {
	t.Setenv("MAILPIT_URL", "http://elsewhere:8025")
	cfg, err := parseConfig(nil)
	if err != nil || cfg.mailpitURL.String() != "http://127.0.0.1:8025" {
		t.Errorf("%+v, %v", cfg, err)
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
		if _, err := parseConfig(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}
