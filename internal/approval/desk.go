package approval

import (
	"context"
	"fmt"

	"cleanroom/internal/core"
	"cleanroom/internal/eventlog"
	"cleanroom/internal/upstream"
)

// Desk carries out the person's decisions.
type Desk struct {
	Policy    core.Policy
	Log       *eventlog.Log
	Upstreams *upstream.Set
}

// Deny records that the person denied the held call at seq. The call will
// never run.
func (d *Desk) Deny(seq int, via string) error {
	_, err := d.resolve(seq, "", false, via)
	return err
}

// Approve records that the person approved the held call at seq, then runs
// the stored call, once, with the stored arguments.
//
// The hash must be the one on the screen the person was shown. The
// approval is appended under the log's lock before anything runs, so a
// second approval of the same proposal fails and the effect cannot happen
// twice. If cleanroom dies between the approval and the effect, the
// proposal stays "approved" and is not retried: at most once, never twice.
func (d *Desk) Approve(ctx context.Context, seq int, hash, via string) (core.Status, error) {
	pr, err := d.resolve(seq, hash, true, via)
	if err != nil {
		return "", err
	}
	c := pr.Call.Call
	res, err := d.Upstreams.Call(ctx, c.Upstream, c.Tool, c.Args)
	ok := err == nil && !res.IsError
	if _, logErr := d.Log.Update(func([]core.Event) ([]core.Event, error) {
		return []core.Event{{Kind: core.KindResult, Result: &core.Result{Ref: seq, OK: ok}}}, nil
	}); logErr != nil {
		return "", fmt.Errorf("the call ran (ok=%v) but its result could not be recorded: %w", ok, logErr)
	}
	if !ok {
		return core.Failed, nil
	}
	return core.Done, nil
}

// resolve appends the person's decision if core.Resolve accepts it. An
// empty hash means "whatever is stored", which only a denial may use.
func (d *Desk) resolve(seq int, hash string, approve bool, via string) (core.Proposal, error) {
	var pr core.Proposal
	_, err := d.Log.Update(func(log []core.Event) ([]core.Event, error) {
		var ok bool
		if pr, ok = core.ProposalAt(log, seq); !ok {
			return nil, fmt.Errorf("%d is not a held call", seq)
		}
		if pr.Call.Call.Op != core.OpTool {
			return nil, fmt.Errorf("proposal %d is not a tool call and cannot be carried out", seq)
		}
		if hash == "" && !approve {
			hash = pr.Call.Hash
		}
		e, err := core.Resolve(d.Policy, log, seq, hash, approve, via)
		if err != nil {
			return nil, err
		}
		return []core.Event{e}, nil
	})
	return pr, err
}

// MarkDelivered records that the answer to the ask at seq was shown to the
// person.
func (d *Desk) MarkDelivered(seq int, via string) error {
	_, err := d.Log.Update(func(log []core.Event) ([]core.Event, error) {
		if _, ok := core.AnswerTo(log, seq); !ok {
			return nil, fmt.Errorf("ask %d has no answer", seq)
		}
		return []core.Event{{Kind: core.KindDelivered, Delivered: &core.Delivered{Ref: seq, Via: via}}}, nil
	})
	return err
}
