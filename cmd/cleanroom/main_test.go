package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain lets the test binary stand in for the cleanroom binary: when
// the variable is set it runs main instead of the tests. Tests can then
// start real cleanroom processes without building anything first.
//
// A second value makes it a small upstream MCP server, so that the real
// server has something real to proxy.
func TestMain(m *testing.M) {
	switch os.Getenv("CLEANROOM_TEST_AS") {
	case "cleanroom":
		main()
	case "upstream":
		fakeUpstream()
	default:
		os.Exit(m.Run())
	}
}

// fakeUpstream is a calendar-like MCP server with one tool that acts.
func fakeUpstream() {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-calendar", Version: "0"}, nil)
	s.AddTool(&mcp.Tool{
		Name:        "create_event",
		Description: "Create an event.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "created " + string(req.Params.Arguments)}}}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

// cleanroom returns a command that runs the cleanroom binary with args.
func cleanroom(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "CLEANROOM_TEST_AS=cleanroom")
	cmd.Stderr = os.Stderr
	return cmd
}

func writePolicy(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// testPolicy is a policy whose one upstream is the fake calendar, run from
// this same test binary.
func testPolicy(t *testing.T) string {
	t.Helper()
	return writePolicy(t, fmt.Sprintf(`{
		"upstreams": {
			"calendar": {
				"command": [%q],
				"env": {"CLEANROOM_TEST_AS": "upstream"},
				"labels": [],
				"tools": {"create_event": {"class": "act"}},
			},
		},
	}`, os.Args[0]))
}

// TestServeOverStdio starts the real server as a child process, which
// starts the upstream as its own child, and calls through both over stdin
// and stdout, as an MCP client does.
func TestServeOverStdio(t *testing.T) {
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{
		Command: cleanroom(t, "serve", "--policy", testPolicy(t), "--session", "chief"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, `"session": "chief"`) {
		t.Errorf("status = %s", got)
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "calendar_create_event", Arguments: map[string]any{"title": "Reunion"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Content[0].(*mcp.TextContent).Text, `created {"title":"Reunion"}`; got != want {
		t.Errorf("create_event = %q, want %q", got, want)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := [][]string{
		{},
		{"frobnicate"},
		{"serve"},                                // no policy
		{"serve", "--policy", "/does/not/exist"}, // unreadable policy
		{"serve", "--no-such-flag"},
	}
	for _, args := range tests {
		cmd := cleanroom(t, args...)
		cmd.Env = append(cmd.Env, "CLEANROOM_POLICY=")
		cmd.Stderr = nil
		if err := cmd.Run(); err == nil {
			t.Errorf("cleanroom %v exited 0, want an error", args)
		}
	}
}
