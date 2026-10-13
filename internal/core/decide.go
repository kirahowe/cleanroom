package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"cleanroom/internal/typed"
)

// Verdict is the outcome of a decision.
type Verdict string

const (
	Run    Verdict = "run"    // go ahead
	Hold   Verdict = "hold"   // wait for the person to approve or deny
	Refuse Verdict = "refuse" // never
)

func (v Verdict) valid() bool { return v == Run || v == Hold || v == Refuse }

// Decision is what Decide returns. It is recorded in the log next to the
// call it is about.
type Decision struct {
	Verdict Verdict `json:"verdict"`
	// Class is the class the call was judged under.
	Class Class `json:"class,omitempty"`
	// Reasons explain the verdict and cite the events it rests on.
	Reasons []Reason `json:"reasons"`
	// Gains is what the session takes on when the call takes effect.
	Gains Labels `json:"gains"`
}

// Equal reports whether two decisions are the same. A decision read back
// from the log compares equal to the one that was written.
func (d Decision) Equal(o Decision) bool {
	return d.Verdict == o.Verdict && d.Class == o.Class && d.Gains == o.Gains &&
		slices.EqualFunc(d.Reasons, o.Reasons, func(a, b Reason) bool {
			return a.Text == b.Text && slices.Equal(a.Events, b.Events)
		})
}

// Reason is one sentence of explanation, with the Seq of each event that
// made it true.
//
// Every word of Text is cleanroom's. It may name an upstream or a tool that
// is in the policy, and it never repeats a name, a type or any other string
// that a caller supplied. Reasons are printed by "cleanroom explain" and on
// approval screens, to readers other than the session that made the call,
// and a refused call can hold anything its caller chose to put in it.
type Reason struct {
	Text   string `json:"text"`
	Events []int  `json:"events,omitempty"`
}

// Limits on a call. Anything larger is refused.
const (
	MaxArgsBytes    = 256 << 10
	MaxQuestionLen  = 2000
	MaxReaderCalls  = 40 // tool calls one reader may make for one ask
	maxReturnsBytes = 16 << 10
)

// Decide is the whole policy decision. It is a pure function: the same
// policy, log and call always give the same decision, so any decision in
// the log can be replayed and explained later. It does no I/O, and nothing
// that depends on a call's arguments may happen before it has been called.
//
// Anything Decide does not recognise is refused.
func Decide(p Policy, log []Event, c Call) Decision {
	if err := CheckLog(log); err != nil {
		return refuse("the event log is malformed: " + err.Error())
	}
	if c.Invalid != "" {
		return refuse("the call is malformed: " + c.Invalid)
	}
	if askSeq, ok := readerAsk(c.Session); ok {
		return decideReader(p, log, c, askSeq)
	}
	if !ValidSession(c.Session) {
		return refuse("the session id is not valid")
	}
	switch c.Op {
	case OpTool:
		return decideTool(p, log, c)
	case OpAsk:
		return decideAsk(p, log, c)
	case OpReadAnswer:
		return decideReadAnswer(p, log, c)
	}
	return refuse("unknown operation")
}

func decideTool(p Policy, log []Event, c Call) Decision {
	up, ok := p.Upstreams[c.Upstream]
	if !ok {
		return refuse("there is no upstream of that name")
	}
	class, ok := ClassOf(up, c.Tool, c.Hint)
	if !ok {
		return refuse("the tool has no class: the policy has no entry for it and the server gave no usable annotations")
	}
	if !validArgs(c.Args) {
		return refuse("the arguments are not a JSON object of acceptable size")
	}
	if class == Read && up.AskOnly {
		return refuse(fmt.Sprintf("%q can only be read through ask", c.Upstream))
	}

	st := StateOf(p, log, c.Session)
	d := Decision{Class: class, Gains: ResultLabels(p, log, c)}
	switch {
	case up.Tools[c.Tool].AlwaysHold:
		d.Verdict = Hold
		d.Reasons = []Reason{{Text: fmt.Sprintf("the policy holds every call to %s.%s", c.Upstream, c.Tool)}}
	case class == Act && st.Labels.Contaminated:
		d.Verdict = Hold
		d.Reasons = []Reason{{
			Text:   "this call acts for the user, and the session is contaminated, so it may be following instructions from content it read",
			Events: st.ContaminatedBy,
		}}
	case class == Egress && st.Labels.Contaminated && st.Labels.Private:
		d.Verdict = Hold
		d.Reasons = []Reason{
			{Text: "this call can send data out, and the session is contaminated, so it may be following instructions from content it read", Events: st.ContaminatedBy},
			{Text: "the session holds private data that the call could leak", Events: st.PrivateBy},
		}
	default:
		d.Verdict = Run
		d.Reasons = []Reason{{Text: fmt.Sprintf("a %s call from a session that is %s", class, st.Labels)}}
	}
	return d
}

// ClassOf resolves a tool's class: the policy entry if there is one,
// otherwise the hint taken from the server's annotations. It reports false
// when neither gives a class, and such a tool is refused.
//
// Hints are not used for an upstream that has labels. A source returns
// untrusted content, and its server may be no more trustworthy, so a source
// cannot be the one to say that its own tools are harmless.
func ClassOf(up Upstream, tool string, hint Class) (Class, bool) {
	if t, ok := up.Tools[tool]; ok && t.Class != "" {
		return t.Class, t.Class.valid()
	}
	if up.Labels != (Labels{}) {
		return "", false
	}
	return hint, hint.valid()
}

// ResultLabels is what the caller of a tool takes on: whatever the
// upstream carries now, or nothing if cleanroom discards the result.
func ResultLabels(p Policy, log []Event, c Call) Labels {
	if p.Upstreams[c.Upstream].Tools[c.Tool].DiscardResult {
		return Labels{}
	}
	return SourceLabels(p, log, c.Upstream)
}

// validArgs reports whether raw is a JSON object that means one thing
// only. An object that repeats a key, or has two keys that differ only in
// case, is refused: the approval screen would show one value and an
// upstream might use the other.
func validArgs(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true // no arguments
	}
	if len(raw) > MaxArgsBytes {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return false
	}
	return !ambiguousKeys(json.NewDecoder(bytes.NewReader(raw)))
}

// ambiguousKeys walks one JSON value and reports whether any object in it
// has two keys that are equal when case is ignored.
func ambiguousKeys(dec *json.Decoder) bool {
	tok, err := dec.Token()
	if err != nil {
		return true
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return true
			}
			folded := strings.ToLower(key.(string))
			if seen[folded] {
				return true
			}
			seen[folded] = true
			if ambiguousKeys(dec) {
				return true
			}
		}
		_, err := dec.Token() // the closing brace
		return err != nil
	case json.Delim('['):
		for dec.More() {
			if ambiguousKeys(dec) {
				return true
			}
		}
		_, err := dec.Token() // the closing bracket
		return err != nil
	}
	return false
}

func decideAsk(p Policy, log []Event, c Call) Decision {
	up, ok := p.Upstreams[c.Upstream]
	if !ok {
		return refuse("there is no source of that name")
	}
	if len(up.Reader) == 0 {
		return refuse(fmt.Sprintf("source %q has no reader configured", c.Upstream))
	}
	if c.Question == "" || len(c.Question) > MaxQuestionLen || !utf8.ValidString(c.Question) {
		return refuse(fmt.Sprintf("the question must be between 1 and %d bytes of text", MaxQuestionLen))
	}
	if len(c.Returns) > maxReturnsBytes {
		return refuse("the returns type is too large")
	}
	spec, err := typed.ParseSpec(c.Returns)
	if err != nil {
		return refuse(err.Error())
	}

	// A question is data leaving the session: the reader passes it on to
	// the source as search terms, or as a URL if the source can fetch.
	// A session that may be following someone else's instructions and
	// holds private data must not have that way out.
	source := SourceLabels(p, log, c.Upstream)
	st := StateOf(p, log, c.Session)
	if st.Labels.Contaminated && st.Labels.Private && readerCanSendOut(up, source) {
		return Decision{Verdict: Refuse, Reasons: []Reason{
			{Text: fmt.Sprintf("a reader of %q can send data out, and the question would carry it; the session is contaminated", c.Upstream), Events: st.ContaminatedBy},
			{Text: "the session holds private data that the question could leak", Events: st.PrivateBy},
		}}
	}

	d := Decision{Verdict: Run, Class: Read}
	switch {
	case !spec.IsText():
		// A typed value cannot carry instructions, so the caller stays
		// uncontaminated. It can still be private.
		d.Gains = Labels{Private: source.Private}
		d.Reasons = []Reason{{Text: "the answer is a typed value, which cannot carry instructions"}}
	case !source.Contaminated:
		d.Gains = source
		d.Reasons = []Reason{{Text: "the source is not contaminated, so its text can be returned"}}
	case c.AcceptContamination:
		d.Gains = source
		d.Reasons = []Reason{{Text: "the caller chose to receive untrusted text and takes on the labels of the source"}}
	default:
		d.Reasons = []Reason{{Text: "free text goes to the person; the caller receives only an id"}}
	}
	return d
}

func decideReadAnswer(p Policy, log []Event, c Call) Decision {
	if _, ok := AskAt(log, c.Answer); !ok {
		return refuse(fmt.Sprintf("%d is not an ask", c.Answer))
	}
	ans, ok := AnswerTo(log, c.Answer)
	if !ok {
		return refuse(fmt.Sprintf("ask %d has not been answered yet", c.Answer))
	}
	if ans.Outcome == OutcomeNone {
		return refuse(fmt.Sprintf("ask %d has no answer", c.Answer))
	}
	// An answer carries its own labels, whatever its type. A typed
	// answer is contaminated only if the session that asked for it was,
	// since that session chose the question, the choices and the field
	// names.
	if ans.Labels.Contaminated && !c.AcceptContamination {
		return refuse("this answer is untrusted; reading it contaminates the session for good, so it needs accept_contamination: true")
	}
	return Decision{
		Verdict: Run, Class: Read,
		Gains:   ans.Labels,
		Reasons: []Reason{{Text: "the caller reads a stored answer and takes on its labels", Events: []int{c.Answer}}},
	}
}

// decideReader decides a call made by a reader. A reader is assumed to be
// compromised, so these rules are all that keeps it harmless: it may use
// the tools of the one source it was started for, and only tools that
// cannot change anything or, if the source is private, send anything out.
func decideReader(p Policy, log []Event, c Call, askSeq int) Decision {
	ask, ok := AskAt(log, askSeq)
	if !ok || ask.Decision.Verdict != Run {
		return refuse("this reader does not belong to an ask that was allowed")
	}
	if _, answered := AnswerTo(log, askSeq); answered {
		return refuse("this reader's ask has already been answered")
	}
	if c.Op != OpTool {
		return refuse("a reader can only call the tools of its source")
	}
	if c.Upstream != ask.Call.Upstream {
		return refuse(fmt.Sprintf("this reader was started for %q and cannot reach another upstream", ask.Call.Upstream))
	}
	up, ok := p.Upstreams[c.Upstream]
	if !ok {
		return refuse("there is no upstream of that name")
	}
	class, ok := ClassOf(up, c.Tool, c.Hint)
	if !ok {
		return refuse("the tool has no class")
	}
	if !validArgs(c.Args) {
		return refuse("the arguments are not a JSON object of acceptable size")
	}
	if up.Tools[c.Tool].AlwaysHold {
		return refuse(fmt.Sprintf("the policy holds every call to %s.%s, and a reader has nobody to ask", c.Upstream, c.Tool))
	}
	source := SourceLabels(p, log, c.Upstream)
	switch {
	case class == Read:
	case readerMayUse(up, c.Tool, source):
		// A reader of a public source may fetch: it holds nothing
		// private to leak.
	default:
		return refuse(fmt.Sprintf("a reader of %q may not make a %s call", c.Upstream, class))
	}
	if ReaderSpent(log, c.Session) {
		return refuse(fmt.Sprintf("this reader has used its %d calls", MaxReaderCalls))
	}
	return Decision{
		Verdict: Run, Class: class, Gains: source,
		Reasons: []Reason{{Text: "a reader using a read tool of its own source", Events: []int{askSeq}}},
	}
}

// ReaderSpent reports whether a reader session has used all its calls,
// counting refused ones. The shell stops recording a spent reader's calls,
// so that a reader cannot grow the log, or move the numbers later events
// get, by more than its budget.
func ReaderSpent(log []Event, session string) bool {
	if _, ok := readerAsk(session); !ok {
		return false
	}
	used := 0
	for _, e := range log {
		if e.Kind == KindCall && e.Call.Call.Session == session {
			if used++; used >= MaxReaderCalls {
				return true
			}
		}
	}
	return false
}

// readerMayUse reports whether a reader may call an egress tool of its
// source. Only when the policy itself classes the tool egress, and only
// when the source is not private: a reader that holds private content and
// can send data out would have the whole trifecta to itself.
func readerMayUse(up Upstream, tool string, source Labels) bool {
	return up.Tools[tool].Class == Egress && !source.Private
}

// readerCanSendOut reports whether a reader of this source has any way to
// send data out.
func readerCanSendOut(up Upstream, source Labels) bool {
	for tool := range up.Tools {
		if readerMayUse(up, tool, source) {
			return true
		}
	}
	return false
}

func refuse(text string) Decision {
	return Decision{Verdict: Refuse, Reasons: []Reason{{Text: text}}}
}

// Sanitize prepares a call for Decide and for the log, which every session
// shares and which the person reads. After it, every name in the call is
// short and printable, every piece of JSON parses and is compact, and
// nothing is larger than its limit. Whatever did not meet that is dropped,
// and the call is marked invalid, which Decide refuses.
//
// Compacting matters for the hash: a call must read back from the log byte
// for byte as it was stored.
func Sanitize(c Call) Call {
	invalid := func(why string) {
		if c.Invalid == "" {
			c.Invalid = why
		}
	}
	if _, isReader := readerAsk(c.Session); !isReader && !ValidSession(c.Session) {
		c.Session = ""
		invalid("the session id is not valid")
	}
	if c.Upstream != "" && !upstreamName.MatchString(c.Upstream) {
		c.Upstream = ""
		invalid("the upstream name is not valid")
	}
	if c.Tool != "" && !toolName.MatchString(c.Tool) {
		c.Tool = ""
		invalid("the tool name is not valid")
	}
	if !c.Op.valid() {
		c.Op = ""
		invalid("the operation is not one cleanroom knows")
	}
	if c.Hint != "" && !c.Hint.valid() {
		c.Hint = ""
	}
	if len(c.Question) > MaxQuestionLen || !utf8.ValidString(c.Question) {
		c.Question = ""
		invalid(fmt.Sprintf("the question must be at most %d bytes of text", MaxQuestionLen))
	}
	var ok bool
	if c.Args, ok = compact(c.Args, MaxArgsBytes); !ok {
		invalid(fmt.Sprintf("the arguments are not valid JSON of at most %d bytes", MaxArgsBytes))
	}
	if c.Returns, ok = compact(c.Returns, maxReturnsBytes); !ok {
		invalid("the returns type is not valid JSON of acceptable size")
	}
	return c
}

// compact returns raw in the exact form the log stores it in, which is what
// encoding/json writes for a RawMessage: no insignificant whitespace, and
// "<", ">" and "&" escaped. Using that form from the start means the size
// that is checked, the bytes that are hashed and the bytes that are sent
// are the same before and after a trip through the log.
func compact(raw json.RawMessage, limit int) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if len(raw) > limit {
		return nil, false
	}
	stored, err := json.Marshal(raw)
	if err != nil || len(stored) > limit {
		return nil, false
	}
	return stored, true
}

// Record builds the log event for a sanitized call and its decision. The
// log assigns Seq and Time when the event is appended.
func Record(p Policy, log []Event, c Call, d Decision) Event {
	return Event{Kind: KindCall, Call: &Decided{
		Call:     c,
		Decision: d,
		Held:     StateOf(p, log, c.Session).Labels,
		Hash:     c.Hash(),
	}}
}
