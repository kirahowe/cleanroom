package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// unfold joins folded lines and splits the result into logical lines.
func unfold(t testing.TB, ics string) []string {
	t.Helper()
	if !strings.HasSuffix(ics, "\r\n") {
		t.Fatalf("output does not end in CRLF: %q", ics)
	}
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSuffix(ics, "\r\n"), "\r\n") {
		if strings.ContainsAny(l, "\r\n") {
			t.Fatalf("bare CR or LF inside a line: %q", l)
		}
		if len(l) > maxLineBytes {
			t.Fatalf("line of %d octets: %q", len(l), l)
		}
		if strings.HasPrefix(l, " ") && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
		} else {
			lines = append(lines, l)
		}
	}
	return lines
}

// unescape reverses TEXT escaping.
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == 'n' {
				b.WriteByte('\n')
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// wantText is the text escapeText promises to preserve: line breaks
// become \n and other control characters are dropped.
func wantText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func FuzzRenderICS(f *testing.F) {
	f.Add("Meeting", "Room 1", "Agenda")
	f.Add("a,b;c\\d", "", "x\r\ny")
	f.Add("x\r\nEND:VEVENT\r\nBEGIN:VEVENT", "L\nSUMMARY:evil", "")
	f.Add(strings.Repeat("日本語", 40), "", strings.Repeat("é", 100))
	f.Add("\\", "\\\\n", "\r")

	f.Fuzz(func(t *testing.T, title, location, description string) {
		ev := storedEvent{
			Event: Event{
				UID: "id@cleanroom", Title: title, Date: "2026-03-10", EndDate: "2026-03-10",
				Location: location, Description: description,
			},
			Created: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		}
		out := renderICS([]storedEvent{ev})
		validInput := utf8.ValidString(title) && utf8.ValidString(location) && utf8.ValidString(description)
		if validInput && !utf8.ValidString(out) {
			t.Fatalf("output is not valid UTF-8: %q", out)
		}

		var begins, summaries int
		var summary string
		for _, l := range unfold(t, out) {
			switch {
			case l == "BEGIN:VEVENT":
				begins++
			case strings.HasPrefix(l, "SUMMARY:"):
				summaries++
				summary = strings.TrimPrefix(l, "SUMMARY:")
			}
		}
		if begins != 1 || summaries != 1 {
			t.Fatalf("VEVENTs = %d, SUMMARYs = %d, want 1 each", begins, summaries)
		}
		if validInput && unescape(summary) != wantText(title) {
			t.Fatalf("SUMMARY = %q, want %q", unescape(summary), wantText(title))
		}
	})
}
