// Package mcptest provides in-memory MCP servers that stand in for real
// upstreams in tests.
package mcptest

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/upstream"
)

// Tool describes one tool of a fake upstream.
type Tool struct {
	Name string
	// Hints are the annotations the server gives. Nil means none.
	Hints *mcp.ToolAnnotations
	// Reply is the text the tool returns.
	Reply string
	// Fail makes the tool return an error result.
	Fail bool
}

// Upstream is a fake upstream server. It records every call that reaches
// it, which is how tests check that a held or refused call never did.
type Upstream struct {
	server *mcp.Server

	mu    sync.Mutex
	calls []string
}

// NewUpstream returns a fake upstream with the given tools.
func NewUpstream(tools ...Tool) *Upstream {
	u := &Upstream{server: mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "0"}, nil)}
	for _, tool := range tools {
		u.server.AddTool(&mcp.Tool{
			Name:        tool.Name,
			Description: "fake " + tool.Name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Annotations: tool.Hints,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u.mu.Lock()
			u.calls = append(u.calls, tool.Name+" "+string(req.Params.Arguments))
			u.mu.Unlock()
			return &mcp.CallToolResult{
				IsError: tool.Fail,
				Content: []mcp.Content{&mcp.TextContent{Text: tool.Reply}},
			}, nil
		})
	}
	return u
}

// Seen returns the calls that reached the upstream, as "tool {arguments}".
func (u *Upstream) Seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.calls...)
}

// Dialer connects cleanroom to fakes by name instead of starting
// processes.
func Dialer(t testing.TB, fakes map[string]*Upstream) upstream.Dialer {
	return func(ctx context.Context, name string, _ core.Upstream) (*mcp.ClientSession, error) {
		serverSide, clientSide := mcp.NewInMemoryTransports()
		ss, err := fakes[name].server.Connect(ctx, serverSide, nil)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { ss.Close() })
		return mcp.NewClient(&mcp.Implementation{Name: "cleanroom", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	}
}

// Hints builds tool annotations.
func Hints(readOnly, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: readOnly, OpenWorldHint: &openWorld}
}
