package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/config"
	"cleanroom/internal/gateway"
	"cleanroom/internal/reader"
	"cleanroom/internal/upstream"
)

// sessionEnv names the environment variable that gives serve its session
// id when --session is absent. "cleanroom claude" uses it to keep the id
// out of the server's command line, so that the command is the same for
// every conversation and a user can allow exactly that command in Claude
// Code's settings.
const sessionEnv = "CLEANROOM_SESSION"

// serveArgv is the command line of the MCP server as an MCP client is told
// to start it. policyFlag and stateDir are empty for the defaults.
func serveArgv(self, policyFlag, stateDir string) []string {
	argv := []string{self, "serve"}
	if policyFlag != "" {
		argv = append(argv, "--policy", policyFlag)
	}
	if stateDir != "" {
		argv = append(argv, "--state", stateDir)
	}
	return argv
}

func serveCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	session := fs.String("session", "",
		"id of the conversation this server belongs to; reuse it when the conversation is resumed")
	return func(ctx context.Context) error {
		if *session == "" {
			*session = os.Getenv(sessionEnv)
		}
		if *session == "" {
			*session = newSessionID()
			fmt.Fprintf(c.stderr, "cleanroom: no --session given, using %s. "+
				"If this conversation is resumed in a new process it will start clean; pass --session to prevent that.\n", *session)
		}
		g, closeGateway, err := c.gateway(*session)
		if err != nil {
			return err
		}
		defer closeGateway()
		server, err := g.Server(ctx)
		if err != nil {
			return err
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}
}

// gateway builds the gateway for one session from the policy, with its
// upstream connections and readers. The returned function releases them.
func (c *common) gateway(session string) (*gateway.Gateway, func(), error) {
	st, err := c.open()
	if err != nil {
		return nil, nil, err
	}
	cfg := st.cfg
	children, err := st.childLog()
	if err != nil {
		return nil, nil, err
	}
	readers, err := commandReaders(cfg, log.New(children, "", log.LstdFlags).Printf)
	if err != nil {
		children.Close()
		return nil, nil, err
	}
	upstreams := upstream.New(cfg.Policy, upstream.CommandDialer(cfg.Dir, children))
	g := &gateway.Gateway{
		Policy: cfg.Policy, Log: st.log, Session: session,
		Upstreams: upstreams, Readers: readers,
		Flags: c.hintFlags(),
		Logf:  log.Printf,
	}
	return g, func() { upstreams.Close(); children.Close() }, nil
}

// commandReaders builds the reader for each source that has one in the
// policy. Each reader reaches its source through this same binary, run as
// "cleanroom reader-tools", which is a pipe back to this process.
func commandReaders(cfg *config.Config, logf func(string, ...any)) (map[string]reader.Reader, error) {
	self, err := selfPath()
	if err != nil {
		return nil, err
	}
	readers := map[string]reader.Reader{}
	for name, up := range cfg.Policy.Upstreams {
		if len(up.Reader) == 0 {
			continue
		}
		readers[name] = &reader.Command{
			Argv: up.Reader,
			Dir:  cfg.Dir,
			Pipe: []string{self, "reader-tools"},
			Logf: logf,
		}
	}
	return readers, nil
}

// selfPath is the path of this program with symlinks resolved. Commands
// that others start are written with it, so that they read the same
// whether cleanroom was run by its own path or through a link.
func selfPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(self)
}

func newSessionID() string {
	b := make([]byte, 6)
	rand.Read(b) // never fails
	return "s-" + hex.EncodeToString(b)
}
