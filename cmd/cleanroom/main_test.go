package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// Leave a line for each time this server is started, so a test can
	// count how many times cleanroom started it.
	if path := os.Getenv("FAKE_UPSTREAM_STARTS"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(1)
		}
		fmt.Fprintln(f, "started")
		f.Close()
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-calendar", Version: "0"}, nil)
	s.AddTool(&mcp.Tool{
		Name:        "create_event",
		Description: "Create an event.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Leave a trace on disk, so a test can count how many times the
		// effect really happened.
		if path := os.Getenv("FAKE_UPSTREAM_TRACE"); path != "" {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(f, "create_event %s\n", req.Params.Arguments)
			f.Close()
		}
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
			Env     map[string]string
		}
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		log.Fatal(err)
	}
	server := config.MCPServers["calendar"]
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Env = append(os.Environ(), "CLEANROOM_TEST_AS=cleanroom")
	for name, value := range server.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
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

// testPassword is what a person "types" at the password prompt in tests.
const testPassword = "typed-password"

func cleanroom(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "CLEANROOM_TEST_AS=cleanroom")
	cmd.Stderr = os.Stderr
	return cmd
}

// runCLI runs a cleanroom command in this process, as a person at a
// terminal or as a program without one, and returns what it printed.
func runCLI(t *testing.T, terminal bool, stdin string, args ...string) (stdout string, ok bool) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, stdio{
		stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut, terminal: terminal,
		readPassword: func() (string, error) { return testPassword, nil },
	})
	return out.String() + errOut.String(), code == 0
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
// server, a reader started as a command, the pipe that joins the reader to
// the server, and the upstream behind it.
//
// The server starts the upstream once and every reader uses that one
// connection. A server that has to log in must not do it per question.
func TestAskThroughRealProcesses(t *testing.T) {
	ctx := context.Background()
	starts := filepath.Join(t.TempDir(), "starts")
	t.Setenv("FAKE_UPSTREAM_STARTS", starts)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{
		Command: cleanroom(t, "serve", "--policy", testPolicy(t), "--session", "chief"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	for range 3 {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ask", Arguments: map[string]any{
			"source": "calendar", "question": "How many tools can you see?", "returns": "number",
		}})
		if err != nil {
			t.Fatal(err)
		}
		// One: list_events. The reader was not offered create_event
		// and could not call it.
		if got := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, `"value": 1`) {
			t.Errorf("ask = %s, want the value 1", got)
		}
	}
	cs.Close()
	if got, _ := os.ReadFile(starts); string(got) != "started\n" {
		t.Errorf("after three questions the upstream had been started %d times, want once", strings.Count(string(got), "started"))
	}
}

// TestPersonCommands holds a call through the real server, then plays the
// person at the command line.
func TestPersonCommands(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	policy := writePolicy(t, fmt.Sprintf(`{
		"upstreams": {
			"calendar": {
				"command": [%q],
				"env": {"CLEANROOM_TEST_AS": "upstream", "FAKE_UPSTREAM_TRACE": %q},
				"labels": [],
				"tools": {"create_event": {"class": "act", "always_hold": true}},
			},
		},
	}`, os.Args[0], trace))

	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx,
		&mcp.CommandTransport{Command: cleanroom(t, "serve", "--policy", policy, "--session", "chief")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for range 2 {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "calendar_create_event", Arguments: map[string]any{"title": "Reunion"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Content[0].(*mcp.TextContent).Text; !strings.HasPrefix(got, "HELD") {
			t.Fatalf("create_event = %q, want it held", got)
		}
	}
	// The two held calls are events 1 and 2.

	out, _ := runCLI(t, false, "", "proposals", "--policy", policy)
	if strings.Count(out, "pending") != 2 || !strings.Contains(out, "calendar.create_event") {
		t.Errorf("proposals:\n%s", out)
	}

	// Without a terminal, neither reading nor approving is possible.
	for _, cmd := range []string{"show", "approve"} {
		out, ok := runCLI(t, false, "1\n", cmd, "--policy", policy, "1")
		if ok || !strings.Contains(out, "only works at a terminal") {
			t.Errorf("%s without a terminal: ok=%v\n%s", cmd, ok, out)
		}
	}
	if _, err := os.Stat(trace); err == nil {
		t.Fatal("the call ran without a person")
	}

	out, ok := runCLI(t, true, "", "show", "--policy", policy, "1")
	if !ok || !strings.Contains(out, `title: "Reunion"`) || !strings.Contains(out, "the policy holds every call") {
		t.Errorf("show:\n%s", out)
	}

	// At a terminal, anything but the proposal number cancels.
	// Ctrl+C also cancels, even after a partial confirmation, and leaves
	// the proposal pending without executing or denying it.
	for _, input := range []string{"", "1"} {
		out = runCancelledPrompt(t, input, "approve", "--policy", policy, "1")
		if !strings.Contains(out, "The call is still held") {
			t.Errorf("interrupted approve:\n%s", out)
		}
		out, _ = runCLI(t, false, "", "proposals", "--policy", policy)
		if strings.Count(out, "pending") != 2 {
			t.Errorf("interrupted approve changed a proposal:\n%s", out)
		}
		if _, err := os.Stat(trace); !os.IsNotExist(err) {
			t.Fatalf("interrupted approve ran the call: %v", err)
		}
	}
	out, _ = runCLI(t, true, "yes\n", "approve", "--policy", policy, "1")
	if !strings.Contains(out, "Cancelled") {
		t.Errorf("approve with the wrong confirmation:\n%s", out)
	}
	out, ok = runCLI(t, true, "1\n", "approve", "--policy", policy, "1")
	if !ok || !strings.Contains(out, "The call ran once: done") {
		t.Errorf("approve:\n%s", out)
	}
	if out, ok := runCLI(t, true, "1\n", "approve", "--policy", policy, "1"); ok {
		t.Errorf("a second approve succeeded:\n%s", out)
	}
	ran, _ := os.ReadFile(trace)
	if got, want := string(ran), "create_event {\"title\":\"Reunion\"}\n"; got != want {
		t.Errorf("the upstream ran %q, want exactly %q", got, want)
	}

	// Denying needs no terminal: it can only make things safer.
	if out, ok := runCLI(t, false, "", "deny", "--policy", policy, "2"); !ok || !strings.Contains(out, "will never run") {
		t.Errorf("deny:\n%s", out)
	}
	out, _ = runCLI(t, false, "", "proposals", "--policy", policy)
	if !strings.Contains(out, "done") || !strings.Contains(out, "denied") {
		t.Errorf("proposals after deciding:\n%s", out)
	}

	out, ok = runCLI(t, false, "", "explain", "--policy", policy, "1")
	if !ok || !strings.Contains(out, "Decision: hold (class act)") || !strings.Contains(out, "It matches the decision recorded at the time") {
		t.Errorf("explain:\n%s", out)
	}
	if out, _ := runCLI(t, false, "", "log", "--policy", policy); !strings.Contains(out, "the person approved proposal 1") {
		t.Errorf("log:\n%s", out)
	}
	for _, args := range [][]string{{"show", "--policy", policy}, {"show", "--policy", policy, "x"}, {"explain", "--policy", policy, "99"}, {"deny", "--policy", policy, "3"}} {
		if out, ok := runCLI(t, true, "", args...); ok {
			t.Errorf("cleanroom %v succeeded:\n%s", args, out)
		}
	}
}

// TestRepairAfterACrash cuts the log's last line short, as a crash in the
// middle of an append would. Every command fails and says how to recover,
// and repair recovers.
func TestRepairAfterACrash(t *testing.T) {
	policy := testPolicy(t)
	state := filepath.Join(filepath.Dir(policy), ".cleanroom")
	if out, ok := runCLI(t, false, "", "repair", "--policy", policy); !ok || !strings.Contains(out, "Nothing to repair") {
		t.Fatalf("repair on a new log:\n%s", out)
	}
	path := filepath.Join(state, "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"seq":1,"kind":"call","call":{"ca`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, ok := runCLI(t, false, "", "log", "--policy", policy); ok || !strings.Contains(out, "cleanroom repair") {
		t.Errorf("log on a cut-off file should fail and name the repair command:\n%s", out)
	}
	if out, ok := runCLI(t, false, "", "repair", "--policy", policy); !ok || !strings.Contains(out, "Set aside") {
		t.Fatalf("repair:\n%s", out)
	}
	if out, ok := runCLI(t, false, "", "log", "--policy", policy); !ok {
		t.Errorf("log after the repair:\n%s", out)
	}
	if aside, _ := os.ReadFile(path + ".cut-off"); !strings.Contains(string(aside), `"seq":1`) {
		t.Errorf("the cut-off line was not kept: %q", aside)
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
		// An empty home, so that no default policy is found.
		cmd.Env = append(cmd.Env, "HOME="+t.TempDir())
		cmd.Stderr = nil
		if err := cmd.Run(); err == nil {
			t.Errorf("cleanroom %v exited 0, want an error", args)
		}
	}
}

func TestParseAnywhere(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		returns string
		rest    []string
	}{
		"flags first":      {[]string{"--returns", "date", "When?"}, "date", []string{"When?"}},
		"flags last":       {[]string{"When?", "--returns", "date"}, "date", []string{"When?"}},
		"flags between":    {[]string{"When", "--returns=date", "is", "it?"}, "date", []string{"When", "is", "it?"}},
		"after the dashes": {[]string{"--returns", "date", "--", "--returns", "x"}, "date", []string{"--returns", "x"}},
		"dashes later":     {[]string{"What", "is", "--", "--returns", "?"}, "text", []string{"What", "is", "--returns", "?"}},
		"no arguments":     {nil, "text", nil},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		returns := fs.String("returns", "text", "")
		if err := parseAnywhere(fs, tc.args); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if *returns != tc.returns || !slices.Equal(fs.Args(), tc.rest) {
			t.Errorf("%s: returns %q and arguments %q, want %q and %q", name, *returns, fs.Args(), tc.returns, tc.rest)
		}
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := parseAnywhere(fs, []string{"When?", "--response", "boolean"}); err == nil {
		t.Errorf("an unknown flag after an argument was accepted, leaving %q", fs.Args())
	}
}

// Enter alone does not approve, and the cancellation says what was expected.
func TestApprovePromptNamesTheNumberToType(t *testing.T) {
	if prompt := approvePrompt(318); !strings.Contains(prompt, "type the proposal number, 318,") || !strings.Contains(prompt, "Enter alone") {
		t.Errorf("prompt: %q", prompt)
	}
	if got := notApproved(318, true); !strings.Contains(got, "was not 318") || !strings.Contains(got, "still held") {
		t.Errorf("after a wrong entry: %q", got)
	}
	if got := notApproved(318, false); strings.Contains(got, "318") || !strings.Contains(got, "still held") {
		t.Errorf("after an interrupt: %q", got)
	}
}
