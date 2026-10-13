package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/eventlog"
)

// connect serves g over an in-memory transport and returns a client
// connected to it, as an agent's MCP client would be.
func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call calls a tool and decodes its structured content into out.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if out != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: structured content %s: %v", tool, b, err)
		}
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	out := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}

func newLog(t *testing.T) *eventlog.Log {
	t.Helper()
	log, err := eventlog.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return log
}

func TestStatusOfANewSession(t *testing.T) {
	g := &Gateway{
		Policy:  core.Policy{Upstreams: map[string]core.Upstream{}},
		Log:     newLog(t),
		Session: "chief",
	}
	server, err := g.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cs := connect(t, server)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if diff := cmp.Diff([]string{"ask", "read_answer", "status"}, names); diff != "" {
		t.Fatalf("built-in tools (-want +got):\n%s", diff)
	}

	var st Status
	call(t, cs, "status", nil, &st)
	if st.Session != "chief" || st.Labels != (core.Labels{}) || len(st.Held) != 0 {
		t.Errorf("status = %+v, want a clean session named chief", st)
	}
}

func TestServerRejectsABadSessionID(t *testing.T) {
	for _, id := range []string{"", "reader:1", "two words", "a\nb"} {
		g := &Gateway{Log: newLog(t), Session: id}
		if _, err := g.Server(context.Background()); err == nil {
			t.Errorf("session id %q was accepted", id)
		}
	}
}
