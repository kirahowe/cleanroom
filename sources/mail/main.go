// Command mail-source is a stdio MCP server that exposes a mailbox through
// the search and read tools. It cannot send mail. Logs go to stderr because
// stdout carries the protocol.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	cfg, err := parseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	server := newServer(newMailpit(cfg.mailpitURL))
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
