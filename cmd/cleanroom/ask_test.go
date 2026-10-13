package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAskFromTheCommandLine(t *testing.T) {
	policy := testPolicy(t)

	// Not at a terminal: what the ask tool would return.
	out, ok := runCLI(t, false, "", "ask", "--policy", policy, "--returns", "number", "How many", "tools can you see?")
	if !ok || !strings.Contains(out, `"status": "answered"`) || !strings.Contains(out, `"value": 1`) {
		t.Errorf("got %v:\n%s", ok, out)
	}

	// At a terminal: the screen a person reads. Flags count after the
	// question too, and stay out of it.
	out, ok = runCLI(t, true, "", "ask", "How many tools can you see?", "--policy", policy, "--returns", "number")
	if !ok || !strings.Contains(out, "│ How many tools can you see?\n") || !strings.Contains(out, "Answer\n  │ 1\n") {
		t.Errorf("got %v:\n%s", ok, out)
	}

	// The reader answers with a number, which is not a date.
	out, ok = runCLI(t, false, "", "ask", "--policy", policy, "--returns", "date", "When?")
	if !ok || !strings.Contains(out, `"status": "no_answer"`) {
		t.Errorf("got %v:\n%s", ok, out)
	}

	// Every ask is in the log, under a session of its own.
	log, _ := runCLI(t, false, "", "log", "--policy", policy)
	if strings.Count(log, "asked calendar") != 3 || !strings.Contains(log, "cli-s-") {
		t.Errorf("log:\n%s", log)
	}
}

func TestAskRefusals(t *testing.T) {
	policy := testPolicy(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no question":    {[]string{"--returns", "date"}, "give the question"},
		"unknown type":   {[]string{"--returns", "poem", "Write one"}, "REFUSED"},
		"unknown source": {[]string{"--source", "nowhere", "Anything?"}, "REFUSED"},
		// A mistyped flag after the question is an error. It is not
		// asked as part of the question.
		"unknown flag after the question": {[]string{"Anything?", "--response", "boolean"}, "flag provided but not defined: -response"},
		"bad type after the question":     {[]string{"Write one", "--returns", "poem"}, "REFUSED"},
	} {
		out, ok := runCLI(t, false, "", append([]string{"ask", "--policy", policy}, tc.args...)...)
		if ok || !strings.Contains(out, tc.want) {
			t.Errorf("%s: %v, want an error mentioning %q:\n%s", name, ok, tc.want, out)
		}
	}
}

func TestReturnsSpec(t *testing.T) {
	for in, want := range map[string]string{
		"date":                                  `"date"`,
		" money ":                               `"money"`,
		`"number"`:                              `"number"`,
		`{"type":"choice","choices":["a","b"]}`: `{"type":"choice","choices":["a","b"]}`,
	} {
		if got := string(returnsSpec(in)); got != want {
			t.Errorf("returnsSpec(%q) = %s, want %s", in, got, want)
		}
	}
}

// A reader that fails gives no answer. At a terminal the person is told
// where to look.
func TestAskExplainsAFailedReader(t *testing.T) {
	policy := readerPolicy(t, `"false"`)
	out, ok := runCLI(t, true, "", "ask", "--policy", policy, "--returns", "date", "When?")
	// The hints name this policy and the log that belongs to it, so they
	// hold for a policy that is not at the default path.
	for _, want := range []string{
		"No answer",
		filepath.Join(filepath.Dir(policy), ".cleanroom", "children.log") + "\n",
		"cleanroom log --policy " + policy + "\n",
	} {
		if !ok || !strings.Contains(out, want) {
			t.Errorf("got %v, want %q in:\n%s", ok, want, out)
		}
	}
	state := t.TempDir()
	out, _ = runCLI(t, true, "", "ask", "--policy", policy, "--state", state, "--returns", "date", "When?")
	if want := filepath.Join(state, "children.log") + "\n"; !strings.Contains(out, want) {
		t.Errorf("with --state, want %q in:\n%s", want, out)
	}
	// An agent is told only that there is no answer.
	out, _ = runCLI(t, false, "", "ask", "--policy", policy, "--returns", "date", "When?")
	if strings.Contains(out, "children.log") || !strings.Contains(out, `"status": "no_answer"`) {
		t.Errorf("without a terminal:\n%s", out)
	}
}

// A reader whose tools Claude Code would not start says so by its exit
// status. The person is shown the entry to add and the file that has the
// list.
func TestAskExplainsBlockedTools(t *testing.T) {
	policy := readerPolicy(t, `"sh", "-c", "exit 78"`)
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	settings := filepath.Join(configDir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"allowedMcpServers": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := selfPath()
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(self)

	out, ok := runCLI(t, true, "", "ask", "--policy", policy, "--returns", "date", "When?")
	for _, want := range []string{
		"No answer: the reader could not start its tools.",
		`"allowedMcpServers" in` + "\n" + settings + ":",
		`{ "serverCommand": [` + string(quoted) + `,"reader-tools"] }`,
	} {
		if !ok || !strings.Contains(out, want) {
			t.Errorf("got %v, want %q in:\n%s", ok, want, out)
		}
	}
	if strings.Contains(out, "children.log") {
		t.Errorf("sent the person to the reader's log for a cause that is known:\n%s", out)
	}

	out, _ = runCLI(t, false, "", "ask", "--policy", policy, "--returns", "date", "When?")
	if strings.Contains(out, "allowedMcpServers") || !strings.Contains(out, `"status": "no_answer"`) {
		t.Errorf("without a terminal:\n%s", out)
	}
}

// A reader that could not log in to its model says so by its exit status.
// The person is shown where Claude Code looked for the login.
func TestAskExplainsAMissingLogin(t *testing.T) {
	policy := readerPolicy(t, `"sh", "-c", "exit 77"`)
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	out, ok := runCLI(t, true, "", "ask", "--policy", policy, "--returns", "date", "When?")
	for _, want := range []string{
		"No answer: the reader is not logged in to its model.",
		"looks for a login in\n" + configDir + ".",
		"/login",
	} {
		if !ok || !strings.Contains(out, want) {
			t.Errorf("got %v, want %q in:\n%s", ok, want, out)
		}
	}
	if strings.Contains(out, "children.log") {
		t.Errorf("sent the person to the reader's log for a cause that is known:\n%s", out)
	}

	out, _ = runCLI(t, false, "", "ask", "--policy", policy, "--returns", "date", "When?")
	if strings.Contains(out, "/login") || !strings.Contains(out, `"status": "no_answer"`) {
		t.Errorf("without a terminal:\n%s", out)
	}
}

// Without CLAUDE_CONFIG_DIR the login is under the home directory.
func TestClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got, want := claudeConfigDir(), filepath.Join(home, ".claude"); got != want {
		t.Errorf("claudeConfigDir() = %q, want %q", got, want)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/elsewhere")
	if got := claudeConfigDir(); got != "/elsewhere" {
		t.Errorf("claudeConfigDir() = %q with CLAUDE_CONFIG_DIR set", got)
	}
}

func TestAllowlistFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	without := write("without.json", `{"model": "opus"}`)
	broken := write("broken.json", `{"allowedMcpServers": [`)
	with := write("with.json", `{"allowedMcpServers": []}`)
	missing := filepath.Join(dir, "missing.json")

	if got := allowlistFile([]string{missing, without, broken, with}); got != with {
		t.Errorf("allowlistFile = %q, want %q", got, with)
	}
	if got := allowlistFile([]string{missing, without}); got != "" {
		t.Errorf("allowlistFile = %q, want none", got)
	}
	if help := toolsBlockedHelp([]string{"/bin/cleanroom", "reader-tools"}, ""); !strings.Contains(help, "your Claude Code settings.json:") {
		t.Errorf("help without a file:\n%s", help)
	}
}

// readerPolicy is a policy whose one source has the given reader command,
// written as the elements of a JSON array.
func readerPolicy(t *testing.T, reader string) string {
	t.Helper()
	return writePolicy(t, fmt.Sprintf(`{
		"upstreams": {
			"calendar": {
				"command": [%q],
				"env": {"CLEANROOM_TEST_AS": "upstream"},
				"labels": [],
				"reader": [%s],
				"tools": {"list_events": {"class": "read"}},
			},
		},
	}`, os.Args[0], reader))
}
