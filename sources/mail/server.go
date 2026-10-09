package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultLimit = 20
	maxLimit     = 50
	maxQueryLen  = 500
)

type searchInput struct {
	Query string `json:"query" jsonschema:"search text; Mailpit filters such as from:, to:, subject: and is:unread also work"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results, 1 to 50 (default 20)"`
}

type searchOutput struct {
	Messages []Summary `json:"messages" jsonschema:"matching messages, newest first"`
}

type readInput struct {
	ID string `json:"id" jsonschema:"message ID from a search result"`
}

type sendInput struct {
	To      []string `json:"to" jsonschema:"recipient email addresses"`
	Subject string   `json:"subject" jsonschema:"subject line, plain text, no line breaks"`
	Body    string   `json:"body" jsonschema:"plain text message body, up to 100 KB"`
}

type sendOutput struct {
	Status     string   `json:"status" jsonschema:"always sent on success"`
	Recipients []string `json:"recipients" jsonschema:"addresses the message was sent to"`
}

// tools holds the mailbox and the send policy shared by all handlers.
type tools struct {
	mailbox Mailbox
	allowed map[string]bool // lower-case addresses; empty means no restriction
}

// newServer builds the MCP server. If allowed is not empty, send refuses
// every recipient that is not in it.
func newServer(mailbox Mailbox, allowed []string) *mcp.Server {
	t := &tools{mailbox: mailbox, allowed: map[string]bool{}}
	for _, a := range allowed {
		t.allowed[strings.ToLower(a)] = true
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "mail-source", Version: "0.1.0"}, nil)
	closed := false
	open := true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search",
		Description: "Search the mailbox. Returns message summaries, newest first.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, t.search)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "read",
		Description: "Read one message by ID. Returns the plain text body. This may mark the message as read in the mailbox.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, t.read)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "send",
		Description: "Send a plain text email from the configured sender address. The recipients may be restricted by the server configuration.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &closed, OpenWorldHint: &open},
	}, t.send)
	return s
}

func (t *tools) search(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
	query := strings.TrimSpace(in.Query)
	switch {
	case query == "":
		return nil, searchOutput{}, errors.New("query is required")
	case len(query) > maxQueryLen:
		return nil, searchOutput{}, fmt.Errorf("query is longer than %d bytes", maxQueryLen)
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maxLimit {
		return nil, searchOutput{}, fmt.Errorf("limit must be between 1 and %d", maxLimit)
	}
	found, err := t.mailbox.Search(ctx, query, limit)
	if err != nil {
		return nil, searchOutput{}, err
	}
	if found == nil {
		found = []Summary{}
	}
	return nil, searchOutput{Messages: found}, nil
}

func (t *tools) read(ctx context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, Message, error) {
	if in.ID == "" {
		return nil, Message{}, errors.New("id is required")
	}
	msg, err := t.mailbox.Read(ctx, in.ID)
	return nil, msg, err
}

func (t *tools) send(ctx context.Context, _ *mcp.CallToolRequest, in sendInput) (*mcp.CallToolResult, sendOutput, error) {
	msg, err := newOutgoing(in.To, in.Subject, in.Body)
	if err != nil {
		return nil, sendOutput{}, err
	}
	if len(t.allowed) > 0 {
		for _, rcpt := range msg.To {
			if !t.allowed[strings.ToLower(rcpt)] {
				return nil, sendOutput{}, fmt.Errorf("recipient %s is not in the allowed recipients list", rcpt)
			}
		}
	}
	if err := t.mailbox.Send(ctx, msg); err != nil {
		return nil, sendOutput{}, err
	}
	return nil, sendOutput{Status: "sent", Recipients: msg.To}, nil
}
