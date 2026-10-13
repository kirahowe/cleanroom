package gateway

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	// only the tool of it that does not read is offered.
	want := []string{"ask", "calendar_create_event", "calendar_create_reminder", "calendar_list_events", "inbox_archive", "mail_search", "read_answer", "shady_harmless", "status"}
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
	if diff := cmp.Diff(want, w.fakes["calendar"].Seen()); diff != "" {
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
	res := call(t, cs, "calendar_create_event", map[string]any{"title": "Wire the deposit today", "date": "2026-10-12"}, &held)
	if held.Status != "held" || held.Proposal == 0 || res.IsError {
		t.Fatalf("result = %+v, text %q", held, text(res))
	}
	if !strings.Contains(text(res), "called mail.search") {
		t.Errorf("held text does not cite what contaminated the session:\n%s", text(res))
	}
	// The whole point: the upstream never saw the held call.
	if seen := w.fakes["calendar"].Seen(); len(seen) != 0 {
		t.Errorf("the calendar upstream saw %v, want nothing", seen)
	}

	var st Status
	call(t, cs, "status", nil, &st)
	want := []HeldStatus{{Proposal: held.Proposal, Tool: "calendar.create_event", Status: core.Pending}}
	if diff := cmp.Diff(want, st.Held); diff != "" {
		t.Errorf("status held (-want +got):\n%s", diff)
	}

	// Another session is unaffected.
	other := w.agent("other")
	if res := call(t, other, "calendar_create_event", nil, nil); text(res) != "created" {
		t.Errorf("a clean session was affected: %q", text(res))
	}
}

func TestDiscardedResultTellsTheCallerNothing(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")
	// The fake replies with the new reminder's id. The caller must not
	// see even that.
	res := call(t, cs, "calendar_create_reminder", map[string]any{"title": "Blanket arrives", "due": "2026-10-14"}, nil)
	if res.IsError || text(res) != "calendar.create_reminder ran." {
		t.Errorf("calendar_create_reminder = %q (error %v)", text(res), res.IsError)
	}
	if got := w.labels("chief"); got != (core.Labels{}) {
		t.Errorf("labels = %v, want clean", got)
	}
}

// TestOversizedCallDoesNotDamageTheLog sends arguments far over the limit.
// The call is refused, the log keeps only a small record of it, and the
// next call works.
func TestOversizedCallDoesNotDamageTheLog(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")
	big := map[string]any{"title": strings.Repeat("x", 4*core.MaxArgsBytes)}
	if res := call(t, cs, "calendar_create_event", big, nil); !res.IsError {
		t.Fatalf("an oversized call was not refused: %.80s", text(res))
	}
	if len(w.fakes["calendar"].Seen()) != 0 {
		t.Error("the oversized call reached the upstream")
	}
	events, err := w.g.Log.Read()
	if err != nil {
		t.Fatalf("the log is unreadable after an oversized call: %v", err)
	}
	if stored, _ := json.Marshal(events); len(stored) > 4096 {
		t.Errorf("the log grew to %d bytes", len(stored))
	}
	if res := call(t, cs, "calendar_create_event", nil, nil); text(res) != "created" {
		t.Errorf("the next call = %q, want it to run", text(res))
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
	if len(w.fakes["shady"].Seen()) != 0 {
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
		wg.Go(func() {
			tool := "calendar_create_event"
			if i == 10 {
				tool = "mail_search"
			}
			// Not the call helper: it may call t.Fatal, which is only
			// allowed on the test's own goroutine.
			if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool}); err != nil {
				t.Error(err)
			}
		})
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
	if got := len(w.fakes["calendar"].Seen()); got != ran {
		t.Errorf("the upstream saw %d calls, the log allowed %d", got, ran)
	}
}

// TestUnofferedToolIsRefusedAndRecorded calls tools the server does not
// list. The MCP layer alone would reject them as unknown and leave no
// trace. Each one must instead be decided, refused and written to the log,
// without reaching an upstream and without its name appearing in a summary.
func TestUnofferedToolIsRefusedAndRecorded(t *testing.T) {
	w := newWorld(t)
	cs := w.agent("chief")
	for _, name := range []string{
		"inbox_read",       // exists upstream, but inbox is ask-only
		"calendar_mystery", // exists upstream, with no class
		"mail_forward_all", // an upstream that has no such tool
		"xevilx_do_it",     // nothing cleanroom knows
		"xevilx",
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"to": "eve@evil.example"}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !res.IsError || !strings.HasPrefix(text(res), "REFUSED") {
			t.Errorf("%s = %q, want a refusal", name, text(res))
		}
	}
	for name, fake := range w.fakes {
		if seen := fake.Seen(); len(seen) != 0 {
			t.Errorf("upstream %s saw %v", name, seen)
		}
	}
	log, err := w.g.Log.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 5 {
		t.Fatalf("the log has %d events, want one for each of the 5 attempts", len(log))
	}
	for _, e := range log {
		if e.Kind != core.KindCall || e.Call.Decision.Verdict != core.Refuse {
			t.Errorf("event %d is %s with verdict %q, want a refused call", e.Seq, e.Kind, e.Call.Decision.Verdict)
		}
		shown := core.Summarize(log, e.Seq)
		for _, r := range e.Call.Decision.Reasons {
			shown += " " + r.Text
		}
		if strings.Contains(shown, "xevilx") || strings.Contains(shown, "forward") {
			t.Errorf("event %d shows the name the caller chose: %s", e.Seq, shown)
		}
	}
	// The person can still find the name: it is in the recorded call.
	if got := log[2].Call.Call; got.Upstream != "mail" || got.Tool != "forward_all" {
		t.Errorf("recorded call = %s.%s, want mail.forward_all", got.Upstream, got.Tool)
	}
}
