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
// width is the width of their terminal in columns, or 0 when it is unknown.
func AnswerScreen(log []core.Event, seq, width int) (string, error) {
	if seq < 1 || seq > len(log) || log[seq-1].Kind != core.KindCall || log[seq-1].Call.Call.Op != core.OpAsk {
		return "", fmt.Errorf("%d is not an ask", seq)
	}
	c := log[seq-1].Call.Call
	var b strings.Builder
	fmt.Fprintf(&b, "Ask %d to %q, from session %q\n\n", seq, visible(c.Upstream), visible(c.Session))
	fmt.Fprintf(&b, "Question\n%s\n", prose(c.Question, width))

	ans, ok := core.AnswerTo(log, seq)
	switch {
	case !ok:
		b.WriteString("No answer yet.\n")
	case ans.Outcome == core.OutcomeNone:
		fmt.Fprintf(&b, "No answer: %s.\n", visible(ans.Detail))
	case ans.Outcome == core.OutcomeTyped:
		fmt.Fprintf(&b, "Answer (a checked value): %s\n", visible(string(ans.Value)))
	default:
		b.WriteString("Answer\n")
		b.WriteString(prose(ans.Text, width))
		if ans.Labels.Contaminated {
			b.WriteString("\n" + paragraph("This text was written by a reader that read untrusted content. "+
				"It may be wrong, and it may be trying to persuade you. "+
				"It has been shown to you and not to the agent.", width))
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
	for line := range strings.SplitSeq(text, "\n") {
		fmt.Fprintf(&b, "    | %s\n", visible(line))
	}
	return b.String()
}

const (
	// proseBar starts every row of prose.
	proseBar = "  │ "
	// A screen is laid out for 80 columns when the terminal's width is
	// unknown, and never wider than 88, past which lines are hard to read.
	defaultWidth, minWidth, maxWidth = 80, 40, 88
)

// prose lays out untrusted text that is meant to be read, a question or an
// answer, behind a bar. Like block it lets the text span lines without
// letting any of it pass for the screen around it.
//
// Long lines are wrapped to the terminal's width here, so that the terminal
// has nothing left to wrap. A line the terminal wraps continues at the left
// edge, in front of the bar. That is hard to read, and text padded to the
// right length could use it to draw a line of its own there.
//
// Wrapping changes the spacing: a run of spaces becomes one, and a line
// break the writer made looks like one made here. Use block for text that
// has to be shown exactly.
func prose(text string, width int) string {
	var b strings.Builder
	for line := range strings.SplitSeq(strings.Trim(text, "\n"), "\n") {
		for _, row := range wrap(visible(line), layoutWidth(width)-columns(proseBar)) {
			b.WriteString(strings.TrimRight(proseBar+row, " ") + "\n")
		}
	}
	return b.String()
}

// paragraph lays out a paragraph of cleanroom's own text.
func paragraph(text string, width int) string {
	return strings.Join(wrap(text, layoutWidth(width)), "\n") + "\n"
}

// layoutWidth is the width a screen is laid out for on a terminal of the
// given width, where 0 means unknown.
func layoutWidth(terminal int) int {
	switch {
	case terminal <= 0:
		return defaultWidth
	case terminal < minWidth:
		return minWidth
	case terminal > maxWidth:
		return maxWidth
	}
	return terminal
}

// wrap breaks one line into rows of at most width columns, at spaces where
// it can and inside a word that is longer than a row. The rows keep the
// line's indentation, so a wrapped list item stays under its first row.
func wrap(line string, width int) []string {
	indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
	indent = indent[:min(len(indent), width/2)]
	var rows []string
	row, used := indent, len(indent)
	flush := func() {
		rows = append(rows, row)
		row, used = indent, len(indent)
	}
	for _, word := range strings.Fields(line) {
		if used > len(indent) && used+1+columns(word) > width {
			flush()
		}
		if used > len(indent) {
			row, used = row+" ", used+1
		}
		for columns(word) > width-used {
			head, tail := cut(word, width-used)
			row += head
			flush()
			word = tail
		}
		row, used = row+word, used+columns(word)
	}
	return append(rows, row)
}

// cut splits a word after as many characters as fit in width columns, and
// after one when none fits.
func cut(word string, width int) (head, tail string) {
	used := 0
	for i, r := range word {
		used += columns(string(r))
		if used > width && i > 0 {
			return word[:i], word[i:]
		}
	}
	return word, ""
}

// columns is how wide s is drawn, at most. Characters from Hangul Jamo on
// are counted as two columns. Many of them take one, and counting those as
// two only wraps a line early.
func columns(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x1100 {
			n++
		}
	}
	return n
}

// invisible lists characters that count as printable but draw nothing, or
// change how their neighbours are drawn: fillers, variation selectors and
// the like.
var invisible = []*unicode.RangeTable{
	unicode.Other_Default_Ignorable_Code_Point,
	unicode.Variation_Selector,
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
		case unicode.IsPrint(r) && !unicode.In(r, invisible...):
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
