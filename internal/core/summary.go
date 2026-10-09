package core

import "fmt"

// Summarize says in a few words what happened at seq, for citing an event
// in an explanation. Every word is written by cleanroom: it names tools,
// sources and numbers, and quotes nothing that came from a source or from
// a call's arguments. It is therefore safe to show to anyone.
func Summarize(log []Event, seq int) string {
	if seq < 1 || seq > len(log) {
		return "unknown event"
	}
	e := log[seq-1]
	switch e.Kind {
	case KindCall:
		return summarizeCall(e.Call.Call)
	case KindApproval:
		verb := "denied"
		if e.Approval.Approve {
			verb = "approved"
		}
		return fmt.Sprintf("the person %s proposal %d", verb, e.Approval.Ref)
	case KindAnswer:
		return fmt.Sprintf("answer to ask %d (%s)", e.Answer.Ref, e.Answer.Outcome)
	case KindResult:
		if e.Result.OK {
			return fmt.Sprintf("call %d finished", e.Result.Ref)
		}
		return fmt.Sprintf("call %d failed", e.Result.Ref)
	case KindDelivered:
		return fmt.Sprintf("answer %d was shown to the person", e.Delivered.Ref)
	}
	return string(e.Kind)
}

func summarizeCall(c Call) string {
	switch c.Op {
	case OpTool:
		return fmt.Sprintf("called %s.%s", c.Upstream, c.Tool)
	case OpAsk:
		if c.AcceptContamination {
			return fmt.Sprintf("asked %s and accepted contamination", c.Upstream)
		}
		return fmt.Sprintf("asked %s", c.Upstream)
	case OpReadAnswer:
		if c.AcceptContamination {
			return fmt.Sprintf("read stored answer %d and accepted contamination", c.Answer)
		}
		return fmt.Sprintf("read stored answer %d", c.Answer)
	}
	return "made a call cleanroom did not recognise"
}
