package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Event is one line of the log. The log is append-only and shared by every
// session, and it is the only state cleanroom has: what a session carries,
// which calls are waiting for approval and what was answered are all
// computed from it.
//
// Exactly one of the payload pointers is set, matching Kind.
type Event struct {
	// Seq is the event's position in the log, counting from 1. Other
	// events and the command line refer to an event by this number.
	Seq  int       `json:"seq"`
	Time time.Time `json:"time"`
	Kind Kind      `json:"kind"`

	Call      *Decided   `json:"call,omitempty"`
	Result    *Result    `json:"result,omitempty"`
	Answer    *Answer    `json:"answer,omitempty"`
	Approval  *Approval  `json:"approval,omitempty"`
	Delivered *Delivered `json:"delivered,omitempty"`
}

// Kind names the payload of an event.
type Kind string

const (
	KindCall      Kind = "call"      // a call and the decision made about it
	KindResult    Kind = "result"    // a call finished
	KindAnswer    Kind = "answer"    // a reader's answer to an ask
	KindApproval  Kind = "approval"  // the person approved or denied a held call
	KindDelivered Kind = "delivered" // a free-text answer was shown to the person
)

// Op is what a call asks cleanroom to do.
type Op string

const (
	OpTool       Op = "tool"        // call an upstream tool
	OpAsk        Op = "ask"         // put a question to a reader
	OpReadAnswer Op = "read_answer" // read a stored answer
)

func (o Op) valid() bool { return o == OpTool || o == OpAsk || o == OpReadAnswer }

// Call is a request from a session, before any decision. It holds every
// input the decision depends on apart from the policy and the log.
type Call struct {
	Session string `json:"session"`
	Op      Op     `json:"op"`

	// Upstream is the server to call for OpTool, or the source to ask
	// for OpAsk.
	Upstream string `json:"upstream,omitempty"`

	// For OpTool.
	Tool string          `json:"tool,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	// Hint is the class guessed from the server's annotations, or empty.
	Hint Class `json:"hint,omitempty"`

	// For OpAsk.
	Question string          `json:"question,omitempty"`
	Returns  json.RawMessage `json:"returns,omitempty"`

	// For OpReadAnswer: the Seq of the ask whose answer to read.
	Answer int `json:"answer,omitempty"`

	// AcceptContamination is the caller's explicit choice to receive
	// untrusted text, for OpAsk and OpReadAnswer.
	AcceptContamination bool `json:"accept_contamination,omitempty"`

	// Invalid is set by Sanitize when part of the call could not be
	// kept, and says why. Such a call is always refused.
	Invalid string `json:"invalid,omitempty"`
}

// Hash identifies exactly what a call would do. An approval names the hash
// it approves, so it cannot be applied to a call with other arguments.
func (c Call) Hash() string {
	b, err := json.Marshal(c)
	if err != nil {
		// Marshal fails only if Args or Returns is not valid JSON.
		// Decide refuses such a call, so it never needs a hash.
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Decided is a call together with the decision made about it. Recording
// both in one event means a decision can be replayed later: Decide on the
// log up to this event, with this call, gives this decision again.
type Decided struct {
	Call     Call     `json:"call"`
	Decision Decision `json:"decision"`
	// Held is what the session carried when the call was decided.
	Held Labels `json:"held"`
	// Hash is Call.Hash(), stored so that approval screens and approvals
	// can quote it.
	Hash string `json:"hash"`
}

// Result records that a call finished. It holds no content from the
// upstream: only whether the call worked.
type Result struct {
	Ref  int    `json:"ref"` // Seq of the call
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"` // written by cleanroom, never by an upstream
}

// Outcome is how an ask ended.
type Outcome string

const (
	OutcomeTyped Outcome = "typed" // a value that passed the type check
	OutcomeText  Outcome = "text"  // free text, stored for the person
	OutcomeNone  Outcome = "none"  // no usable answer
)

// Answer is a reader's answer to an ask. A text answer is a stored value:
// it keeps the labels of the reader that wrote it, and a session that reads
// it later takes those labels on.
type Answer struct {
	Ref     int             `json:"ref"` // Seq of the ask
	Outcome Outcome         `json:"outcome"`
	Value   json.RawMessage `json:"value,omitempty"` // for OutcomeTyped
	Text    string          `json:"text,omitempty"`  // for OutcomeText
	Labels  Labels          `json:"labels"`
	// Detail says why there was no answer. It is for the person and the
	// log. The caller is told only that there is no answer.
	Detail string `json:"detail,omitempty"`
}

// Approval is the person's decision about a held call.
type Approval struct {
	Ref     int    `json:"ref"` // Seq of the held call
	Approve bool   `json:"approve"`
	Hash    string `json:"hash"` // the Call.Hash() the person was shown
	// Held is what the session carried when the approval was given.
	Held Labels `json:"held"`
	// Gains is what the session takes on from the call's result, worked
	// out when the approval was given. What the call would have gained
	// when it was first held may be out of date by then.
	Gains Labels `json:"gains"`
	Via   string `json:"via"` // "cli", "elicitation", ...
}

// Delivered records that a free-text answer was shown to the person.
type Delivered struct {
	Ref int    `json:"ref"` // Seq of the ask
	Via string `json:"via"`
}

const readerPrefix = "reader:"

// ReaderSession names the disposable session of the reader that answers the
// ask recorded at seq. The colon keeps it apart from client sessions, which
// cannot contain one.
func ReaderSession(askSeq int) string { return readerPrefix + strconv.Itoa(askSeq) }

// readerAsk returns the Seq of the ask that a reader session serves.
func readerAsk(session string) (int, bool) {
	rest, ok := strings.CutPrefix(session, readerPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

// CheckLog reports the first structural fault in a log: a gap in the
// sequence, a missing payload, or a reference to an event that is not what
// it should be. Decide refuses every call when the log is faulty. The folds
// (StateOf, SourceLabels, Proposals) assume a log that passes, so the shell
// checks a log when it loads it.
func CheckLog(log []Event) error {
	resolved := map[int]bool{} // Seq of each held call already approved or denied
	for i, e := range log {
		if e.Seq != i+1 {
			return fmt.Errorf("event at position %d has seq %d", i+1, e.Seq)
		}
		ref, wantRef := 0, false
		ok := false
		switch e.Kind {
		case KindCall:
			ok = e.Call != nil && e.Call.Decision.Verdict.valid()
		case KindResult:
			if ok = e.Result != nil; ok {
				ref, wantRef = e.Result.Ref, true
			}
		case KindAnswer:
			if ok = e.Answer != nil; ok {
				ref, wantRef = e.Answer.Ref, true
			}
		case KindApproval:
			if ok = e.Approval != nil; ok {
				ref, wantRef = e.Approval.Ref, true
			}
		case KindDelivered:
			if ok = e.Delivered != nil; ok {
				ref, wantRef = e.Delivered.Ref, true
			}
		}
		if !ok {
			return fmt.Errorf("event %d has kind %q and no matching payload", e.Seq, e.Kind)
		}
		if wantRef && (ref < 1 || ref >= e.Seq || log[ref-1].Kind != KindCall) {
			return fmt.Errorf("event %d refers to %d, which is not an earlier call", e.Seq, ref)
		}
		if e.Kind == KindApproval {
			if log[ref-1].Call.Decision.Verdict != Hold || resolved[ref] {
				return fmt.Errorf("event %d resolves %d, which is not a held call waiting for a decision", e.Seq, ref)
			}
			resolved[ref] = true
		}
	}
	return nil
}

// AskAt returns the ask recorded at seq, if that event is one.
func AskAt(log []Event, seq int) (*Decided, bool) {
	c, ok := callAt(log, seq)
	if !ok || c.Call.Op != OpAsk {
		return nil, false
	}
	return c, true
}

// callAt returns the call recorded at seq, if there is one.
func callAt(log []Event, seq int) (*Decided, bool) {
	if seq < 1 || seq > len(log) || log[seq-1].Kind != KindCall {
		return nil, false
	}
	return log[seq-1].Call, true
}
