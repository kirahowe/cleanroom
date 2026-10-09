package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
// Two more values make it a small upstream MCP server and a reader, so
// that the real server has real processes on every side.
func TestMain(m *testing.M) {
	switch os.Getenv("CLEANROOM_TEST_AS") {
	case "cleanroom":
		main()
	case "upstream":
		fakeUpstream()
	case "reader":
		fakeReader()
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
	s.AddTool(&mcp.Tool{
		Name:        "list_events",
		Description: "List events.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "3 events"}}}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

// fakeReader is a reader run as a command. It connects to the MCP server
// named in the config file it is given, as a headless agent would, and
// answers with the number of tools it was offered there. If it can call
// create_event it answers -1.
func fakeReader() {
	ctx := context.Background()
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	var config struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		log.Fatal(err)
	}
	server := config.MCPServers["calendar"]
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Env = append(os.Environ(), "CLEANROOM_TEST_AS=cleanroom")
	cmd.Stderr = os.Stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "fake-reader", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_events"}); err != nil {
		log.Fatal(err)
	}
	answer := len(tools.Tools)
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "create_event"}); err == nil && !res.IsError {
		answer = -1
	}
	fmt.Printf(`{"answer": %d}`+"\n", answer)
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
				"reader": ["env", "CLEANROOM_TEST_AS=reader", %q, "{mcp_config}"],
				"tools": {"create_event": {"class": "act"}, "list_events": {"class": "read"}},
			},
		},
	}`, os.Args[0], os.Args[0]))
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

// TestAskThroughRealProcesses runs the whole chain as processes: client,
// server, a reader started as a command, the reader's own tool server, and
// the upstream behind it.
func TestAskThroughRealProcesses(t *testing.T) {
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{
		Command: cleanroom(t, "serve", "--policy", testPolicy(t), "--session", "chief"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ask", Arguments: map[string]any{
		"source": "calendar", "question": "How many tools can you see?", "returns": "number",
	}})
	if err != nil {
		t.Fatal(err)
	}
	// One: list_events. The reader was not offered create_event and
	// could not call it.
	if got := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, `"value": 1`) {
		t.Errorf("ask = %s, want the value 1", got)
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
