package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeClaude stands in for Claude Code and records its arguments.
func fakeClaude(t *testing.T) (path, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	path, argsFile = filepath.Join(dir, "claude"), filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\n" +
		"printf '%s' \"$CLAUDE_CONFIG_DIR\" > \"" + dir + "/config-dir\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, argsFile
}

func TestClaude(t *testing.T) {
	claude, argsFile := fakeClaude(t)
	policy := testPolicy(t)

	// Claude Code is configured through its environment, for instance a
	// CLAUDE_CONFIG_DIR that selects which login it uses. It has to see
	// the same environment it would if the person had started it.
	t.Setenv("CLAUDE_CONFIG_DIR", "/somewhere/else")

	out, ok := runCLI(t, true, "", "claude", "--policy", policy, "--claude", claude)
	if !ok {
		t.Fatal(out)
	}
	if got := readFile(t, filepath.Join(filepath.Dir(claude), "config-dir")); got != "/somewhere/else" {
		t.Errorf("Claude Code saw CLAUDE_CONFIG_DIR=%q", got)
	}
	args := strings.Split(strings.TrimSpace(readFile(t, argsFile)), "\n")
	if len(args) != 7 || args[0] != "--session-id" || !uuidPattern.MatchString(args[1]) {
		t.Fatalf("args = %q", args)
	}
	id := args[1]
	// No built-in tools and no MCP server but cleanroom.
	if !slices.Equal(args[2:6], []string{"--tools", "", "--strict-mcp-config", "--mcp-config"}) {
		t.Errorf("args = %q", args)
	}
	var mcp struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal([]byte(args[6]), &mcp); err != nil {
		t.Fatal(err)
	}
	server := mcp.MCPServers["cleanroom"]
	if len(mcp.MCPServers) != 1 || !filepath.IsAbs(server.Command) ||
		!slices.Equal(server.Args, []string{"serve", "--session", id, "--policy", policy}) {
		t.Errorf("mcp config = %+v", mcp)
	}
	// The conversation and the cleanroom session share the id, and the
	// way back in keeps both.
	if !strings.Contains(out, "cleanroom claude --resume "+id+" --policy "+policy) {
		t.Errorf("output lacks the way to resume:\n%s", out)
	}

	out, ok = runCLI(t, true, "", "claude", "--policy", policy, "--claude", claude, "--resume", id)
	if !ok {
		t.Fatal(out)
	}
	args = strings.Split(strings.TrimSpace(readFile(t, argsFile)), "\n")
	if !slices.Equal(args[:4], []string{"--resume", id, "--tools", ""}) || !strings.Contains(args[6], `"--session","`+id+`"`) {
		t.Errorf("resume args = %q", args)
	}
}

func TestClaudeRefusals(t *testing.T) {
	claude, argsFile := fakeClaude(t)
	policy := testPolicy(t)
	missing := filepath.Join(t.TempDir(), "policy.jsonc")
	for name, tc := range map[string]struct {
		terminal bool
		args     []string
		want     string
	}{
		"not a terminal":      {false, []string{"--policy", policy, "--claude", claude}, "only works at a terminal"},
		"no policy":           {true, []string{"--policy", missing, "--claude", claude}, "cleanroom init"},
		"resume is not an id": {true, []string{"--policy", policy, "--claude", claude, "--resume", "--dangerously-skip-permissions"}, "--resume takes the id"},
		"claude is missing":   {true, []string{"--policy", policy, "--claude", filepath.Join(t.TempDir(), "claude")}, "could not start Claude Code"},
	} {
		out, ok := runCLI(t, tc.terminal, "", append([]string{"claude"}, tc.args...)...)
		if ok || !strings.Contains(out, tc.want) {
			t.Errorf("%s: %v, want an error mentioning %q:\n%s", name, ok, tc.want, out)
		}
		if strings.Contains(out, "To continue this conversation") {
			t.Errorf("%s: offered to resume a conversation that never started", name)
		}
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Error("Claude Code was started")
	}
}

func TestNewUUID(t *testing.T) {
	a, b := newUUID(), newUUID()
	if !uuidPattern.MatchString(a) || a[14] != '4' || a == b {
		t.Errorf("%s, %s", a, b)
	}
}
