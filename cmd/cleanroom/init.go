package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// starter is what init needs to know to write a first policy: one mail
// upstream read over IMAP, with a Claude Code session as its reader.
type starter struct {
	mailSource   string
	reader       string
	addr         string
	tlsMode      string
	user         string
	passwordFile string
	mailbox      string
}

// mailSourceArgs is the mail source's command line, without the program.
func (s starter) mailSourceArgs() []string {
	return []string{
		"--imap-addr", s.addr, "--imap-tls", s.tlsMode,
		"--imap-user", s.user, "--imap-password-file", s.passwordFile,
		"--imap-mailbox", s.mailbox,
	}
}

// starterPolicy is the text of the policy file. It is written as text and
// not marshalled, so that the file a person later edits has comments.
func starterPolicy(s starter) string {
	quoted := func(values ...string) string {
		parts := make([]string, len(values))
		for i, v := range values {
			b, _ := json.Marshal(v)
			parts[i] = string(b)
		}
		return strings.Join(parts, ", ")
	}
	args := s.mailSourceArgs()
	return `// Written by "cleanroom init". This file is the trusted part of cleanroom's
// configuration. See the README for what each setting does.
{
	// The agent holds private data that cleanroom cannot see, so every
	// session counts as private from the start.
	"sessions_start_private": true,

	"upstreams": {
		"mail": {
			"command": [` + quoted(s.mailSource) + `,
				` + quoted(args[0:4]...) + `,
				` + quoted(args[4:8]...) + `,
				` + quoted(args[8:]...) + `],
			"labels": ["contaminated", "private"],

			// Agents cannot read mail directly. They ask, and a reader reads.
			"ask_only": true,

			// A headless Claude Code session reads the mail. What it reads
			// is sent to Anthropic.
			"reader": [` + quoted(s.reader) + `,
				"--mcp-config", "{mcp_config}",
				"--question", "{question}",
				"--returns", "{returns}"],

			"tools": {
				"search": {"class": "read"},
				"read": {"class": "read"},
			},
		},
	},
}
`
}

func initReport(policyPath, passwordFile, check string) string {
	return check + "\n\n" +
		"Wrote " + policyPath + "\n" +
		"Wrote " + passwordFile + " (only you can read it)\n\n" +
		"Start a conversation that can ask about your mail:\n\n" +
		"    cleanroom claude\n"
}

// initCommand sets cleanroom up for one IMAP mailbox. It asks for the user
// name and the password, tests them against the server, and only then
// writes a policy.
//
// It only works at a terminal. The password is typed by the person and is
// never an argument, so it reaches neither an agent's context nor the
// process list.
func initCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	s := starter{}
	fs.StringVar(&s.addr, "imap-addr", "127.0.0.1:1143", "IMAP server as host:port; the default is Proton Mail Bridge's")
	fs.StringVar(&s.tlsMode, "imap-tls", "starttls", "starttls, or implicit for TLS from the first byte (usually port 993)")
	fs.StringVar(&s.user, "imap-user", "", "IMAP user name; asked for when not given")
	fs.StringVar(&s.mailbox, "imap-mailbox", "INBOX", "the mailbox to read")
	binDir := fs.String("bin-dir", "", "directory holding mail-source and claude-reader (default: beside this program)")
	return func(ctx context.Context) error {
		if !c.terminal {
			return errNeedsTerminal
		}
		policyPath, err := c.resolvedPolicyPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(policyPath); err == nil {
			return fmt.Errorf("%s already exists; edit it, or remove it to start again", policyPath)
		}
		if s.mailSource, s.reader, err = siblings(*binDir); err != nil {
			return err
		}

		in := bufio.NewReader(c.stdin)
		if s.user == "" {
			fmt.Fprintf(c.stdout, "IMAP user name for %s: ", s.addr)
			line, _ := in.ReadString('\n')
			if s.user = strings.TrimSpace(line); s.user == "" {
				return errors.New("no user name given")
			}
		}
		fmt.Fprint(c.stdout, "IMAP password (not shown): ")
		password, err := c.readPassword()
		fmt.Fprintln(c.stdout)
		if err != nil {
			return err
		}
		if password == "" {
			return errors.New("no password given")
		}

		dir := filepath.Dir(policyPath)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		s.passwordFile = filepath.Join(dir, "imap-password")
		if err := writePrivate(s.passwordFile, password+"\n"); err != nil {
			return err
		}
		check, err := exec.CommandContext(ctx, s.mailSource, append([]string{"--check"}, s.mailSourceArgs()...)...).CombinedOutput()
		if err != nil {
			os.Remove(s.passwordFile)
			return fmt.Errorf("could not open the mailbox, so nothing was written:\n%s", strings.TrimSpace(string(check)))
		}
		if err := writePrivate(policyPath, starterPolicy(s)); err != nil {
			return err
		}
		fmt.Fprint(c.stdout, initReport(policyPath, s.passwordFile, strings.TrimSpace(string(check))))
		return nil
	}
}

// writePrivate writes a file that only its owner can read, replacing any
// file already there.
func writePrivate(path, content string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// siblings finds the programs a starter policy runs. They are built into
// the same directory as cleanroom.
func siblings(dir string) (mailSource, reader string, err error) {
	if dir == "" {
		self, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		if self, err = filepath.EvalSymlinks(self); err != nil {
			return "", "", err
		}
		dir = filepath.Dir(self)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return "", "", err
	}
	mailSource, reader = filepath.Join(dir, "mail-source"), filepath.Join(dir, "claude-reader")
	for _, path := range []string{mailSource, reader} {
		if _, err := os.Stat(path); err != nil {
			return "", "", fmt.Errorf("%s is missing; build it with make build", path)
		}
	}
	return mailSource, reader, nil
}
