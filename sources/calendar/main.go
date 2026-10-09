// Command calendar-source is a stdio MCP server that writes events to an
// .ics file and reminders to a JSON file.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.SetOutput(os.Stderr) // stdout carries the MCP protocol
	log.SetFlags(0)

	defaultDir := "./calendar-data"
	if env := os.Getenv("CALENDAR_DIR"); env != "" {
		defaultDir = env
	}
	dir := flag.String("dir", defaultDir, "directory for calendar.ics and reminders.json (env CALENDAR_DIR)")
	flag.Parse()

	store, err := NewStore(*dir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := NewServer(store).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatalf("server: %v", err)
	}
}
