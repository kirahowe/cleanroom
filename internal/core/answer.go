package core

import (
	"fmt"

	"cleanroom/internal/typed"
)

// Conclude turns a reader's raw output into the answer event for the ask at
// askSeq. The reader is not trusted: raw is checked against the type the
// caller asked for, and anything that does not fit becomes "no answer".
//
// A non-empty failure means the reader produced nothing usable (it crashed
// or ran out of time), and is recorded as the detail.
func Conclude(p Policy, log []Event, askSeq int, raw []byte, failure string) (Event, error) {
	ask, ok := callAt(log, askSeq)
	if !ok || ask.Call.Op != OpAsk || ask.Decision.Verdict != Run {
		return Event{}, fmt.Errorf("%d is not an ask that was allowed", askSeq)
	}
	if _, answered := answerTo(log, askSeq); answered {
		return Event{}, fmt.Errorf("ask %d has already been answered", askSeq)
	}
	spec, err := typed.ParseSpec(ask.Call.Returns)
	if err != nil {
		return Event{}, err // Decide refuses such an ask, so it cannot be in the log
	}

	// The answer was written by a reader that read the source, so it
	// carries what the source carries now.
	source := SourceLabels(p, log, ask.Call.Upstream)
	a := &Answer{Ref: askSeq, Outcome: OutcomeNone, Labels: source}
	switch {
	case failure != "":
		a.Detail = failure
	case spec.IsText():
		if text, ok := typed.CheckText(raw); ok {
			a.Outcome, a.Text = OutcomeText, text
		} else {
			a.Detail = "the reader's answer was not usable text"
		}
	default:
		if value, ok := typed.Check(spec, raw); ok {
			// The type check is what removes contamination: a value
			// of the fixed grammar cannot carry instructions.
			a.Outcome, a.Value = OutcomeTyped, value
			a.Labels = Labels{Private: source.Private}
		} else {
			a.Detail = fmt.Sprintf("the reader's answer was not a valid %s", spec.Type)
		}
	}
	return Event{Kind: KindAnswer, Answer: a}, nil
}

// MayRelease reports whether the content of an answer may be handed to the
// session whose call got decision d. The session must have taken on every
// label the content carries. This is checked again at the moment of release
// because a source's labels can grow between the decision and the answer.
func MayRelease(d Decision, a Answer) bool {
	return d.Verdict == Run && a.Outcome != OutcomeNone && d.Gains.Contains(a.Labels)
}
