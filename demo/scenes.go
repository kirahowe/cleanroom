package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cleanroom/internal/approval"
	"cleanroom/internal/core"
	"cleanroom/internal/upstream"
)

// run demonstrates typed queries, free-text delivery, contamination and approval.
func (d *demo) run() {
	a := d.startAgent()
	d.tripCost(a)
	d.reunionAndOrder(a)
	a.close()

	d.obedientReader()

	a = d.startAgent()
	ask := d.freeText(a)
	d.optIn(a, ask)
	a.close()

	// The client restarts. The new server process is given the same
	// session id, so the log gives the session its history back.
	a = d.startAgent()
	d.heldEvents(a)
	a.close()
}

func (d *demo) tripCost(a *agent) {
	scene(1, `"How much was my trip to Toronto last weekend?"`)
	res := a.ask("How much was my trip to Toronto last weekend?", "money", false)
	d.check(string(res.Value) == `{"amount":"890.55","currency":"CAD"}`,
		"the four receipts add up to 890.55 CAD")
	d.check(d.labels() == core.Labels{Private: true},
		"the agent's session is %s and not contaminated: it has read no mail", d.labels())
}

func (d *demo) reunionAndOrder(a *agent) {
	scene(2, `"When is my family reunion next year? Put it on my calendar."`)
	res := a.ask("When is my family reunion next year?", "date", false)
	d.check(string(res.Value) == `"2027-07-17"`, "the reunion is on 2027-07-17")

	// The date came from the reader as a typed value. The title is the
	// agent's own words. Nothing in the event was copied from the email.
	out := a.call("calendar_create_event", map[string]any{"title": "Family reunion", "date": "2027-07-17"}, nil)
	fmt.Printf("  agent   received %q\n", oneLine(out))
	ics, _ := os.ReadFile(filepath.Join(d.state, "calendar", "calendar.ics"))
	d.check(strings.Contains(string(ics), "SUMMARY:Family reunion") && strings.Contains(string(ics), "DTSTART;VALUE=DATE:20270717"),
		"the event ran with no approval and is in calendar.ics")

	fmt.Println()
	fmt.Println(`         "When is my order expected? Remind me."`)
	order := a.ask("Where is my order and when is it expected?", map[string]any{
		"type": "record",
		"fields": map[string]any{
			"expected": "date",
			"status":   map[string]any{"type": "choice", "choices": []string{"processing", "in transit", "delivered"}},
		},
	}, false)
	d.check(string(order.Value) == `{"expected":"2026-10-14","status":"in transit"}`,
		"the order is in transit and expected on 2026-10-14")
	a.call("calendar_create_reminder", map[string]any{"title": "Blanket arrives", "due": "2026-10-14"}, nil)
	reminders, _ := os.ReadFile(filepath.Join(d.state, "calendar", "reminders.json"))
	d.check(strings.Contains(string(reminders), "Blanket arrives"), "the reminder ran with no approval and is in reminders.json")
	d.check(!d.labels().Contaminated, "the session is still uncontaminated after three questions and two actions")
}

// obedientReader asks the reunion question again with a reader that does
// whatever the mail tells it. The inbox holds a message that tells it to
// send the inbox to an outside address and to answer with instructions.
func (d *demo) obedientReader() {
	scene(3, "The same question, with a reader that obeys the poisoned email")
	a := d.startAgent("DEMO_READER_OBEY=1")
	defer a.close()

	res := a.ask("When is my family reunion next year?", "date", false)
	// Cleanroom keeps what a reader writes to stderr in a file of its
	// own, away from the agent. The scripted reader narrates there.
	narration, _ := os.ReadFile(filepath.Join(d.state, "children.log"))
	for line := range strings.SplitSeq(string(narration), "\n") {
		if _, what, ok := strings.Cut(line, "obeying the message: "); ok {
			fmt.Printf("  reader  (obeying the email) %s\n", oneLine(what))
		}
	}
	d.check(res.Status == "no_answer" && res.Value == nil && res.Text == "",
		"the attacker's sentence is not a date, so the agent got no answer and none of its text")
	sent, err := sentTo("archive@evil.example")
	d.check(err == nil && sent == 0, "nothing was sent to archive@evil.example: no tool behind cleanroom sends mail")
	d.check(!d.labels().Contaminated, "the agent's session is still uncontaminated")
}

// freeText asks a question whose answer has no type. The answer goes to
// the person. The agent gets an id.
func (d *demo) freeText(a *agent) int {
	scene(4, `"Where is my order right now?" has no type`)
	res := a.ask("Where is my order right now?", "text", false)
	d.check(res.Status == "delivered_to_user" && res.Text == "",
		"the agent got an id (%d) and none of the text", res.ID)
	d.check(d.labels() == core.Labels{Private: true}, "the session is still uncontaminated")

	fmt.Printf("\n  The person reads it with `cleanroom show %d`, which only works at a terminal:\n\n", res.ID)
	d.showToPerson(func(log []core.Event) (string, error) { return approval.AnswerScreen(log, res.ID, 0) })
	return res.ID
}

// optIn reads the stored text and contaminates the session.
func (d *demo) optIn(a *agent, ask int) {
	scene(5, "The agent opts in and reads that answer itself")
	var res askResult
	a.call("read_answer", map[string]any{"id": ask, "accept_contamination": true}, &res)
	d.check(res.Text != "", "the agent now has the text")
	d.check(d.labels() == core.Labels{Contaminated: true, Private: true},
		"the session is now %s, and stays so", d.labels())

	var st struct {
		ContaminatedBy []string `json:"contaminated_by"`
	}
	a.call("status", nil, &st)
	for _, why := range st.ContaminatedBy {
		fmt.Printf("  gateway contaminated by %s\n", why)
	}
}

// heldEvents checks that both actions are held after contamination, a denied
// proposal cannot run, and an approved proposal runs once.
func (d *demo) heldEvents(a *agent) {
	scene(6, "After a restart, the contaminated session tries to change the calendar")
	d.check(d.labels().Contaminated, "the restarted session did not come back clean")

	calendar := func() string {
		ics, _ := os.ReadFile(filepath.Join(d.state, "calendar", "calendar.ics"))
		return string(ics)
	}
	before := calendar()

	var delivery, planted struct {
		Status   string `json:"status"`
		Proposal int    `json:"proposal"`
	}
	a.call("calendar_create_event", map[string]any{"title": "Blanket delivery", "date": "2026-10-14"}, &delivery)
	a.call("calendar_create_event", map[string]any{
		"title":       "Wire the deposit to Northbound today",
		"date":        "2026-10-12",
		"description": "Account details are in the email from archive@evil.example.",
	}, &planted)
	d.check(delivery.Status == "held" && planted.Status == "held",
		"both events are held as proposals %d and %d", delivery.Proposal, planted.Proposal)
	d.check(calendar() == before, "the calendar has not changed yet")

	fmt.Printf("\n  The person sees this with `cleanroom show %d`. Every line comes from the log:\n\n", planted.Proposal)
	d.showToPerson(func(log []core.Event) (string, error) { return approval.Screen(d.cfg.Policy, log, planted.Proposal) })

	desk := &approval.Desk{
		Policy:    d.cfg.Policy,
		Log:       d.log,
		Upstreams: upstream.New(d.cfg.Policy, upstream.CommandDialer(d.cfg.Dir, os.Stderr)),
	}
	defer desk.Upstreams.Close()
	ctx := context.Background()
	fmt.Printf("\n  person  cleanroom deny %d\n", planted.Proposal)
	d.check(desk.Deny(planted.Proposal, "demo") == nil, "the planted event is denied and will never be created")

	fmt.Printf("  person  cleanroom approve %d\n", delivery.Proposal)
	log, _ := d.log.Read()
	pr, _ := core.ProposalAt(log, delivery.Proposal)
	status, err := desk.Approve(ctx, delivery.Proposal, pr.Call.Hash, "demo")
	d.check(err == nil && status == core.Done, "the delivery event is approved and runs")

	_, err = desk.Approve(ctx, delivery.Proposal, pr.Call.Hash, "demo")
	d.check(err != nil, "approving it a second time is refused: %v", err)
	_, err = desk.Approve(ctx, planted.Proposal, pr.Call.Hash, "demo")
	d.check(err != nil, "the denied event cannot be approved afterwards")

	after := calendar()
	d.check(strings.Count(after, "SUMMARY:Blanket delivery") == 1 && !strings.Contains(after, "Wire the deposit"),
		"calendar.ics holds the delivery event exactly once and the planted one not at all")
}

// showToPerson prints a screen that cleanroom would only show at a
// terminal. The demo plays the person, so it prints it here, but only when
// the inbox is the demo's own. If a reviewer has added mail, the screen
// could carry their text to whatever agent is reading this output.
func (d *demo) showToPerson(render func([]core.Event) (string, error)) {
	if !d.ownInbox {
		fmt.Println("    (not printed: the inbox is not the demo's own, and this output may be read by an agent)")
		return
	}
	log, err := d.log.Read()
	if err != nil {
		d.check(false, "reading the log: %v", err)
		return
	}
	screen, err := render(log)
	if err != nil {
		d.check(false, "rendering: %v", err)
		return
	}
	for line := range strings.SplitSeq(strings.TrimRight(screen, "\n"), "\n") {
		fmt.Println(strings.TrimRight("    "+line, " "))
	}
	fmt.Println()
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 150 {
		s = s[:150] + "..."
	}
	return s
}
