package main

import (
	"context"
	"errors"
	"flag"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/gateway"
	"cleanroom/internal/upstream"
)

// readerToolsCommand serves the tools of one reader over stdio. Cleanroom
// starts it indirectly: a command reader is given an MCP config file that
// names this command, and connects to it as any MCP client would.
func readerToolsCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	ask := fs.Int("ask", 0, "number of the ask this reader is answering")
	return func(ctx context.Context) error {
		if *ask == 0 {
			return errors.New("--ask is required")
		}
		st, err := c.open()
		if err != nil {
			return err
		}
		upstreams := upstream.New(st.cfg.Policy, upstream.CommandDialer(st.cfg.Dir))
		defer upstreams.Close()
		g := &gateway.Gateway{Policy: st.cfg.Policy, Log: st.log, Upstreams: upstreams, Logf: log.Printf}
		server, err := g.ReaderServer(ctx, *ask)
		if err != nil {
			return err
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}
}
