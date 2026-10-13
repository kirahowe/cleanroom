// Command mail-source is a stdio MCP server that exposes a mailbox through
// the search and read tools. It cannot send mail. Logs go to stderr because
// stdout carries the protocol.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.SetOutput(os.Stderr)
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var mailbox Mailbox = newMailpit(cfg.mailpitURL)
	if cfg.imap != nil {
		box, err := newIMAP(*cfg.imap)
		if err != nil {
			log.Fatal(err)
		}
		if cfg.check {
			count, err := box.Count(ctx)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(checkReport(*cfg.imap, count))
			return
		}
		mailbox = box
	}

	if err := newServer(mailbox).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func checkReport(opts imapOptions, count uint32) string {
	return fmt.Sprintf("Logged in to %s as %s. Mailbox %q is open read-only and holds %d messages.",
		opts.addr, opts.user, opts.mailbox, count)
}
