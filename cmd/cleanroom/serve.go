package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/config"
	"cleanroom/internal/gateway"
	"cleanroom/internal/reader"
	"cleanroom/internal/upstream"
)

func serveCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	session := fs.String("session", "",
		"id of the conversation this server belongs to; reuse it when the conversation is resumed")
	return func(ctx context.Context) error {
		st, err := c.open()
		if err != nil {
			return err
		}
		cfg := st.cfg
		children, err := st.childLog()
		if err != nil {
			return err
		}
		defer children.Close()
		if *session == "" {
			*session = newSessionID()
			fmt.Fprintf(c.stderr, "cleanroom: no --session given, using %s. "+
				"If this conversation is resumed in a new process it will start clean; pass --session to prevent that.\n", *session)
		}
		upstreams := upstream.New(cfg.Policy, upstream.CommandDialer(cfg.Dir, children))
		defer upstreams.Close()
		readers, err := commandReaders(cfg, log.New(children, "", log.LstdFlags).Printf)
		if err != nil {
			return err
		}
		g := &gateway.Gateway{
			Policy: cfg.Policy, Log: st.log, Session: *session,
			Upstreams: upstreams, Readers: readers,
			Logf: log.Printf,
		}
		server, err := g.Server(ctx)
		if err != nil {
			return err
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}
}

// commandReaders builds the reader for each source that has one in the
// policy. Each reader reaches its source through this same binary, run as
// "cleanroom reader-tools", which is a pipe back to this process.
func commandReaders(cfg *config.Config, logf func(string, ...any)) (map[string]reader.Reader, error) {
	self, err := os.Executable()
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

func newSessionID() string {
	b := make([]byte, 6)
	rand.Read(b) // never fails
	return "s-" + hex.EncodeToString(b)
}
