// Command cleanroom is an MCP gateway that keeps agents away from untrusted
// content. See README.md.
//
// Usage:
//
//	cleanroom serve [flags]          run the MCP server on stdin and stdout
//	cleanroom reader-tools --ask N   serve one reader's tools (started by cleanroom)
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
//
// Every command takes --policy and --state, which default to the
// CLEANROOM_POLICY and CLEANROOM_STATE environment variables.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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
	os.Exit(run(ctx, os.Args[1:], stdio{in: os.Stdin, out: os.Stdout, err: os.Stderr, terminal: terminal}))
}

// command is one subcommand. It registers its flags on fs and returns the
// function to run once they are parsed.
type command struct {
	name    string
	summary string
	setup   func(fs *flag.FlagSet, c *common) func(ctx context.Context) error
}

var commands = []command{
	{"serve", "run the MCP server on stdin and stdout", serveCommand},
	{"reader-tools", "serve one reader's tools on stdin and stdout (started by cleanroom)", readerToolsCommand},
	{"proposals", "list held calls and where they stand", proposalsCommand},
	{"answers", "list free-text answers delivered to you", answersCommand},
	{"show", "read a held call or an answer: show N (terminal only)", showCommand},
	{"approve", "approve a held call and run it once: approve N (terminal only)", approveCommand},
	{"deny", "deny a held call: deny N", denyCommand},
	{"log", "list every event", logCommand},
	{"explain", "replay the decision made about a call: explain N", explainCommand},
}

// stdio is where a command reads and writes.
type stdio struct {
	in       io.Reader
	out, err io.Writer
	// terminal reports whether a person is at the other end of both in
	// and out.
	terminal bool
}

// common holds what every command needs.
type common struct {
	policyPath string
	stateDir   string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	terminal   bool
}

// open loads the policy and opens the log. The state directory defaults to
// .cleanroom next to the policy file.
func (c *common) open() (*config.Config, *eventlog.Log, error) {
	if c.policyPath == "" {
		return nil, nil, fmt.Errorf("no policy file: pass --policy or set CLEANROOM_POLICY")
	}
	cfg, err := config.Load(c.policyPath)
	if err != nil {
		return nil, nil, err
	}
	if c.stateDir == "" {
		c.stateDir = filepath.Join(cfg.Dir, ".cleanroom")
	}
	// Absolute, because readers are started in another directory and are
	// handed this path.
	if c.stateDir, err = filepath.Abs(c.stateDir); err != nil {
		return nil, nil, err
	}
	events, err := eventlog.Open(c.stateDir)
	if err != nil {
		return nil, nil, err
	}
	return cfg, events, nil
}

func run(ctx context.Context, args []string, io stdio) int {
	stdout, stderr := io.out, io.err
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(stderr)
		return 2
	}
	for _, cmd := range commands {
		if cmd.name != args[0] {
			continue
		}
		c := &common{stdin: io.in, stdout: stdout, stderr: stderr, terminal: io.terminal}
		fs := flag.NewFlagSet("cleanroom "+cmd.name, flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.StringVar(&c.policyPath, "policy", os.Getenv("CLEANROOM_POLICY"), "path to the policy file")
		fs.StringVar(&c.stateDir, "state", os.Getenv("CLEANROOM_STATE"), "state directory (default: .cleanroom next to the policy)")
		runCmd := cmd.setup(fs, c)
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if err := runCmd(ctx); err != nil {
			fmt.Fprintf(stderr, "cleanroom %s: %v\n", cmd.name, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stderr, "cleanroom: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: cleanroom <command> [flags]")
	fmt.Fprintln(w)
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-13s %s\n", cmd.name, cmd.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run cleanroom <command> -h for a command's flags.")
}
