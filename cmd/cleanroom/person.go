package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"text/tabwriter"

	"cleanroom/internal/approval"
	"cleanroom/internal/core"
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
		_, events, err := c.open()
		if err != nil {
			return err
		}
		log, err := events.Read()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROPOSAL\tSTATUS\tSESSION\tCALL")
		for _, pr := range core.Proposals(log) {
			call := pr.Call.Call
			fmt.Fprintf(w, "%d\t%s\t%s\t%s.%s\n", pr.Seq, pr.Status, call.Session, call.Upstream, call.Tool)
		}
		return w.Flush()
	}
}

func answersCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		_, events, err := c.open()
		if err != nil {
			return err
		}
		log, err := events.Read()
		if err != nil {
			return err
		}
		shown := map[int]bool{}
		for _, e := range log {
			if e.Kind == core.KindDelivered {
				shown[e.Delivered.Ref] = true
			}
		}
		w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ASK\tSOURCE\tSESSION\tSHOWN")
		for _, e := range log {
			if e.Kind != core.KindAnswer || e.Answer.Outcome != core.OutcomeText {
				continue
			}
			ask := log[e.Answer.Ref-1].Call.Call
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
		cfg, events, err := c.open()
		if err != nil {
			return err
		}
		log, err := events.Read()
		if err != nil {
			return err
		}
		if screen, err := approval.Screen(cfg.Policy, log, seq); err == nil {
			fmt.Fprint(c.stdout, screen)
			fmt.Fprintf(c.stdout, "\nTo decide: cleanroom approve %d, or cleanroom deny %d\n", seq, seq)
			return nil
		}
		screen, err := approval.AnswerScreen(log, seq)
		if err != nil {
			return fmt.Errorf("%d is neither a held call nor an ask", seq)
		}
		fmt.Fprint(c.stdout, screen)
		if ans, ok := core.AnswerTo(log, seq); ok && ans.Outcome == core.OutcomeText {
			desk := &approval.Desk{Policy: cfg.Policy, Log: events}
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
		cfg, events, err := c.open()
		if err != nil {
			return err
		}
		log, err := events.Read()
		if err != nil {
			return err
		}
		screen, err := approval.Screen(cfg.Policy, log, seq)
		if err != nil {
			return err
		}
		pr, _ := core.ProposalAt(log, seq)
		fmt.Fprint(c.stdout, screen)
		fmt.Fprintf(c.stdout, "\nType %d and press Enter to approve. Anything else cancels: ", seq)
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		if strings.TrimSpace(line) != strconv.Itoa(seq) {
			fmt.Fprintln(c.stdout, "Cancelled. The call is still held.")
			return nil
		}

		upstreams := upstream.New(cfg.Policy, upstream.CommandDialer(cfg.Dir))
		defer upstreams.Close()
		desk := &approval.Desk{Policy: cfg.Policy, Log: events, Upstreams: upstreams}
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
		cfg, events, err := c.open()
		if err != nil {
			return err
		}
		desk := &approval.Desk{Policy: cfg.Policy, Log: events}
		if err := desk.Deny(seq, "cli"); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "Denied. Proposal %d will never run.\n", seq)
		return nil
	}
}

func logCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		_, events, err := c.open()
		if err != nil {
			return err
		}
		log, err := events.Read()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "EVENT\tTIME\tSESSION\tWHAT\tDECISION")
		for _, e := range log {
			session, decision := "", ""
			if e.Kind == core.KindCall {
				session = e.Call.Call.Session
				decision = string(e.Call.Decision.Verdict)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", e.Seq, e.Time.Local().Format("15:04:05"), session, core.Summarize(log, e.Seq), decision)
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
		cfg, events, err := c.open()
		if err != nil {
			return err
		}
		log, err := events.Read()
		if err != nil {
			return err
		}
		if seq > len(log) || log[seq-1].Kind != core.KindCall {
			return fmt.Errorf("event %d is not a call", seq)
		}
		recorded := log[seq-1].Call
		replayed := core.Decide(cfg.Policy, log[:seq-1], recorded.Call)

		fmt.Fprintf(c.stdout, "Event %d: session %q %s\n", seq, recorded.Call.Session, core.Summarize(log, seq))
		fmt.Fprintf(c.stdout, "The session carried: %s\n", recorded.Held)
		fmt.Fprintf(c.stdout, "Decision: %s", replayed.Verdict)
		if replayed.Class != "" {
			fmt.Fprintf(c.stdout, " (class %s)", replayed.Class)
		}
		fmt.Fprintln(c.stdout)
		for _, r := range replayed.Reasons {
			fmt.Fprintf(c.stdout, "  - %s\n", r.Text)
			for _, cited := range r.Events {
				fmt.Fprintf(c.stdout, "      event %d: %s\n", cited, core.Summarize(log, cited))
			}
		}
		if replayed.Gains != (core.Labels{}) {
			fmt.Fprintf(c.stdout, "The session takes on: %s\n", replayed.Gains)
		}
		if reflect.DeepEqual(replayed, recorded.Decision) {
			fmt.Fprintln(c.stdout, "Replayed from the log just now. It matches the decision recorded at the time.")
			return nil
		}
		return fmt.Errorf("the replayed decision differs from the recorded one (%s): the policy has changed since", recorded.Decision.Verdict)
	}
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
