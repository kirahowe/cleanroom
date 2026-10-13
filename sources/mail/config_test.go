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

func TestParseConfigIMAP(t *testing.T) {
	cfg, err := parseConfig([]string{
		"--imap-addr", "127.0.0.1:1143", "--imap-tls", "starttls",
		"--imap-user", "kira@example.com", "--imap-password-file", "/secrets/imap",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := imapOptions{
		addr: "127.0.0.1:1143", host: "127.0.0.1", startTLS: true,
		user: "kira@example.com", passwordFile: "/secrets/imap",
	}
	if cfg.imap == nil || *cfg.imap != want || cfg.check {
		t.Errorf("got %+v", cfg.imap)
	}

	cfg, err = parseConfig([]string{
		"--imap-addr", "imap.example.com:993", "--imap-user", "kira", "--imap-password-file", "p",
		"--imap-mailbox", "Archive", "--imap-ca-file", "ca.pem", "--check",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o := cfg.imap; o.startTLS || o.host != "imap.example.com" || o.mailbox != "Archive" || o.caFile != "ca.pem" || !cfg.check {
		t.Errorf("got %+v, check %v", *o, cfg.check)
	}

	if cfg, err := parseConfig(nil); err != nil || cfg.imap != nil {
		t.Errorf("without --imap-addr the back end should be Mailpit: %+v, %v", cfg.imap, err)
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
		{"stray"},
		{"--check"},
		{"--imap-user", "kira"},
		{"--imap-addr", "host:993"},
		{"--imap-addr", "host:993", "--imap-user", "kira"},
		{"--imap-addr", "host", "--imap-user", "kira", "--imap-password-file", "p"},
		{"--imap-addr", ":993", "--imap-user", "kira", "--imap-password-file", "p"},
		{"--imap-addr", "host:993", "--imap-user", "kira", "--imap-password-file", "p", "--imap-tls", "none"},
		{"--imap-addr", "host:993", "--imap-user", "kira", "--imap-password-file", "p", "--mailpit-url", "http://127.0.0.1:8025"},
		// There is no flag that takes the password itself.
		{"--imap-addr", "host:993", "--imap-user", "kira", "--imap-password", "hunter2"},
	} {
		if _, err := parseConfig(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}
