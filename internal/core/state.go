package core

// This file holds the folds: functions that compute current state from the
// log. Nothing is cached. The log is the state.

// effect is a call that took effect: either it was allowed to run when it
// was decided, or it was held and the person later approved it.
type effect struct {
	at   int      // Seq of the event at which it took effect
	call *Decided // the call
	held Labels   // what its session carried at that moment
}

// effects lists the calls in a log that took effect, in order. Refused
// calls, denied calls and calls still waiting for approval are left out.
func effects(log []Event) []effect {
	var out []effect
	for _, e := range log {
		switch e.Kind {
		case KindCall:
			if e.Call.Decision.Verdict == Run {
				out = append(out, effect{at: e.Seq, call: e.Call, held: e.Call.Held})
			}
		case KindApproval:
			if c, ok := callAt(log, e.Approval.Ref); ok && e.Approval.Approve {
				out = append(out, effect{at: e.Seq, call: c, held: e.Approval.Held})
			}
		}
	}
	return out
}

// State is what a session carries, with the events that put it there.
type State struct {
	Labels Labels
	// ContaminatedBy and PrivateBy list the Seq of each event that gave
	// the session that label. PrivateBy is empty for a session that is
	// private only because the policy says sessions start private.
	ContaminatedBy []int
	PrivateBy      []int
}

// StateOf folds a session's state from the log. A session takes on the
// labels of a call at the moment the call takes effect, which is before any
// content comes back, so a second call made in parallel already sees them.
func StateOf(p Policy, log []Event, session string) State {
	st := State{Labels: Labels{Private: p.SessionsStartPrivate}}
	for _, ef := range effects(log) {
		if ef.call.Call.Session != session {
			continue
		}
		gains := ef.call.Decision.Gains
		if gains.Contaminated {
			st.ContaminatedBy = append(st.ContaminatedBy, ef.at)
		}
		if gains.Private {
			st.PrivateBy = append(st.PrivateBy, ef.at)
		}
		st.Labels = st.Labels.Union(gains)
	}
	return st
}

// SourceLabels is what content read from an upstream carries now: the
// labels its policy declares, plus the labels of every session that has
// written to it. The second part is how contamination follows data from one
// session to another through a shared store.
//
// It is tracked per upstream, not per stored item, because cleanroom cannot
// see inside an upstream. That is coarse but never under-reports.
func SourceLabels(p Policy, log []Event, upstream string) Labels {
	labels := p.Upstreams[upstream].Labels
	for _, ef := range effects(log) {
		c := ef.call
		if c.Call.Op == OpTool && c.Call.Upstream == upstream && c.Decision.Class != Read {
			labels = labels.Union(ef.held)
		}
	}
	return labels
}

// answerTo returns the answer recorded for the ask at seq, if any.
func answerTo(log []Event, askSeq int) (*Answer, bool) {
	for _, e := range log {
		if e.Kind == KindAnswer && e.Answer.Ref == askSeq {
			return e.Answer, true
		}
	}
	return nil, false
}

// AnswerTo returns the answer recorded for the ask at seq, if any.
func AnswerTo(log []Event, askSeq int) (Answer, bool) {
	a, ok := answerTo(log, askSeq)
	if !ok {
		return Answer{}, false
	}
	return *a, true
}
