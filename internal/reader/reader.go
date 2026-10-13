// Package reader runs disposable sessions that answer questions about a source.
//
// Readers are treated as compromised. core.Decide restricts tool access to
// one source; core.Conclude validates the answer.
package reader

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Job is one question for a reader.
type Job struct {
	// Ask is the Seq of the ask in the event log.
	Ask int
	// Source is the upstream the reader may read.
	Source   string
	Question string
	// Returns is the type of answer wanted, as the caller gave it.
	Returns json.RawMessage
	// Tools is the reader's access to its source. A reader that runs
	// inside this process calls it directly. A reader that runs as a
	// command reaches the same Tools over MCP.
	Tools Tools
}

// Tools is the read-only tools of one source, as a reader sees them.
type Tools interface {
	List(ctx context.Context) ([]*mcp.Tool, error)
	Call(ctx context.Context, tool string, args json.RawMessage) (*mcp.CallToolResult, error)
}

// Reader answers a question by reading a source. It returns the answer as
// raw JSON. The caller checks it, so a Reader need not be trusted.
type Reader interface {
	Answer(ctx context.Context, job Job) (raw []byte, err error)
}

// Func adapts a function to a Reader. Tests use it to script a reader,
// including one that misbehaves.
type Func func(ctx context.Context, job Job) ([]byte, error)

// Answer calls f.
func (f Func) Answer(ctx context.Context, job Job) ([]byte, error) { return f(ctx, job) }
