package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestParseQuery(t *testing.T) {
	header := func(key, value string) imap.SearchCriteriaHeaderField {
		return imap.SearchCriteriaHeaderField{Key: key, Value: value}
	}
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	for query, want := range map[string]imap.SearchCriteria{
		"reunion":                    {Text: []string{"reunion"}},
		"  family   reunion ":        {Text: []string{"family", "reunion"}},
		`"family reunion"`:           {Text: []string{"family reunion"}},
		"from:ann@example.com lunch": {Header: []imap.SearchCriteriaHeaderField{header("from", "ann@example.com")}, Text: []string{"lunch"}},
		`SUBJECT:"family reunion"`:   {Header: []imap.SearchCriteriaHeaderField{header("subject", "family reunion")}},
		"to:bob subject:hi":          {Header: []imap.SearchCriteriaHeaderField{header("to", "bob"), header("subject", "hi")}},
		"since:2026-03-01 before:2026-04-01 invoice": {
			Since: day(2026, 3, 1), Before: day(2026, 4, 1), Text: []string{"invoice"},
		},
		// Not a filter this back end knows, so it is text to look for.
		"is:unread":           {Text: []string{"is:unread"}},
		"https://example.com": {Text: []string{"https://example.com"}},
		// A quoted term is text even when it looks like a filter.
		`"from:ann"`:     {Text: []string{"from:ann"}},
		`"never closed`:  {Text: []string{"never closed"}},
		"line one\nline": {Text: []string{"line", "one", "line"}},
		"réunion":        {Text: []string{"réunion"}},
	} {
		got, err := parseQuery(query)
		if err != nil {
			t.Errorf("parseQuery(%q): %v", query, err)
			continue
		}
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("parseQuery(%q) = %+v, want %+v", query, *got, want)
		}
	}
}

func TestParseQueryErrors(t *testing.T) {
	for _, query := range []string{
		"",
		"   ",
		`""`,
		"from:",
		"since:yesterday",
		"before:2026-13-01",
		"a\x00b",
		"a\x1b[2Jb",
	} {
		if got, err := parseQuery(query); err == nil {
			t.Errorf("parseQuery(%q) = %+v, want an error", query, *got)
		}
	}
}
