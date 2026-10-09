package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/gateway"
	"cleanroom/internal/upstream"
)

func serveCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	session := fs.String("session", os.Getenv("CLEANROOM_SESSION"),
		"id of the conversation this server belongs to; reuse it when the conversation is resumed")
	return func(ctx context.Context) error {
		cfg, log, err := c.open()
		if err != nil {
			return err
		}
		if *session == "" {
			*session = newSessionID()
			fmt.Fprintf(c.stderr, "cleanroom: no --session given, using %s. "+
				"If this conversation is resumed in a new process it will start clean; pass --session to prevent that.\n", *session)
		}
		upstreams := upstream.New(cfg.Policy, upstream.CommandDialer(cfg.Dir))
		defer upstreams.Close()
		g := &gateway.Gateway{Policy: cfg.Policy, Log: log, Session: *session, Upstreams: upstreams}
		server, err := g.Server(ctx)
		if err != nil {
			return err
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}
}

func newSessionID() string {
	b := make([]byte, 6)
	rand.Read(b) // never fails
	return "s-" + hex.EncodeToString(b)
}
