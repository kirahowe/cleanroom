//go:build unix

package eventlog

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cleanroom/internal/core"
)

func result(ref int) core.Event {
	return core.Event{Kind: core.KindResult, Result: &core.Result{Ref: ref, OK: true}}
}

func call(session string) core.Event {
	return core.Event{Kind: core.KindCall, Call: &core.Decided{
		Call:     core.Call{Session: session, Op: core.OpTool},
		Decision: core.Decision{Verdict: core.Refuse},
	}}
}

func TestAppendAndRead(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return stamp }

	added, err := l.Update(func(log []core.Event) ([]core.Event, error) {
		if len(log) != 0 {
			t.Errorf("new log has %d events", len(log))
		}
		return []core.Event{call("a"), result(1)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 || added[0].Seq != 1 || added[1].Seq != 2 || !added[1].Time.Equal(stamp) {
		t.Errorf("added = %+v", added)
	}

	log, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0].Call.Call.Session != "a" || log[1].Result.Ref != 1 {
		t.Errorf("log = %+v", log)
	}
}

func TestNothingIsAppendedOnError(t *testing.T) {
	l, _ := Open(t.TempDir())
	boom := errors.New("boom")
	if _, err := l.Update(func([]core.Event) ([]core.Event, error) {
		return []core.Event{call("a")}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	// An event that would make the log unsound is refused too.
	if _, err := l.Update(func([]core.Event) ([]core.Event, error) {
		return []core.Event{result(5)}, nil
	}); err == nil {
		t.Fatal("a dangling reference was appended")
	}
	if log, _ := l.Read(); len(log) != 0 {
		t.Errorf("log has %d events, want none", len(log))
	}
}

func TestDamagedLogIsAnError(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	l.Update(func([]core.Event) ([]core.Event, error) { return []core.Event{call("a")}, nil })

	f, _ := os.OpenFile(filepath.Join(dir, FileName), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":2,"kind":"call","ca`)
	f.Close()

	if _, err := l.Read(); err == nil {
		t.Error("a log with a torn last line was read without error")
	}
}

func TestFileIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	l, _ := Open(dir)
	l.Update(func([]core.Event) ([]core.Event, error) { return []core.Event{call("a")}, nil })
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, FileName): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
}

// TestConcurrentWriters checks the property the whole design leans on:
// read-decide-append is atomic. Each writer appends an event that refers to
// the last event it saw. With separate handles, as separate processes would
// have, no two writers may ever see the same log.
func TestConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 8, 25

	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			l, err := Open(dir)
			if err != nil {
				t.Error(err)
				return
			}
			for range each {
				if _, err := l.Update(func(log []core.Event) ([]core.Event, error) {
					if len(log) == 0 {
						return []core.Event{call("first")}, nil
					}
					return []core.Event{call("next"), result(len(log) + 1)}, nil
				}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()

	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	log, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + (writers*each-1)*2; len(log) != want {
		t.Fatalf("log has %d events, want %d", len(log), want)
	}
	for i, e := range log {
		if e.Kind == core.KindResult && e.Result.Ref != i {
			t.Fatalf("event %d refers to %d: two writers saw the same log", e.Seq, e.Result.Ref)
		}
	}
}
