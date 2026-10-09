package core

import (
	"errors"
	"fmt"
)

// Status is where a held call stands.
type Status string

const (
	Pending  Status = "pending"  // waiting for the person
	Denied   Status = "denied"   // the person said no; it will never run
	Approved Status = "approved" // the person said yes; the effect has not finished
	Done     Status = "done"     // approved, and the effect ran
	Failed   Status = "failed"   // approved, and the effect was tried and failed
)

// Proposal is a held call and where it stands.
type Proposal struct {
	Seq    int // Seq of the held call
	Call   Decided
	Status Status
}

// Proposals lists every held call in the log, oldest first.
func Proposals(log []Event) []Proposal {
	var out []Proposal
	index := map[int]int{} // Seq of the call -> position in out
	for _, e := range log {
		switch e.Kind {
		case KindCall:
			if e.Call.Decision.Verdict == Hold {
				index[e.Seq] = len(out)
				out = append(out, Proposal{Seq: e.Seq, Call: *e.Call, Status: Pending})
			}
		case KindApproval:
			if i, ok := index[e.Approval.Ref]; ok && out[i].Status == Pending {
				out[i].Status = Denied
				if e.Approval.Approve {
					out[i].Status = Approved
				}
			}
		case KindResult:
			if i, ok := index[e.Result.Ref]; ok && out[i].Status == Approved {
				out[i].Status = Failed
				if e.Result.OK {
					out[i].Status = Done
				}
			}
		}
	}
	return out
}

// ProposalAt returns the held call recorded at seq.
func ProposalAt(log []Event, seq int) (Proposal, bool) {
	for _, pr := range Proposals(log) {
		if pr.Seq == seq {
			return pr, true
		}
	}
	return Proposal{}, false
}

// Resolve checks the person's decision about the held call at seq and, if
// it stands, returns the approval event to append. Like Decide it is pure.
//
// The hash must be the one the person was shown. That ties the approval to
// one tool and one set of arguments. A proposal can be resolved once: the
// caller appends the event under the same lock it read the log with, so a
// second approval finds the first and fails. The effect therefore runs at
// most once.
func Resolve(p Policy, log []Event, seq int, hash string, approve bool, via string) (Event, error) {
	if err := CheckLog(log); err != nil {
		return Event{}, fmt.Errorf("the event log is malformed: %w", err)
	}
	pr, ok := ProposalAt(log, seq)
	if !ok {
		return Event{}, fmt.Errorf("%d is not a held call", seq)
	}
	if pr.Status != Pending {
		return Event{}, fmt.Errorf("proposal %d is already %s", seq, pr.Status)
	}
	if hash == "" || hash != pr.Call.Hash || hash != pr.Call.Call.Hash() {
		return Event{}, errors.New("the hash does not match the stored call")
	}
	return Event{Kind: KindApproval, Approval: &Approval{
		Ref:     seq,
		Approve: approve,
		Hash:    hash,
		Held:    StateOf(p, log, pr.Call.Call.Session).Labels,
		Gains:   ResultLabels(p, log, pr.Call.Call),
		Via:     via,
	}}, nil
}
