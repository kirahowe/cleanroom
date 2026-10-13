package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// demoMailpit is where "make inbox" serves the demo inbox.
const demoMailpit = "http://127.0.0.1:18025"

// demoPolicyMark opens a policy that "init --demo" wrote. Such a file holds
// nothing a person typed, so a later init may replace it.
const demoPolicyMark = `// Written by "cleanroom init --demo"`

// starter is what init needs to know to write a first policy: one mail
// upstream with a Claude Code session as its reader. The mail is read over
// IMAP, or from the demo inbox when mailpitURL is set.
type starter struct {
	mailSource   string
	reader       string
	addr         string
	tlsMode      string
	user         string
	passwordFile string
	mailbox      string

	// The demo's policy reads Mailpit and adds the calendar source, so
	// that there is a tool to hold.
	mailpitURL     string
	calendarSource string
}

func (s starter) demo() bool { return s.mailpitURL != "" }

// mailSourceArgs is the mail source's command line, without the program.
func (s starter) mailSourceArgs() []string {
	if s.demo() {
		return []string{"--mailpit-url", s.mailpitURL}
	}
	args := []string{
		"--imap-addr", s.addr, "--imap-tls", s.tlsMode,
		"--imap-user", s.user, "--imap-password-file", s.passwordFile,
	}
	if s.mailbox != "" {
		args = append(args, "--imap-mailbox", s.mailbox)
	}
	return args
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
	// The command, one flag and its value to a line.
	command := []string{quoted(s.mailSource)}
	for pair := range slices.Chunk(s.mailSourceArgs(), 2) {
		command = append(command, quoted(pair...))
	}
	header := `// Written by "cleanroom init". This file is the trusted part of cleanroom's
// configuration. See the README for what each setting does.`
	calendar := ""
	if s.demo() {
		header = demoPolicyMark + ` for the inbox that "make inbox" serves.
// This file is the trusted part of cleanroom's configuration. See the README
// for what each setting does.`
		calendar = `

		// A calendar and reminders, written to local files. Creating an
		// event is acting for the user. Cleanroom discards what these
		// tools return and tells the caller only whether they ran.
		"calendar": {
			"command": [` + quoted(s.calendarSource) + `,
				"--dir", ".cleanroom/calendar"],
			"labels": [],
			"tools": {
				"create_event": {"class": "act", "discard_result": true},
				"create_reminder": {"class": "act", "discard_result": true},
			},
		},`
	}
	return header + `
{
	// The agent holds private data that cleanroom cannot see, so every
	// session counts as private from the start.
	"sessions_start_private": true,

	"upstreams": {
		"mail": {
			"command": [` + strings.Join(command, ",\n\t\t\t\t") + `],
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
		},` + calendar + `
	},
}
`
}

func demoReport(policyPath, flags string) string {
	return "Wrote " + policyPath + "\n\n" +
		"It reads the demo inbox, whose messages are fictional. A Claude Code session\n" +
		"does the reading, so any message there that it reads to answer a question,\n" +
		"including messages you add, is sent to Anthropic.\n\n" +
		"Start the inbox in another terminal and leave it running:\n\n" +
		"    make inbox\n\n" +
		"Then ask it something:\n\n" +
		"    cleanroom ask" + flags + " \"How much did my trip to Toronto cost in total?\"\n" +
		"    cleanroom ask" + flags + " \"When is my family reunion next year?\"\n"
}

// isDemoPolicy reports whether the file at path is one "init --demo" wrote.
func isDemoPolicy(path string) bool {
	content, err := os.ReadFile(path)
	return err == nil && strings.HasPrefix(string(content), demoPolicyMark)
}

func initReport(policyPath, passwordFile, check, flags string) string {
	return check + "\n\n" +
		"Wrote " + policyPath + "\n" +
		"Wrote " + passwordFile + " (only you can read it)\n\n" +
		"Ask your mail something:\n\n" +
		"    cleanroom ask" + flags + " \"When is my next dentist appointment?\"\n" +
		"    cleanroom ask" + flags + " --returns date \"When is my next dentist appointment?\"\n"
}

// initCommand sets cleanroom up for one IMAP mailbox. It asks for the user
// name and the password, tests them against the server, and only then
// writes a policy.
//
// It only works at a terminal. The password is typed by the person and is
// never an argument, so it reaches neither an agent's context nor the
// process list.
//
// With --demo it writes a policy for the demo inbox. That asks nothing and
// has no secret, so it runs anywhere.
func initCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	s := starter{}
	fs.StringVar(&s.addr, "imap-addr", "127.0.0.1:1143", "IMAP server as host:port; the default is Proton Mail Bridge's")
	fs.StringVar(&s.tlsMode, "imap-tls", "starttls", "starttls, or implicit for TLS from the first byte (usually port 993)")
	fs.StringVar(&s.user, "imap-user", "", "IMAP user name; asked for when not given")
	fs.StringVar(&s.mailbox, "imap-mailbox", "", "the mailbox to read (default: the server's all-mail mailbox)")
	demo := fs.Bool("demo", false, "set up the fictional inbox that make inbox serves, in place of a mailbox of your own")
	binDir := fs.String("bin-dir", "", "directory holding mail-source and claude-reader (default: beside this program)")
	return func(ctx context.Context) error {
		if !*demo && !c.terminal {
			return errNeedsTerminal
		}
		policyPath, err := c.resolvedPolicyPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(policyPath); err == nil && !isDemoPolicy(policyPath) {
			return fmt.Errorf("%s already exists; edit it, or remove it to start again", policyPath)
		}
		if *demo {
			programs, err := siblings(*binDir, "mail-source", "claude-reader", "calendar-source")
			if err != nil {
				return err
			}
			demoStarter := starter{mailSource: programs[0], reader: programs[1], calendarSource: programs[2], mailpitURL: demoMailpit}
			if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
				return err
			}
			if err := writePrivate(policyPath, starterPolicy(demoStarter)); err != nil {
				return err
			}
			fmt.Fprint(c.stdout, demoReport(policyPath, c.hintFlags()))
			return nil
		}
		programs, err := siblings(*binDir, "mail-source", "claude-reader")
		if err != nil {
			return err
		}
		s.mailSource, s.reader = programs[0], programs[1]

		fmt.Fprint(c.stdout, "Cleanroom uses claude to answer questions about your mail. The email content\n"+
			"it reads, including headers and message text, is sent to Anthropic in the\n"+
			"process.\n\nProceed? [y/N]: ")
		consent, err := c.input.ReadLine()
		if err != nil {
			return err
		}
		if !slices.Contains([]string{"y", "yes"}, strings.ToLower(strings.TrimSpace(consent))) {
			return errors.New("setup cancelled; mail access was not enabled and nothing was written")
		}
		if s.user == "" {
			fmt.Fprintf(c.stdout, "IMAP user name for %s: ", s.addr)
			line, err := c.input.ReadLine()
			if err != nil {
				return err
			}
			if s.user = strings.TrimSpace(line); s.user == "" {
				return errors.New("no user name given")
			}
		}
		fmt.Fprint(c.stdout, "IMAP password (not shown): ")
		password, err := c.input.ReadPassword()
		fmt.Fprintln(c.stdout)
		if err != nil {
			return err
		}
		if password == "" {
			return errors.New("no password given")
		}

		dir := filepath.Dir(policyPath)
		_, statErr := os.Stat(dir)
		madeDir := errors.Is(statErr, os.ErrNotExist)
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
			if madeDir {
				os.Remove(dir)
			}
			return fmt.Errorf("could not open the mailbox, so nothing was written:\n%s", strings.TrimSpace(string(check)))
		}
		if err := writePrivate(policyPath, starterPolicy(s)); err != nil {
			return err
		}
		fmt.Fprint(c.stdout, initReport(policyPath, s.passwordFile, strings.TrimSpace(string(check)), c.hintFlags()))
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

// siblings finds the named programs a starter policy runs. They are built
// into the same directory as cleanroom.
func siblings(dir string, names ...string) ([]string, error) {
	if dir == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		if self, err = filepath.EvalSymlinks(self); err != nil {
			return nil, err
		}
		dir = filepath.Dir(self)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = filepath.Join(dir, name)
		if _, err := os.Stat(paths[i]); err != nil {
			return nil, fmt.Errorf("%s is missing; build it with make build", paths[i])
		}
	}
	return paths, nil
}
