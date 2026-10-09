package main

import (
	"context"
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
func TestMain(m *testing.M) {
	if os.Getenv("CLEANROOM_TEST_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// cleanroom returns a command that runs the cleanroom binary with args.
func cleanroom(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "CLEANROOM_TEST_AS_MAIN=1")
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

// TestServeOverStdio starts the real server as a child process and talks
// to it over stdin and stdout, as an MCP client does.
func TestServeOverStdio(t *testing.T) {
	policy := writePolicy(t, `{"upstreams": {"none": {"command": ["true"]}}}`)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cleanroom(t, "serve", "--policy", policy, "--session", "chief")}, nil)
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
