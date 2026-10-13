package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"cleanroom/internal/approval"
	"cleanroom/internal/core"
	"cleanroom/internal/eventlog"
	"cleanroom/internal/upstream"
)

// This file holds the commands the person uses: listing what is waiting,
// reading it, and deciding.
//
// Two of them, show and approve, only work at a terminal. show prints
// content that agents are deliberately kept away from, and approve is the
// person's decision. An agent that can run shell commands could otherwise
// read its way into contamination by accident, or approve its own held
// call. The check is a seat belt, not a wall: see "Limits" in the README.

var errNeedsTerminal = errors.New(`this command only works at a terminal, because it is for the person and not for an agent.
If you are an agent: do not try to work around this. Ask the user to run it in their own terminal`)

func proposalsCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		_, events, err := c.read()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROPOSAL\tSTATUS\tSESSION\tCALL")
		for _, pr := range core.Proposals(events) {
			call := pr.Call.Call
			fmt.Fprintf(w, "%d\t%s\t%s\t%s.%s\n", pr.Seq, pr.Status, call.Session, call.Upstream, call.Tool)
		}
		return w.Flush()
	}
}

func answersCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		_, events, err := c.read()
		if err != nil {
			return err
		}
		shown := map[int]bool{}
		for _, e := range events {
			if e.Kind == core.KindDelivered {
				shown[e.Delivered.Ref] = true
			}
		}
		w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ASK\tSOURCE\tSESSION\tSHOWN")
		for _, e := range events {
			if e.Kind != core.KindAnswer || e.Answer.Outcome != core.OutcomeText {
				continue
			}
			ask := events[e.Answer.Ref-1].Call.Call
			fmt.Fprintf(w, "%d\t%s\t%s\t%v\n", e.Answer.Ref, ask.Upstream, ask.Session, shown[e.Answer.Ref])
		}
		return w.Flush()
	}
}

func showCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		seq, err := numberArg(fs)
		if err != nil {
			return err
		}
		if !c.terminal {
			return errNeedsTerminal
		}
		st, events, err := c.read()
		if err != nil {
			return err
		}
		cfg := st.cfg
		if screen, err := approval.Screen(cfg.Policy, events, seq); err == nil {
			fmt.Fprint(c.stdout, screen)
			fmt.Fprintf(c.stdout, "\nTo decide: cleanroom approve %d, or cleanroom deny %d\n", seq, seq)
			return nil
		}
		screen, err := approval.AnswerScreen(events, seq)
		if err != nil {
			return fmt.Errorf("%d is neither a held call nor an ask", seq)
		}
		fmt.Fprint(c.stdout, screen)
		if ans, ok := core.AnswerTo(events, seq); ok && ans.Outcome == core.OutcomeText {
			desk := &approval.Desk{Policy: cfg.Policy, Log: st.log}
			return desk.MarkDelivered(seq, "cli")
		}
		return nil
	}
}

func approveCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		seq, err := numberArg(fs)
		if err != nil {
			return err
		}
		if !c.terminal {
			return errNeedsTerminal
		}
		st, events, err := c.read()
		if err != nil {
			return err
		}
		cfg := st.cfg
		screen, err := approval.Screen(cfg.Policy, events, seq)
		if err != nil {
			return err
		}
		pr, _ := core.ProposalAt(events, seq)
		fmt.Fprint(c.stdout, screen)
		fmt.Fprintf(c.stdout, "\nType %d and press Enter to approve. Anything else cancels: ", seq)
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		if strings.TrimSpace(line) != strconv.Itoa(seq) {
			fmt.Fprintln(c.stdout, "Cancelled. The call is still held.")
			return nil
		}

		children, err := st.childLog()
		if err != nil {
			return err
		}
		defer children.Close()
		upstreams := upstream.New(cfg.Policy, upstream.CommandDialer(cfg.Dir, children))
		defer upstreams.Close()
		desk := &approval.Desk{Policy: cfg.Policy, Log: st.log, Upstreams: upstreams}
		// The hash is the one on the screen just shown. If the stored
		// call were anything else, the approval would be refused.
		status, err := desk.Approve(ctx, seq, pr.Call.Hash, "cli")
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "Approved. The call ran once: %s.\n", status)
		return nil
	}
}

func denyCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		seq, err := numberArg(fs)
		if err != nil {
			return err
		}
		st, err := c.open()
		if err != nil {
			return err
		}
		desk := &approval.Desk{Policy: st.cfg.Policy, Log: st.log}
		if err := desk.Deny(seq, "cli"); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "Denied. Proposal %d will never run.\n", seq)
		return nil
	}
}

func logCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		_, events, err := c.read()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "EVENT\tTIME\tSESSION\tWHAT\tDECISION")
		for _, e := range events {
			session, decision := "", ""
			if e.Kind == core.KindCall {
				session = e.Call.Call.Session
				decision = string(e.Call.Decision.Verdict)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", e.Seq, e.Time.Local().Format("15:04:05"), session, core.Summarize(events, e.Seq), decision)
		}
		return w.Flush()
	}
}

// explainCommand replays a decision. Decide is a pure function of the
// policy, the log and the call, so running it again on the log as it stood
// gives the recorded decision again, with its reasons.
func explainCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		seq, err := numberArg(fs)
		if err != nil {
			return err
		}
		st, events, err := c.read()
		if err != nil {
			return err
		}
		cfg := st.cfg
		if seq > len(events) || events[seq-1].Kind != core.KindCall {
			return fmt.Errorf("event %d is not a call", seq)
		}
		recorded := events[seq-1].Call
		replayed := core.Decide(cfg.Policy, events[:seq-1], recorded.Call)

		fmt.Fprintf(c.stdout, "Event %d: session %q %s\n", seq, recorded.Call.Session, core.Summarize(events, seq))
		fmt.Fprintf(c.stdout, "The session carried: %s\n", recorded.Held)
		fmt.Fprintf(c.stdout, "Decision: %s", replayed.Verdict)
		if replayed.Class != "" {
			fmt.Fprintf(c.stdout, " (class %s)", replayed.Class)
		}
		fmt.Fprintln(c.stdout)
		for _, r := range replayed.Reasons {
			fmt.Fprintf(c.stdout, "  - %s\n", r.Text)
			for _, cited := range r.Events {
				fmt.Fprintf(c.stdout, "      event %d: %s\n", cited, core.Summarize(events, cited))
			}
		}
		if replayed.Gains != (core.Labels{}) {
			fmt.Fprintf(c.stdout, "The session takes on: %s\n", replayed.Gains)
		}
		if replayed.Equal(recorded.Decision) {
			fmt.Fprintln(c.stdout, "Replayed from the log just now. It matches the decision recorded at the time.")
			return nil
		}
		return fmt.Errorf("the replayed decision differs from the recorded one (%s): the policy has changed since", recorded.Decision.Verdict)
	}
}

// repairCommand deals with the one kind of log damage a crash can cause: a
// last line that was cut off partway through. Until it is set aside, every
// command and every decision fails with an error that names this command.
func repairCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		st, err := c.open()
		if err != nil {
			return err
		}
		repaired, err := st.log.Repair()
		if err != nil {
			return fmt.Errorf("the log is damaged in a way repair will not touch, because events may be missing: %w", err)
		}
		if !repaired {
			fmt.Fprintln(c.stdout, "Nothing to repair. The log reads cleanly.")
			return nil
		}
		fmt.Fprintf(c.stdout, "Set aside the cut-off last line in %s.cut-off. The log reads cleanly again.\n", filepath.Join(st.dir, eventlog.FileName))
		return nil
	}
}

// read opens the state and reads the whole log.
func (c *common) read() (*state, []core.Event, error) {
	st, err := c.open()
	if err != nil {
		return nil, nil, err
	}
	events, err := st.log.Read()
	return st, events, err
}

// numberArg returns the single event number given after the flags.
func numberArg(fs *flag.FlagSet) (int, error) {
	if fs.NArg() != 1 {
		return 0, fmt.Errorf("give one number, as in: %s 12", fs.Name())
	}
	n, err := strconv.Atoi(fs.Arg(0))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%q is not an event number", fs.Arg(0))
	}
	return n, nil
}
