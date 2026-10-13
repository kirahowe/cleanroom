package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cleanroom/internal/typed"
)

// fakeClaude writes a script that stands in for Claude Code. It records
// its arguments, stdin and working directory under dir, then prints output.
func fakeClaude(t *testing.T, dir, output string, exit int) string {
	t.Helper()
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + dir + `/args"
cat > "` + dir + `/stdin"
pwd > "` + dir + `/cwd"
printf '%s' "$CLAUDE_CONFIG_DIR" > "` + dir + `/config-dir"
ls -A | wc -l | tr -d ' ' > "` + dir + `/cwd-entries"
cat "` + dir + `/output"
exit ` + strconv.Itoa(exit) + "\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output"), []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mcpConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const oneServer = `{"mcpServers":{"mail":{"command":"cleanroom","args":["reader-tools","/tmp/x.sock"]}}}`

func recorded(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(b), "\n")
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	claude := fakeClaude(t, dir, `{"subtype":"success","is_error":false,"num_turns":4,"duration_ms":2500,"total_cost_usd":0.0123,
		"result":"ignored","structured_output":{"answer":"2027-07-17"}}`, 0)
	config := mcpConfig(t, oneServer)
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	// The session uses the login the environment selects, so it has to
	// inherit the environment unchanged.
	t.Setenv("CLAUDE_CONFIG_DIR", "/somewhere/else")

	answer, report, err := run(context.Background(), claude, "sonnet", config, "--when is the reunion?", `"date"`, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, dir, "config-dir"); got != "/somewhere/else" {
		t.Errorf("Claude Code saw CLAUDE_CONFIG_DIR=%q", got)
	}
	if string(answer) != `{"answer":"2027-07-17"}` {
		t.Errorf("answer = %s", answer)
	}
	if report != "success, 4 turns, 2.5s, $0.0123" {
		t.Errorf("report = %q", report)
	}

	// The question travels on stdin, where a leading dash means nothing.
	if got := recorded(t, dir, "stdin"); got != "--when is the reunion?" {
		t.Errorf("stdin = %q", got)
	}
	args := strings.Split(recorded(t, dir, "args"), "\n")
	if slices.Contains(args, "--when is the reunion?") {
		t.Error("the question is on the command line")
	}
	if cwd := recorded(t, dir, "cwd"); recorded(t, dir, "cwd-entries") != "0" {
		t.Errorf("the session ran in %s, which is not empty", cwd)
	} else if _, err := os.Stat(cwd); err == nil {
		t.Errorf("%s was left behind", cwd)
	}
}

func TestRunWithoutAnAnswer(t *testing.T) {
	config := mcpConfig(t, oneServer)
	now := time.Now()
	for name, tc := range map[string]struct {
		output string
		exit   int
		answer string
	}{
		"null answer":          {`{"subtype":"success","structured_output":{"answer":null}}`, 0, `{"answer":null}`},
		"no structured output": {`{"subtype":"success","result":"The reunion is on July 17."}`, 0, ""},
		"empty object":         {`{"subtype":"success","structured_output":{}}`, 0, ""},
		"session error":        {`{"subtype":"error_max_turns","is_error":true,"structured_output":{"answer":"2027-07-17"}}`, 1, ""},
		"not json":             {`Error: not logged in`, 1, ""},
		"nothing":              {``, 0, ""},
	} {
		claude := fakeClaude(t, t.TempDir(), tc.output, tc.exit)
		answer, _, err := run(context.Background(), claude, "sonnet", config, "q", `"date"`, now)
		if tc.answer == "" && err == nil {
			t.Errorf("%s: got %s, want an error", name, answer)
		}
		if tc.answer != "" && (err != nil || string(answer) != tc.answer) {
			t.Errorf("%s: got %s, %v", name, answer, err)
		}
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	claude := fakeClaude(t, t.TempDir(), `{"structured_output":{"answer":true}}`, 0)
	for name, tc := range map[string]struct{ config, returns string }{
		"unknown type": {mcpConfig(t, oneServer), `"poem"`},
		"two servers":  {mcpConfig(t, `{"mcpServers":{"a":{},"b":{}}}`), `"date"`},
		"no servers":   {mcpConfig(t, `{}`), `"date"`},
		"no config":    {filepath.Join(t.TempDir(), "missing.json"), `"date"`},
	} {
		if _, _, err := run(context.Background(), claude, "sonnet", tc.config, "q", tc.returns, time.Now()); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// The flags that leave the session with nothing but the source's tools.
// Dropping one of them gives a reader a shell, the user's other MCP
// servers, or the user's hooks.
func TestClaudeArgsLockTheSessionDown(t *testing.T) {
	args := claudeArgs("sonnet", "/tmp/mcp.json", "mail", "SYSTEM", "{}")
	for _, want := range [][]string{
		{"--print"},
		{"--tools", ""},
		{"--strict-mcp-config", "--mcp-config", "/tmp/mcp.json"},
		{"--allowedTools", "mcp__mail"},
		{"--permission-prompts", "none"},
		{"--setting-sources", ""},
		{"--disable-slash-commands"},
		{"--no-session-persistence"},
		{"--system-prompt", "SYSTEM"},
	} {
		if !containsRun(args, want) {
			t.Errorf("args lack %q: %q", want, args)
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, "dangerously") || arg == "bypassPermissions" {
			t.Errorf("args contain %q", arg)
		}
	}
}

func containsRun(haystack, needle []string) bool {
	for i := range haystack {
		if i+len(needle) <= len(haystack) && slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func TestAnswerSchema(t *testing.T) {
	for returns, want := range map[string]string{
		`"boolean"`: `{"type":"boolean"}`,
		`"number"`:  `{"type":"number"}`,
		`{"type":"choice","choices":["shipped","delivered"]}`: `{"enum":["shipped","delivered"],"type":"string"}`,
	} {
		spec, err := typed.ParseSpec(json.RawMessage(returns))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(answerSchema(spec))
		wantFull := `{"additionalProperties":false,"properties":{"answer":{"anyOf":[` + want + `,{"type":"null"}]}},"required":["answer"],"type":"object"}`
		if string(got) != wantFull {
			t.Errorf("%s:\n got %s\nwant %s", returns, got, wantFull)
		}
	}
}

// Every type a caller can ask for has a schema with the right JSON type,
// and a record's fields may each be null.
func TestValueSchemaCoversEveryType(t *testing.T) {
	for returns, wantType := range map[string]string{
		`"date"`: "string", `"datetime"`: "string", `"number"`: "number", `"money"`: "object",
		`"boolean"`: "boolean", `"text"`: "string",
		`{"type":"choice","choices":["a"]}`:                         "string",
		`{"type":"record","fields":{"due":"date","total":"money"}}`: "object",
	} {
		spec, err := typed.ParseSpec(json.RawMessage(returns))
		if err != nil {
			t.Fatalf("%s: %v", returns, err)
		}
		s := valueSchema(spec)
		if s["type"] != wantType {
			t.Errorf("%s: type %v, want %s", returns, s["type"], wantType)
		}
		if spec.Type == typed.Record {
			raw, _ := json.Marshal(s)
			if !strings.Contains(string(raw), `"due":{"anyOf":[{"description":"A calendar day, YYYY-MM-DD."`) ||
				!strings.Contains(string(raw), `"required":["due","total"]`) {
				t.Errorf("record schema: %s", raw)
			}
		}
	}
}

func TestSystemPrompt(t *testing.T) {
	p := systemPrompt("mail", "Monday, October 12, 2026")
	for _, want := range []string{`"mail"`, "Monday, October 12, 2026", "untrusted", "null"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
