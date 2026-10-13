package gateway

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/upstream"
)

// readerTools is what the reader of one ask can reach: the tools of its
// source that only read. It implements reader.Tools, and it reaches the
// source through the gateway's own upstream connections, so a reader starts
// no server and logs in nowhere.
//
// Listing only those tools is a convenience for the reader. The limit that
// counts is in core.Decide, which every call below goes through under the
// reader's own session and which refuses anything else.
type readerTools struct {
	g       *Gateway
	session string // the reader's own session, not the gateway's
	source  string
}

// forReader returns the tools for the reader of the ask at askSeq.
func (g *Gateway) forReader(askSeq int, source string) readerTools {
	return readerTools{g: g, session: core.ReaderSession(askSeq), source: source}
}

func (r readerTools) List(ctx context.Context) ([]*mcp.Tool, error) {
	up := r.g.Policy.Upstreams[r.source]
	all, err := r.g.Upstreams.Tools(ctx, r.source)
	if err != nil {
		return nil, err
	}
	var tools []*mcp.Tool
	for _, tool := range all {
		class, ok := core.ClassOf(up, tool.Name, upstream.Hint(tool))
		if ok && (class == core.Read || class == core.Egress) {
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

func (r readerTools) Call(ctx context.Context, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	all, err := r.g.Upstreams.Tools(ctx, r.source)
	if err != nil {
		return nil, err
	}
	var hint core.Class
	for _, t := range all {
		if t.Name == tool {
			hint = upstream.Hint(t)
		}
	}
	return r.g.callTool(ctx, core.Call{
		Session: r.session, Op: core.OpTool,
		Upstream: r.source, Tool: tool, Args: args, Hint: hint,
	}), nil
}
