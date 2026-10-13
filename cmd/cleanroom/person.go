package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

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
	return c.print(func(_ *state, events []core.Event) (string, error) {
		return proposalsTable(events), nil
	})
}

func answersCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return c.print(func(_ *state, events []core.Event) (string, error) {
		return answersTable(events), nil
	})
}

// print returns a command that reads the log, renders it and writes the
// result. The rendering is a function of the log alone, and this is the only
// step that writes.
func (c *common) print(render func(*state, []core.Event) (string, error)) func(context.Context) error {
	return func(context.Context) error {
		st, events, err := c.read()
		if err != nil {
			return err
		}
		text, err := render(st, events)
		fmt.Fprint(c.stdout, text)
		return err
	}
}

func table(header string, rows []string) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, header)
	for _, row := range rows {
		fmt.Fprintln(w, row)
	}
	w.Flush()
	return b.String()
}

func proposalsTable(events []core.Event) string {
	var rows []string
	for _, pr := range core.Proposals(events) {
		call := pr.Call.Call
		rows = append(rows, fmt.Sprintf("%d\t%s\t%s\t%s.%s", pr.Seq, pr.Status, call.Session, call.Upstream, call.Tool))
	}
	return table("PROPOSAL\tSTATUS\tSESSION\tCALL", rows)
}

func answersTable(events []core.Event) string {
	shown := map[int]bool{}
	for _, e := range events {
		if e.Kind == core.KindDelivered {
			shown[e.Delivered.Ref] = true
		}
	}
	var rows []string
	for _, e := range events {
		if e.Kind != core.KindAnswer || e.Answer.Outcome != core.OutcomeText {
			continue
		}
		ask := events[e.Answer.Ref-1].Call.Call
		rows = append(rows, fmt.Sprintf("%d\t%s\t%s\t%v", e.Answer.Ref, ask.Upstream, ask.Session, shown[e.Answer.Ref]))
	}
	return table("ASK\tSOURCE\tSESSION\tSHOWN", rows)
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
			fmt.Fprintf(c.stdout, "\nTo decide: cleanroom approve%[2]s %[1]d, or cleanroom deny%[2]s %[1]d\n", seq, c.hintFlags())
			return nil
		}
		screen, err := approval.AnswerScreen(events, seq, c.width)
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
		fmt.Fprint(c.stdout, approvePrompt(seq))
		line, err := c.input.ReadLine()
		if err != nil || strings.TrimSpace(line) != strconv.Itoa(seq) {
			fmt.Fprint(c.stdout, notApproved(seq, err == nil))
			return err
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

// approvePrompt asks for the proposal's number and not a bare Enter, so
// that approving takes a deliberate act.
func approvePrompt(seq int) string {
	return fmt.Sprintf("\nTo approve, type the proposal number, %d, and then press Enter.\nEnter alone, or anything else, cancels: ", seq)
}

// notApproved says that the call stays held, and why when the person
// answered the prompt with something other than the number.
func notApproved(seq int, answered bool) string {
	if !answered {
		return "Cancelled. The call is still held.\n"
	}
	return fmt.Sprintf("Cancelled, because what you entered was not %d. The call is still held.\n", seq)
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
	return c.print(func(_ *state, events []core.Event) (string, error) {
		return logTable(events, time.Local), nil
	})
}

func logTable(events []core.Event, zone *time.Location) string {
	var rows []string
	for _, e := range events {
		session, decision := "", ""
		if e.Kind == core.KindCall {
			session = e.Call.Call.Session
			decision = string(e.Call.Decision.Verdict)
		}
		what := core.Summarize(events, e.Seq)
		// A summary is short enough to cite in a note to an agent. The
		// log has room for the reason an ask came to nothing.
		if e.Kind == core.KindAnswer && e.Answer.Outcome == core.OutcomeNone && e.Answer.Detail != "" {
			what = fmt.Sprintf("no answer to ask %d: %s", e.Answer.Ref, e.Answer.Detail)
		}
		rows = append(rows, fmt.Sprintf("%d\t%s\t%s\t%s\t%s", e.Seq, e.Time.In(zone).Format("15:04:05"), session, what, decision))
	}
	return table("EVENT\tTIME\tSESSION\tWHAT\tDECISION", rows)
}

// explainCommand replays a decision. Decide is a pure function of the
// policy, the log and the call, so running it again on the log as it stood
// gives the recorded decision again, with its reasons.
func explainCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return c.print(func(st *state, events []core.Event) (string, error) {
		seq, err := numberArg(fs)
		if err != nil {
			return "", err
		}
		return explanation(st.cfg.Policy, events, seq)
	})
}

// explanation replays the decision about the call at seq and describes it.
// It returns the description with an error when the replay does not match
// what was recorded.
func explanation(policy core.Policy, events []core.Event, seq int) (string, error) {
	if seq > len(events) || events[seq-1].Kind != core.KindCall {
		return "", fmt.Errorf("event %d is not a call", seq)
	}
	recorded := events[seq-1].Call
	replayed := core.Decide(policy, events[:seq-1], recorded.Call)

	var b strings.Builder
	fmt.Fprintf(&b, "Event %d: session %q %s\n", seq, recorded.Call.Session, core.Summarize(events, seq))
	fmt.Fprintf(&b, "The session carried: %s\n", recorded.Held)
	fmt.Fprintf(&b, "Decision: %s", replayed.Verdict)
	if replayed.Class != "" {
		fmt.Fprintf(&b, " (class %s)", replayed.Class)
	}
	fmt.Fprintln(&b)
	for _, r := range replayed.Reasons {
		fmt.Fprintf(&b, "  - %s\n", r.Text)
		for _, cited := range r.Events {
			fmt.Fprintf(&b, "      event %d: %s\n", cited, core.Summarize(events, cited))
		}
	}
	if replayed.Gains != (core.Labels{}) {
		fmt.Fprintf(&b, "The session takes on: %s\n", replayed.Gains)
	}
	if !replayed.Equal(recorded.Decision) {
		return b.String(), fmt.Errorf("the replayed decision differs from the recorded one (%s): the policy has changed since", recorded.Decision.Verdict)
	}
	b.WriteString("Replayed from the log just now. It matches the decision recorded at the time.\n")
	return b.String(), nil
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

func (c *common) read() (*state, []core.Event, error) {
	st, err := c.open()
	if err != nil {
		return nil, nil, err
	}
	events, err := st.log.Read()
	return st, events, err
}

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
