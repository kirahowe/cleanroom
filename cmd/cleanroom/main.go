// Command cleanroom is an MCP gateway that keeps agents away from untrusted
// content. See README.md.
//
// Usage:
//
//	cleanroom serve [flags]          run the MCP server on stdin and stdout
//	cleanroom reader-tools SOCKET    connect a reader to its tools (started by cleanroom)
//
// For the person:
//
//	cleanroom proposals    list held calls
//	cleanroom answers      list free-text answers
//	cleanroom show N       read a held call or an answer (terminal only)
//	cleanroom approve N    approve a held call and run it once (terminal only)
//	cleanroom deny N       deny a held call
//	cleanroom log          list every event
//	cleanroom explain N    replay the decision made about call N
//	cleanroom repair       set aside a last log line that a crash cut off
//
// Every command takes --policy and --state. Without --policy the policy is
// read from ~/.config/cleanroom/policy.jsonc, and without --state the state
// directory is .cleanroom next to the policy. Nothing is read from the
// environment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/term"

	"cleanroom/internal/config"
	"cleanroom/internal/eventlog"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("cleanroom: ")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	terminal := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	os.Exit(run(ctx, os.Args[1:], stdio{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, terminal: terminal}))
}

type command struct {
	name    string
	summary string
	// setup registers the command's flags on fs and returns the function
	// to run once they are parsed.
	setup func(fs *flag.FlagSet, c *common) func(ctx context.Context) error
}

var commands = []command{
	{"serve", "run the MCP server on stdin and stdout", serveCommand},
	{"reader-tools", "connect a reader to its tools: reader-tools SOCKET (started by cleanroom)", readerToolsCommand},
	{"proposals", "list held calls and where they stand", proposalsCommand},
	{"answers", "list free-text answers delivered to you", answersCommand},
	{"show", "read a held call or an answer: show N (terminal only)", showCommand},
	{"approve", "approve a held call and run it once: approve N (terminal only)", approveCommand},
	{"deny", "deny a held call: deny N", denyCommand},
	{"log", "list every event", logCommand},
	{"explain", "replay the decision made about a call: explain N", explainCommand},
	{"repair", "set aside a last log line that a crash cut off", repairCommand},
}

type stdio struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// terminal reports whether a person is at the other end of both
	// stdin and stdout.
	terminal bool
}

type common struct {
	stdio
	policyPath string
	stateDir   string
}

type state struct {
	cfg *config.Config
	dir string // absolute
	log *eventlog.Log
}

// defaultPolicyPath is where the policy is looked for when --policy is not
// given: ~/.config/cleanroom/policy.jsonc.
func defaultPolicyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cleanroom", "policy.jsonc"), nil
}

// open loads the policy and opens the log. The state directory defaults to
// .cleanroom next to the policy file.
func (c *common) open() (*state, error) {
	path := c.policyPath
	if path == "" {
		var err error
		if path, err = defaultPolicyPath(); err != nil {
			return nil, err
		}
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("no policy file: pass --policy, or put one at %s", path)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	dir := c.stateDir
	if dir == "" {
		dir = filepath.Join(cfg.Dir, ".cleanroom")
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, err
	}
	elog, err := eventlog.Open(dir)
	if err != nil {
		return nil, err
	}
	return &state{cfg: cfg, dir: dir, log: elog}, nil
}

const childLogName = "children.log"

// childLog opens the file that collects the stderr of the processes
// cleanroom starts. It does not go to cleanroom's own stderr: a reader's
// stderr is written by a reader, and some MCP clients show a server's
// stderr to the model.
func (s *state) childLog() (*os.File, error) {
	return os.OpenFile(filepath.Join(s.dir, childLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

func lookup(name string) (command, bool) {
	i := slices.IndexFunc(commands, func(c command) bool { return c.name == name })
	if i < 0 {
		return command{}, false
	}
	return commands[i], true
}

func usageText() string {
	var b strings.Builder
	b.WriteString("usage: cleanroom <command> [flags]\n\n")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "  %-13s %s\n", cmd.name, cmd.summary)
	}
	b.WriteString("\nRun cleanroom <command> -h for a command's flags.\n")
	return b.String()
}

// run is the only place here that writes: it picks the command, runs it,
// and turns the outcome into output and an exit code.
func run(ctx context.Context, args []string, sio stdio) int {
	if len(args) == 0 {
		fmt.Fprint(sio.stderr, usageText())
		return 2
	}
	if slices.Contains([]string{"-h", "--help", "help"}, args[0]) {
		fmt.Fprint(sio.stdout, usageText())
		return 0
	}
	cmd, ok := lookup(args[0])
	if !ok {
		fmt.Fprintf(sio.stderr, "cleanroom: unknown command %q\n\n%s", args[0], usageText())
		return 2
	}

	c := &common{stdio: sio}
	fs := flag.NewFlagSet("cleanroom "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(sio.stderr)
	fs.StringVar(&c.policyPath, "policy", "", "path to the policy file (default: ~/.config/cleanroom/policy.jsonc)")
	fs.StringVar(&c.stateDir, "state", "", "state directory (default: .cleanroom next to the policy)")
	runCmd := cmd.setup(fs, c)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := runCmd(ctx); err != nil {
		fmt.Fprintf(sio.stderr, "cleanroom %s: %v\n", cmd.name, err)
		return 1
	}
	return 0
}
