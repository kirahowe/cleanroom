package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
)

const imapQuerySyntax = `Every term must match. Filters: from:, to:, subject:, since:YYYY-MM-DD and before:YYYY-MM-DD. ` +
	`Quote a phrase to keep its words together, as in subject:"family reunion". ` +
	`Snippets are empty; read a message to see its text.`

// term is one piece of a query. A term that started with a quote is plain
// text even if it contains a colon.
type term struct {
	text   string
	quoted bool
}

// parseQuery turns a search query into IMAP SEARCH criteria. A message has
// to match every term. IMAP matches substrings without regard to case, so
// there is no ranking and no stemming.
//
// The query is untrusted. It becomes values in the criteria and never a
// part of the command: an unknown filter is searched for as text.
func parseQuery(query string) (*imap.SearchCriteria, error) {
	terms, err := splitTerms(query)
	if err != nil {
		return nil, err
	}
	if len(terms) == 0 {
		return nil, errors.New("query has no search terms")
	}
	criteria := &imap.SearchCriteria{}
	for _, t := range terms {
		key, value, isFilter := strings.Cut(t.text, ":")
		key = strings.ToLower(key)
		switch {
		case t.quoted || !isFilter:
			criteria.Text = append(criteria.Text, t.text)
		case key == "from" || key == "to" || key == "subject":
			if value == "" {
				return nil, fmt.Errorf("%s: needs a value", key)
			}
			criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: key, Value: value})
		case key == "since" || key == "before":
			day, err := time.Parse(time.DateOnly, value)
			if err != nil {
				return nil, fmt.Errorf("%s: needs a date such as 2026-03-01", key)
			}
			if key == "since" {
				criteria.Since = day
			} else {
				criteria.Before = day
			}
		default:
			criteria.Text = append(criteria.Text, t.text)
		}
	}
	return criteria, nil
}

// splitTerms splits a query at white space, except inside double quotes.
// The quotes are dropped. A quote that is never closed runs to the end.
func splitTerms(query string) ([]term, error) {
	var (
		terms   []term
		current strings.Builder
		open    bool // a term is being collected
		quoted  bool // it started with a quote
		inQuote bool
	)
	flush := func() {
		if open && current.Len() > 0 {
			terms = append(terms, term{text: current.String(), quoted: quoted})
		}
		current.Reset()
		open, quoted = false, false
	}
	for _, r := range query {
		switch {
		case r == '"':
			if !open {
				quoted = true
			}
			open = true
			inQuote = !inQuote
		case unicode.IsSpace(r) && !inQuote:
			flush()
		case unicode.IsControl(r):
			return nil, errors.New("query contains a control character")
		default:
			open = true
			current.WriteRune(r)
		}
	}
	flush()
	return terms, nil
}
