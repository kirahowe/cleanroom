package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cleanroom/internal/config"
)

// fakeBinDir holds stand-ins for the programs init looks for. Its
// mail-source records its arguments and the password file's content, then
// succeeds or fails as told.
func fakeBinDir(t *testing.T, checkSucceeds bool) string {
	t.Helper()
	dir := t.TempDir()
	outcome := `echo "Logged in. Mailbox is open read-only and holds 3 messages."`
	if !checkSucceeds {
		outcome = `echo "logging in to the mail server: no such user" >&2; exit 1`
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dir + "/args\"\n" +
		"while [ $# -gt 0 ]; do [ \"$1\" = --imap-password-file ] && cat \"$2\" > \"" + dir + "/password-seen\"; shift; done\n" +
		outcome + "\n"
	for name, content := range map[string]string{"mail-source": script, "claude-reader": "#!/bin/sh\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInit(t *testing.T) {
	bin := fakeBinDir(t, true)
	home := t.TempDir()
	policy := filepath.Join(home, "cleanroom", "policy.jsonc")
	passwordFile := filepath.Join(home, "cleanroom", "imap-password")

	out, ok := runCLI(t, true, "kira@example.com\n", "init", "--policy", policy, "--bin-dir", bin)
	if !ok {
		t.Fatal(out)
	}
	for _, want := range []string{"holds 3 messages", policy, "cleanroom claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, testPassword) {
		t.Error("the password was printed")
	}

	// The check ran with the typed password, before the policy existed.
	seen, _ := os.ReadFile(filepath.Join(bin, "password-seen"))
	if string(seen) != testPassword+"\n" {
		t.Errorf("mail-source saw password %q", seen)
	}
	args, _ := os.ReadFile(filepath.Join(bin, "args"))
	if !strings.HasPrefix(string(args), "--check\n--imap-addr\n127.0.0.1:1143\n--imap-tls\nstarttls\n--imap-user\nkira@example.com\n") {
		t.Errorf("mail-source ran with:\n%s", args)
	}

	for _, path := range []string{policy, passwordFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", path, info.Mode().Perm())
		}
	}
	if strings.Contains(readFile(t, policy), testPassword) {
		t.Error("the password is in the policy file")
	}

	// The policy it wrote is one cleanroom accepts, and it runs the same
	// command that was just checked.
	cfg, err := config.Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	mail := cfg.Policy.Upstreams["mail"]
	wantCommand := append([]string{filepath.Join(bin, "mail-source")}, strings.Split(strings.TrimSpace(string(args)), "\n")[1:]...)
	if !slices.Equal(mail.Command, wantCommand) {
		t.Errorf("command = %q\nwant      %q", mail.Command, wantCommand)
	}
	if !mail.AskOnly || !mail.Labels.Contaminated || !mail.Labels.Private || !cfg.Policy.SessionsStartPrivate {
		t.Errorf("the mail upstream is not ask-only, contaminated and private: %+v", mail)
	}
	if mail.Reader[0] != filepath.Join(bin, "claude-reader") {
		t.Errorf("reader = %q", mail.Reader)
	}

	// A second run leaves the policy alone.
	if out, ok := runCLI(t, true, "someone@example.com\n", "init", "--policy", policy, "--bin-dir", bin); ok || !strings.Contains(out, "already exists") {
		t.Errorf("init over an existing policy: %v\n%s", ok, out)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInitWritesNothingWhenTheLoginFails(t *testing.T) {
	bin := fakeBinDir(t, false)
	dir := filepath.Join(t.TempDir(), "cleanroom")
	out, ok := runCLI(t, true, "", "init", "--policy", filepath.Join(dir, "policy.jsonc"), "--bin-dir", bin, "--imap-user", "kira")
	if ok || !strings.Contains(out, "no such user") || !strings.Contains(out, "nothing was written") {
		t.Errorf("got %v:\n%s", ok, out)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestInitRefusals(t *testing.T) {
	bin := fakeBinDir(t, true)
	policy := func() string { return filepath.Join(t.TempDir(), "policy.jsonc") }
	for name, tc := range map[string]struct {
		terminal bool
		stdin    string
		args     []string
		want     string
	}{
		"not a terminal":    {false, "kira\n", []string{"--bin-dir", bin}, "only works at a terminal"},
		"no user name":      {true, "\n", []string{"--bin-dir", bin}, "no user name"},
		"programs missing":  {true, "kira\n", []string{"--bin-dir", t.TempDir()}, "make build"},
		"password argument": {true, "kira\n", []string{"--bin-dir", bin, "--imap-password", "hunter2"}, "flag provided but not defined"},
	} {
		args := append([]string{"init", "--policy", policy()}, tc.args...)
		if out, ok := runCLI(t, tc.terminal, tc.stdin, args...); ok || !strings.Contains(out, tc.want) {
			t.Errorf("%s: %v, want an error mentioning %q:\n%s", name, ok, tc.want, out)
		}
	}
}

// Values go into the policy as JSON strings, so a path or user name with
// quotes or backslashes cannot change the policy's structure.
func TestStarterPolicyQuotesItsValues(t *testing.T) {
	s := starter{
		mailSource: `/tmp/a "b"/mail-source`, reader: `/tmp/c\d/claude-reader`,
		addr: "127.0.0.1:1143", tlsMode: "starttls", user: `"], "ask_only": false, "x": ["`,
		passwordFile: "/p", mailbox: "All Mail",
	}
	path := filepath.Join(t.TempDir(), "policy.jsonc")
	if err := os.WriteFile(path, []byte(starterPolicy(s)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mail := cfg.Policy.Upstreams["mail"]
	if !mail.AskOnly || mail.Command[0] != s.mailSource || mail.Reader[0] != s.reader ||
		!slices.Contains(mail.Command, s.user) || !slices.Contains(mail.Command, "All Mail") {
		t.Errorf("%+v", mail)
	}
}
