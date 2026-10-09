// Command cleanroom is an MCP gateway that keeps agents away from untrusted
// content. See README.md.
//
// Usage:
//
//	cleanroom serve [flags]    run the MCP server on stdin and stdout
//
// Every command takes --policy and --state, which default to the
// CLEANROOM_POLICY and CLEANROOM_STATE environment variables.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"cleanroom/internal/config"
	"cleanroom/internal/eventlog"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
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
}

// common holds what every command needs.
type common struct {
	policyPath string
	stateDir   string
	stdout     io.Writer
	stderr     io.Writer
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
	log, err := eventlog.Open(c.stateDir)
	if err != nil {
		return nil, nil, err
	}
	return cfg, log, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(stderr)
		return 2
	}
	for _, cmd := range commands {
		if cmd.name != args[0] {
			continue
		}
		c := &common{stdout: stdout, stderr: stderr}
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
		fmt.Fprintf(w, "  %-12s %s\n", cmd.name, cmd.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run cleanroom <command> -h for a command's flags.")
}
