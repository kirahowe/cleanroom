// Package approval is the person's side of cleanroom: what they are shown
// and how their decision is carried out.
//
// Everything on a screen comes from the event log. Nothing is taken from
// what a model said about its own call, because a contaminated model would
// say whatever gets the call approved.
package approval

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"cleanroom/internal/core"
)

// Screen renders the held call at seq for the person to judge. It shows
// the tool, the arguments in full, why the call was held, and what the
// session that asked has been exposed to.
func Screen(p core.Policy, log []core.Event, seq int) (string, error) {
	pr, ok := core.ProposalAt(log, seq)
	if !ok {
		return "", fmt.Errorf("%d is not a held call", seq)
	}
	c := pr.Call.Call
	var b strings.Builder
	fmt.Fprintf(&b, "Proposal %d: %s.%s   [%s]\n", seq, visible(c.Upstream), visible(c.Tool), pr.Status)
	fmt.Fprintf(&b, "Asked by session %q. Class: %s.\n\n", visible(c.Session), pr.Call.Decision.Class)

	b.WriteString("Arguments, exactly as they will be sent:\n")
	b.WriteString(renderArgs(c.Args))

	b.WriteString("\nWhy it was held:\n")
	for _, r := range pr.Call.Decision.Reasons {
		fmt.Fprintf(&b, "  - %s\n", r.Text)
		for _, cited := range r.Events {
			fmt.Fprintf(&b, "      event %d: %s\n", cited, core.Summarize(log, cited))
		}
	}

	st := core.StateOf(p, log, c.Session)
	fmt.Fprintf(&b, "\nThe session is now: %s\n", st.Labels)
	if st.Labels.Contaminated {
		b.WriteString("\nThe arguments above were written by an agent that has read untrusted\n" +
			"content. Read them as you would read a request from a stranger.\n")
	}
	fmt.Fprintf(&b, "\nHash: %s\n", pr.Call.Hash)
	return b.String(), nil
}

// AnswerScreen renders the stored answer to the ask at seq for the person.
func AnswerScreen(log []core.Event, seq int) (string, error) {
	if seq < 1 || seq > len(log) || log[seq-1].Kind != core.KindCall || log[seq-1].Call.Call.Op != core.OpAsk {
		return "", fmt.Errorf("%d is not an ask", seq)
	}
	c := log[seq-1].Call.Call
	var b strings.Builder
	fmt.Fprintf(&b, "Ask %d, put to %q by session %q\n", seq, visible(c.Upstream), visible(c.Session))
	fmt.Fprintf(&b, "Question:\n%s\n", block(c.Question))

	ans, ok := core.AnswerTo(log, seq)
	switch {
	case !ok:
		b.WriteString("No answer yet.\n")
	case ans.Outcome == core.OutcomeNone:
		fmt.Fprintf(&b, "No answer: %s.\n", ans.Detail)
	case ans.Outcome == core.OutcomeTyped:
		fmt.Fprintf(&b, "Answer (a checked value): %s\n", visible(string(ans.Value)))
	default:
		b.WriteString("Answer:\n")
		b.WriteString(block(ans.Text))
		if ans.Labels.Contaminated {
			b.WriteString("\nThis text was written by a reader that read untrusted content. It may be\n" +
				"wrong, and it may be trying to persuade you. It has been shown to you and\n" +
				"not to the agent.\n")
		}
	}
	return b.String(), nil
}

// renderArgs lays out a call's arguments one key at a time, with string
// values shown in full as indented blocks.
func renderArgs(raw json.RawMessage) string {
	var args map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil || len(args) == 0 {
		return "  (none)\n"
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(args)) {
		var s string
		if json.Unmarshal(args[k], &s) == nil && strings.ContainsAny(s, "\n") {
			fmt.Fprintf(&b, "  %s:\n%s", visible(k), block(s))
			continue
		}
		fmt.Fprintf(&b, "  %s: %s\n", visible(k), visible(string(args[k])))
	}
	return b.String()
}

// block indents text behind a bar, so that nothing in it can pass for part
// of the screen around it. It is the only place where untrusted text may
// span lines.
func block(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(&b, "    | %s\n", visible(line))
	}
	return b.String()
}

// visible makes every character of untrusted text show up as itself, on
// one line. Line breaks, control characters, terminal escape sequences and
// invisible or direction-changing characters are written out as escapes,
// so text cannot start a line of its own, repaint the screen or hide part
// of itself from the person reading it.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString("    ")
		case r <= 0xFFFF:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	return b.String()
}
