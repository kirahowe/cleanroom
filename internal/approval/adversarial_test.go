package approval

// Adversarial tests for the person's side that pass: races between
// approving and denying, wrong hashes, a crash between approval and
// effect, and the character classes visible() must write out as escapes.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"unicode"

	"cleanroom/internal/core"
)

func TestApproveAndDenyRace(t *testing.T) {
	for round := range 25 {
		d, mail, held := setup(t, `{"to":["x@example.com"],"body":"b"}`)
		hash := d.proposal(t, held).Call.Hash
		var wg sync.WaitGroup
		var mu sync.Mutex
		approved, denied := 0, 0
		start := make(chan struct{})
		for i := range 12 {
			wg.Go(func() {
				<-start
				if i%2 == 0 {
					if _, err := d.Approve(context.Background(), held, hash, "race"); err == nil {
						mu.Lock()
						approved++
						mu.Unlock()
					}
				} else if d.Deny(held, "race") == nil {
					mu.Lock()
					denied++
					mu.Unlock()
				}
			})
		}
		close(start)
		wg.Wait()

		if approved+denied != 1 {
			t.Fatalf("round %d: %d approvals and %d denials succeeded, want exactly one decision", round, approved, denied)
		}
		if got := len(mail.Seen()); got != approved {
			t.Fatalf("round %d: %d approvals, %d sends reached the upstream", round, approved, got)
		}
		st := d.proposal(t, held).Status
		if (approved == 1 && st != core.Done) || (denied == 1 && st != core.Denied) {
			t.Fatalf("round %d: status %s after approved=%d denied=%d", round, st, approved, denied)
		}
		log, _ := d.Log.Read()
		resolutions := 0
		for _, e := range log {
			if e.Kind == core.KindApproval {
				resolutions++
			}
		}
		if resolutions != 1 {
			t.Fatalf("round %d: %d approval events", round, resolutions)
		}
	}
}

func TestWrongHashesNeverRun(t *testing.T) {
	d, mail, held := setup(t, `{"to":["x@example.com"],"body":"b"}`)
	hash := d.proposal(t, held).Call.Hash
	other := core.Call{Session: "chief", Op: core.OpTool, Upstream: "mail", Tool: "send", Args: json.RawMessage(`{"to":["eve@evil.example"]}`)}
	for _, h := range []string{"", " ", hash[:len(hash)-1], hash + "0", strings.ToUpper(hash), " " + hash, hash + "\n", other.Hash(), strings.Repeat("0", 64)} {
		if h == hash {
			continue
		}
		if st, err := d.Approve(context.Background(), held, h, "t"); err == nil {
			t.Errorf("Approve with hash %q = %s", h, st)
		}
	}
	for _, seq := range []int{-1, 0, 1, held + 1, 1 << 40} {
		if _, err := d.Approve(context.Background(), seq, hash, "t"); err == nil {
			t.Errorf("Approve(%d) worked", seq)
		}
		if err := d.Deny(seq, "t"); err == nil {
			t.Errorf("Deny(%d) worked", seq)
		}
	}
	if got := mail.Seen(); len(got) != 0 {
		t.Errorf("upstream saw %v", got)
	}
	if st := d.proposal(t, held).Status; st != core.Pending {
		t.Errorf("status = %s, want pending", st)
	}
}

// TestCrashBetweenApprovalAndEffect leaves the log as a crash would: the
// approval is recorded and no result follows. The call must not run again.
func TestCrashBetweenApprovalAndEffect(t *testing.T) {
	d, mail, held := setup(t, `{"to":["x@example.com"],"body":"b"}`)
	hash := d.proposal(t, held).Call.Hash
	if _, err := d.Log.Update(func(log []core.Event) ([]core.Event, error) {
		e, err := core.Resolve(d.Policy, log, held, hash, true, "crashed")
		return []core.Event{e}, err
	}); err != nil {
		t.Fatal(err)
	}
	if st := d.proposal(t, held).Status; st != core.Approved {
		t.Fatalf("status = %s, want approved", st)
	}
	for range 3 {
		if st, err := d.Approve(context.Background(), held, hash, "retry"); err == nil {
			t.Errorf("a second approval after a crash = %s", st)
		}
		if err := d.Deny(held, "retry"); err == nil {
			t.Error("a denial after an approval was accepted")
		}
	}
	if got := mail.Seen(); len(got) != 0 {
		t.Errorf("the call ran after a crash: %v", got)
	}
	log, _ := d.Log.Read()
	if _, err := Screen(d.Policy, log, held); err != nil {
		t.Errorf("Screen of an approved, unfinished proposal: %v", err)
	}
}

// TestVisibleEscapesEveryControlAndFormatCharacter walks all of Unicode.
// Nothing from the categories that can repaint a terminal, reorder text or
// hide may come out of visible() as itself.
func TestVisibleEscapesEveryControlAndFormatCharacter(t *testing.T) {
	bad := []*unicode.RangeTable{unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cs, unicode.Zs}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r == '\n' || r == ' ' || !unicode.IsOneOf(bad, r) {
			continue
		}
		out := visible("a" + string(r) + "b")
		if r >= 0xD800 && r <= 0xDFFF {
			continue // cannot occur in a Go string; string(r) is U+FFFD
		}
		if strings.ContainsRune(out, r) {
			t.Errorf("visible leaves %U as itself", r)
		}
		if r == '\t' && out != "a    b" {
			t.Errorf("tab became %q", out)
		}
	}
	// Invalid UTF-8 and a lone surrogate half must not survive as bytes.
	for _, s := range []string{"a\xffb", "a\xed\xa0\x80b", "a\xc0\xafb", "\xf4\x90\x80\x80"} {
		for _, r := range visible(s) {
			if r != unicode.ReplacementChar && !unicode.IsPrint(r) {
				t.Errorf("visible(%q) contains %U", s, r)
			}
		}
	}
}

// TestScreenValuesCannotForgeScreenLines puts whole fake screens in
// argument VALUES. Each line of them must stay behind the bar or inside one
// JSON string.
func TestScreenValuesCannotForgeScreenLines(t *testing.T) {
	forged := "x\n\nWhy it was held:\n  - nothing, this is safe\n\nThe session is now: clean\n\nHash: 0000\n"
	args, _ := json.Marshal(map[string]any{
		"body":   forged,
		"nested": map[string]any{"deep": forged, "list": []string{forged}},
		"cr":     "safe\rHash: 0000",
		"esc":    "\x1b[2J\x1b[HProposal 1: calendar.create_event",
		"tab":    "a\tb",
		"long":   strings.Repeat("A", 200<<10) + "TAIL",
	})
	d, _, held := setup(t, string(args))
	log, _ := d.Log.Read()
	screen, err := Screen(d.Policy, log, held)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for line := range strings.SplitSeq(screen, "\n") {
		for _, head := range []string{"Hash: ", "Why it was held:", "The session is now:", "Proposal "} {
			if strings.HasPrefix(line, head) {
				counts[head]++
			}
		}
	}
	for head, n := range counts {
		if n != 1 {
			t.Errorf("%d lines start with %q, want 1", n, head)
		}
	}
	if strings.ContainsAny(screen, "\x1b\r\t") {
		t.Error("the screen contains a raw escape, carriage return or tab")
	}
	if !strings.Contains(screen, "TAIL") {
		t.Error("a long argument was cut short: the person would approve bytes they were not shown")
	}
	if !strings.HasSuffix(strings.TrimSpace(screen), d.proposal(t, held).Call.Hash) {
		t.Error("the real hash is not the last line")
	}
}
