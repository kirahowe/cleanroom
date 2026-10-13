package core

import "fmt"

// Summarize says in a few words what happened at seq, for citing an event
// in an explanation. Every word is written by cleanroom: it names tools,
// sources and numbers, and quotes nothing that came from a source or from
// a call's arguments. It is therefore safe to show to anyone.
//
// A refused call is summarized without its names. The upstream and tool of
// a call that ran or was held are in the policy, but a refused call may
// name anything: an agent or a reader can try a tool that does not exist,
// and the attempt is recorded under the name it used.
func Summarize(log []Event, seq int) string {
	if seq < 1 || seq > len(log) {
		return "unknown event"
	}
	e := log[seq-1]
	switch e.Kind {
	case KindCall:
		if e.Call.Decision.Verdict == Refuse {
			return summarizeRefused(e.Call.Call)
		}
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

func summarizeRefused(c Call) string {
	switch c.Op {
	case OpTool:
		return "tried to call a tool and was refused"
	case OpAsk:
		return "tried to ask a source and was refused"
	case OpReadAnswer:
		return "tried to read a stored answer and was refused"
	}
	return "made a call cleanroom did not recognise"
}
