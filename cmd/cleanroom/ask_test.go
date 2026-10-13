package main

import (
	"fmt"
	"os"
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

	// At a terminal: the screen a person reads.
	out, ok = runCLI(t, true, "", "ask", "--policy", policy, "--returns", "number", "How many tools can you see?")
	if !ok || !strings.Contains(out, "How many tools can you see?") || !strings.Contains(out, "Answer (a checked value): 1") {
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
// where to look and what the usual cause is.
func TestAskExplainsAFailedReader(t *testing.T) {
	policy := writePolicy(t, fmt.Sprintf(`{
		"upstreams": {
			"calendar": {
				"command": [%q],
				"env": {"CLEANROOM_TEST_AS": "upstream"},
				"labels": [],
				"reader": ["false"],
				"tools": {"list_events": {"class": "read"}},
			},
		},
	}`, os.Args[0]))
	out, ok := runCLI(t, true, "", "ask", "--policy", policy, "--returns", "date", "When?")
	if !ok || !strings.Contains(out, "No answer") || !strings.Contains(out, "cleanroom claude-settings") || !strings.Contains(out, "children.log") {
		t.Errorf("got %v:\n%s", ok, out)
	}
	// An agent is told only that there is no answer.
	out, _ = runCLI(t, false, "", "ask", "--policy", policy, "--returns", "date", "When?")
	if strings.Contains(out, "claude-settings") || !strings.Contains(out, `"status": "no_answer"`) {
		t.Errorf("without a terminal:\n%s", out)
	}
}
