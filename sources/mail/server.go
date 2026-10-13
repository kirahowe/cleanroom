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
	Query string `json:"query" jsonschema:"search text; the tool description says which filters work"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results, 1 to 50 (default 20)"`
}

type searchOutput struct {
	Messages []Summary `json:"messages" jsonschema:"matching messages, newest first"`
}

type readInput struct {
	ID string `json:"id" jsonschema:"message ID from a search result"`
}

type tools struct {
	mailbox Mailbox
}

// newServer builds the MCP server. Both of its tools read. Sending mail is
// not something a policy can switch on, because no tool here does it.
func newServer(mailbox Mailbox) *mcp.Server {
	t := &tools{mailbox: mailbox}
	s := mcp.NewServer(&mcp.Implementation{Name: "mail-source", Version: "0.1.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search",
		Description: "Search the mailbox. Returns message summaries, newest first. " + mailbox.QuerySyntax(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, t.search)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "read",
		Description: "Read one message by ID. Returns the plain text body. This may mark the message as read in the mailbox.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, t.read)
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
