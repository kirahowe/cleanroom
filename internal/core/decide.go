package core

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// Reason is one sentence of explanation, with the Seq of each event that
// made it true.
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
	return refuse(fmt.Sprintf("unknown operation %q", c.Op))
}

func decideTool(p Policy, log []Event, c Call) Decision {
	up, ok := p.Upstreams[c.Upstream]
	if !ok {
		return refuse(fmt.Sprintf("there is no upstream named %q", c.Upstream))
	}
	class, ok := ClassOf(up, c.Tool, c.Hint)
	if !ok {
		return refuse(fmt.Sprintf("tool %q has no class: the policy has no entry for it and the server gave no usable annotations", c.Tool))
	}
	if !validArgs(c.Args) {
		return refuse("the arguments are not a JSON object of acceptable size")
	}
	if class == Read && up.AskOnly {
		return refuse(fmt.Sprintf("%q can only be read through ask", c.Upstream))
	}

	st := StateOf(p, log, c.Session)
	d := Decision{Class: class, Gains: resultLabels(p, log, c)}
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
func ClassOf(up Upstream, tool string, hint Class) (Class, bool) {
	if t, ok := up.Tools[tool]; ok && t.Class != "" {
		return t.Class, t.Class.valid()
	}
	return hint, hint.valid()
}

// resultLabels is what the result of a tool call carries.
func resultLabels(p Policy, log []Event, c Call) Labels {
	if l := p.Upstreams[c.Upstream].Tools[c.Tool].Labels; l != nil {
		return *l
	}
	return SourceLabels(p, log, c.Upstream)
}

func validArgs(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true // no arguments
	}
	if len(raw) > MaxArgsBytes {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

func decideAsk(p Policy, log []Event, c Call) Decision {
	up, ok := p.Upstreams[c.Upstream]
	if !ok {
		return refuse(fmt.Sprintf("there is no source named %q", c.Upstream))
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

	source := SourceLabels(p, log, c.Upstream)
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
	ask, ok := callAt(log, c.Answer)
	if !ok || ask.Call.Op != OpAsk {
		return refuse(fmt.Sprintf("%d is not an ask", c.Answer))
	}
	ans, ok := answerTo(log, c.Answer)
	if !ok {
		return refuse(fmt.Sprintf("ask %d has not been answered yet", c.Answer))
	}
	switch ans.Outcome {
	case OutcomeTyped:
		return Decision{
			Verdict: Run, Class: Read,
			Gains:   Labels{Private: ans.Labels.Private},
			Reasons: []Reason{{Text: "the answer is a typed value, which cannot carry instructions"}},
		}
	case OutcomeText:
		if ans.Labels.Contaminated && !c.AcceptContamination {
			return refuse("this answer is untrusted text; reading it contaminates the session for good, so it needs accept_contamination: true")
		}
		return Decision{
			Verdict: Run, Class: Read,
			Gains:   ans.Labels,
			Reasons: []Reason{{Text: "the caller chose to read a stored answer and takes on its labels", Events: []int{c.Answer}}},
		}
	}
	return refuse(fmt.Sprintf("ask %d has no answer", c.Answer))
}

// decideReader decides a call made by a reader. A reader is assumed to be
// compromised, so these rules are all that keeps it harmless: it may use
// the tools of the one source it was started for, and only tools that
// cannot change anything or, if the source is private, send anything out.
func decideReader(p Policy, log []Event, c Call, askSeq int) Decision {
	ask, ok := callAt(log, askSeq)
	if !ok || ask.Call.Op != OpAsk || ask.Decision.Verdict != Run {
		return refuse("this reader does not belong to an ask that was allowed")
	}
	if _, answered := answerTo(log, askSeq); answered {
		return refuse("this reader's ask has already been answered")
	}
	if c.Op != OpTool {
		return refuse("a reader can only call the tools of its source")
	}
	if c.Upstream != ask.Call.Upstream {
		return refuse(fmt.Sprintf("this reader was started for %q and cannot reach %q", ask.Call.Upstream, c.Upstream))
	}
	up, ok := p.Upstreams[c.Upstream]
	if !ok {
		return refuse(fmt.Sprintf("there is no upstream named %q", c.Upstream))
	}
	class, ok := ClassOf(up, c.Tool, c.Hint)
	if !ok {
		return refuse(fmt.Sprintf("tool %q has no class", c.Tool))
	}
	if !validArgs(c.Args) {
		return refuse("the arguments are not a JSON object of acceptable size")
	}
	source := SourceLabels(p, log, c.Upstream)
	switch {
	case class == Read:
	case class == Egress && !source.Private:
		// A reader of a public source may fetch: it holds nothing
		// private to leak.
	default:
		return refuse(fmt.Sprintf("a reader of %q may not make a %s call", c.Upstream, class))
	}
	used := 0
	for _, e := range log {
		if e.Kind == KindCall && e.Call.Call.Session == c.Session {
			used++
		}
	}
	if used >= MaxReaderCalls {
		return refuse(fmt.Sprintf("this reader has used its %d calls", MaxReaderCalls))
	}
	return Decision{
		Verdict: Run, Class: class, Gains: source,
		Reasons: []Reason{{Text: "a reader using a read tool of its own source", Events: []int{askSeq}}},
	}
}

func refuse(text string) Decision {
	return Decision{Verdict: Refuse, Reasons: []Reason{{Text: text}}}
}

// Sanitize prepares a call for Decide and for the log. It compacts the JSON
// in the call, so that the call reads back from the log byte for byte as it
// was stored and its hash stays the same. JSON that does not parse cannot be
// stored at all, so it is dropped and the call is marked invalid, which
// Decide refuses.
func Sanitize(c Call) Call {
	var ok bool
	if c.Args, ok = compact(c.Args); !ok {
		c.Invalid = "the arguments are not valid JSON"
	}
	if c.Returns, ok = compact(c.Returns); !ok {
		c.Invalid = "the returns type is not valid JSON"
	}
	return c
}

func compact(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, false
	}
	return b.Bytes(), true
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
