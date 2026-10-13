// Package typed validates reader answers against the caller's requested type.
//
// Check returns a value for cleanroom to serialise. Output is restricted to
// the type's grammar and caller-supplied choices and field names. Validation
// checks structure, not accuracy.
//
// The package is pure: no I/O, no clock, no globals that change.
package typed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

// Type names a kind of answer a caller can ask for.
type Type string

const (
	Date     Type = "date"     // a calendar day, YYYY-MM-DD
	DateTime Type = "datetime" // an instant, RFC 3339, in UTC
	Number   Type = "number"   // a plain decimal number
	Money    Type = "money"    // an amount and an ISO 4217 currency code
	Boolean  Type = "boolean"  // true or false
	Choice   Type = "choice"   // one of the strings the caller listed
	Record   Type = "record"   // named fields, each of one of the types above
	Text     Type = "text"     // free text: never returned as a typed value
)

// Limits on what a caller may ask for and what a reader may send back.
const (
	MaxChoices   = 32
	MaxChoiceLen = 64
	MaxFields    = 16
	MaxAnswerLen = 64 << 10 // bytes of reader output considered at all
	MaxTextRunes = 4000
	minYear      = 1900
	maxYear      = 2200
)

// Spec is the type of answer a caller wants from ask.
type Spec struct {
	Type    Type            `json:"type"`
	Choices []string        `json:"choices,omitempty"` // for Choice
	Fields  map[string]Spec `json:"fields,omitempty"`  // for Record
}

// IsText reports whether the spec asks for free text, which Check never
// accepts.
func (s Spec) IsText() bool { return s.Type == Text }

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// UnmarshalJSON accepts a bare type name such as "date", or an object such
// as {"type":"choice","choices":[...]}. Unknown keys are an error.
func (s *Spec) UnmarshalJSON(raw []byte) error {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		*s = Spec{Type: Type(name)}
		return nil
	}
	type plain Spec // drops this method, so decoding does not recurse forever
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode((*plain)(s))
}

// ParseSpec reads and validates the spec from a tool argument. Anything it
// does not recognise is an error. The error never repeats part of raw: it
// becomes the reason a call was refused, which is shown to others.
func ParseSpec(raw json.RawMessage) (Spec, error) {
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return Spec{}, errors.New("returns must be a type name or a {type, ...} object")
	}
	return validate(s, true)
}

func validate(s Spec, topLevel bool) (Spec, error) {
	if s.Type != Choice && s.Choices != nil {
		return Spec{}, errors.New("only a choice takes choices")
	}
	if s.Type != Record && s.Fields != nil {
		return Spec{}, errors.New("only a record takes fields")
	}
	switch s.Type {
	case Date, DateTime, Number, Money, Boolean:
		return s, nil
	case Text:
		if !topLevel {
			return Spec{}, errors.New("a record field cannot be free text")
		}
		return s, nil
	case Choice:
		if len(s.Choices) == 0 || len(s.Choices) > MaxChoices {
			return Spec{}, fmt.Errorf("choice needs between 1 and %d choices", MaxChoices)
		}
		seen := map[string]bool{}
		for _, c := range s.Choices {
			if c == "" || len(c) > MaxChoiceLen || !utf8.ValidString(c) || seen[c] {
				return Spec{}, fmt.Errorf("choices must be distinct, non-empty and at most %d bytes", MaxChoiceLen)
			}
			seen[c] = true
		}
		return s, nil
	case Record:
		if !topLevel {
			return Spec{}, errors.New("a record field cannot itself be a record")
		}
		if len(s.Fields) == 0 || len(s.Fields) > MaxFields {
			return Spec{}, fmt.Errorf("record needs between 1 and %d fields", MaxFields)
		}
		for name, f := range s.Fields {
			if !fieldName.MatchString(name) {
				return Spec{}, fmt.Errorf("a field name must match %s", fieldName)
			}
			if _, err := validate(f, false); err != nil {
				return Spec{}, fmt.Errorf("a record field is not valid: %w", err)
			}
		}
		return s, nil
	default:
		return Spec{}, errors.New("unknown type: use date, datetime, number, money, boolean, text, choice or record")
	}
}

// Check tests a reader's raw answer against spec. On success it returns the
// value re-serialised by cleanroom as canonical JSON. It returns false when
// the answer is null, malformed, of the wrong shape or out of range; the
// caller is then told only that there is no answer.
//
// A text spec always fails here. Free text is handled by CheckText and never
// reaches a caller as a typed value.
func Check(spec Spec, raw []byte) (json.RawMessage, bool) {
	v, ok := decode(raw)
	if !ok || v == nil {
		return nil, false
	}
	out, ok := check(spec, v)
	if !ok {
		return nil, false
	}
	return json.RawMessage(out), true
}

// CheckText accepts a reader's free-text answer: a JSON string of bounded
// length. The text is still untrusted. Whoever receives it must be labelled
// contaminated.
func CheckText(raw []byte) (string, bool) {
	v, ok := decode(raw)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" || utf8.RuneCountInString(s) > MaxTextRunes {
		return "", false
	}
	return s, true
}

// decode parses exactly one JSON value, keeping numbers as their literal
// text so that no float conversion can change them.
func decode(raw []byte) (any, bool) {
	if len(raw) > MaxAnswerLen {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	// Nothing may follow the value. (dec.More is not enough: it reports
	// false before a stray closing bracket.)
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

var (
	dateShape = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	// A plain decimal: no exponent, no leading zeros, bounded digits. The
	// bounds keep a number from being used to smuggle a long message.
	numberShape = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]{1,6})?$`)
	amountShape = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,4})?$`)
)

func check(spec Spec, v any) (string, bool) {
	switch spec.Type {
	case Date:
		s, ok := v.(string)
		if !ok || !dateShape.MatchString(s) {
			return "", false
		}
		t, err := time.Parse(time.DateOnly, s)
		if err != nil || !yearInRange(t) {
			return "", false
		}
		return quote(t.Format(time.DateOnly)), true

	case DateTime:
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil || !yearInRange(t) {
			return "", false
		}
		// Whole seconds, in UTC. Fractional digits and the choice of
		// offset would be free bits for a reader to signal with.
		return quote(t.UTC().Truncate(time.Second).Format(time.RFC3339)), true

	case Number:
		n, ok := v.(json.Number)
		if !ok || !numberShape.MatchString(n.String()) {
			return "", false
		}
		return n.String(), true

	case Money:
		m, ok := v.(map[string]any)
		if !ok || len(m) != 2 {
			return "", false
		}
		amount, ok := decimalText(m["amount"])
		if !ok || !amountShape.MatchString(amount) {
			return "", false
		}
		currency, ok := m["currency"].(string)
		if !ok || !currencies[currency] {
			return "", false
		}
		return `{"amount":` + quote(amount) + `,"currency":` + quote(currency) + `}`, true

	case Boolean:
		b, ok := v.(bool)
		if !ok {
			return "", false
		}
		if b {
			return "true", true
		}
		return "false", true

	case Choice:
		s, ok := v.(string)
		if !ok || !slices.Contains(spec.Choices, s) {
			return "", false
		}
		return quote(s), true

	case Record:
		m, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		for name := range m {
			if _, known := spec.Fields[name]; !known {
				return "", false
			}
		}
		names := slices.Sorted(maps.Keys(spec.Fields))

		var b bytes.Buffer
		answered := false
		b.WriteByte('{')
		for i, name := range names {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quote(name) + ":")
			fv := m[name]
			if fv == nil { // missing or null: this field has no answer
				b.WriteString("null")
				continue
			}
			out, ok := check(spec.Fields[name], fv)
			if !ok {
				return "", false
			}
			b.WriteString(out)
			answered = true
		}
		b.WriteByte('}')
		return b.String(), answered
	}
	return "", false
}

// decimalText accepts an amount written as a JSON number or as a string.
func decimalText(v any) (string, bool) {
	switch a := v.(type) {
	case json.Number:
		return a.String(), true
	case string:
		return a, true
	}
	return "", false
}

func yearInRange(t time.Time) bool {
	return t.Year() >= minYear && t.Year() <= maxYear
}

func quote(s string) string {
	b, _ := json.Marshal(s) // a string always marshals
	return string(b)
}
