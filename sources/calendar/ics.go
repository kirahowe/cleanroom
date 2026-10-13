package main

import (
	"strings"
	"time"
	"unicode/utf8"
)

const (
	prodID       = "-//cleanroom//calendar-source//EN"
	maxLineBytes = 75 // RFC 5545 section 3.1: content lines are folded at 75 octets.
	icsDate      = "20060102"
	icsStamp     = "20060102T150405Z"
)

// renderICS builds the full text of a calendar file. All events are
// all-day events. Every line ends in CRLF.
func renderICS(events []storedEvent) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(fold(s)); b.WriteString("\r\n") }

	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:" + prodID)
	line("CALSCALE:GREGORIAN")
	for _, e := range events {
		start, _ := time.Parse(time.DateOnly, e.Date)
		last, _ := time.Parse(time.DateOnly, e.EndDate)
		line("BEGIN:VEVENT")
		line("UID:" + e.UID)
		line("DTSTAMP:" + e.Created.UTC().Format(icsStamp))
		line("DTSTART;VALUE=DATE:" + start.Format(icsDate))
		// DTEND is exclusive, so it is the day after the last day.
		line("DTEND;VALUE=DATE:" + last.AddDate(0, 0, 1).Format(icsDate))
		line("SUMMARY:" + escapeText(e.Title))
		if e.Location != "" {
			line("LOCATION:" + escapeText(e.Location))
		}
		if e.Description != "" {
			line("DESCRIPTION:" + escapeText(e.Description))
		}
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}

// escapeText escapes a TEXT value (RFC 5545 section 3.3.11). Line breaks
// of any kind become \n and other control characters are dropped, so a
// value can never start a new content line.
func escapeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' || c == ';' || c == ',':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			b.WriteString(`\n`)
		case c == '\n':
			b.WriteString(`\n`)
		case c < 0x20 && c != '\t', c == 0x7f:
			// Drop control characters.
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// fold splits a line into physical lines of at most 75 octets. Each
// continuation starts with one space. A UTF-8 sequence is never split.
func fold(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	var b strings.Builder
	limit := maxLineBytes
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		limit = maxLineBytes - 1 // the leading space counts toward the limit
	}
	b.WriteString(s)
	return b.String()
}
