//go:build unix

package eventlog

// Adversarial tests for the log file that pass: many writers in one
// process and in several, and damage to the file that must be an error.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"cleanroom/internal/core"
)

const (
	helperDirEnv    = "CLEANROOM_EVENTLOG_HELPER_DIR"
	helperNameEnv   = "CLEANROOM_EVENTLOG_HELPER_NAME"
	helperRoundsEnv = "CLEANROOM_EVENTLOG_HELPER_ROUNDS"
)

// appendRounds appends rounds events as worker. Each event records how
// long the log was when the worker looked, in Question, so the reader of
// the log can tell whether look-then-append was atomic.
func appendRounds(dir, worker string, rounds int) error {
	l, err := Open(dir)
	if err != nil {
		return err
	}
	for i := 0; i < rounds; i++ {
		_, err := l.Update(func(log []core.Event) ([]core.Event, error) {
			e := call(worker)
			e.Call.Call.Question = strconv.Itoa(len(log))
			e.Call.Call.Answer = i + 1
			if i%3 == 0 { // sometimes two events at once
				return []core.Event{e, result(len(log) + 1)}, nil
			}
			return []core.Event{e}, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// TestHelperProcess is not a test. The test below runs this binary again
// with the environment set, to get writers in separate processes.
func TestHelperProcess(t *testing.T) {
	dir := os.Getenv(helperDirEnv)
	if dir == "" {
		t.Skip("only runs as a child of TestManyProcessesAndGoroutines")
	}
	rounds, _ := strconv.Atoi(os.Getenv(helperRoundsEnv))
	if err := appendRounds(dir, os.Getenv(helperNameEnv), rounds); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(3)
	}
}

func TestManyProcessesAndGoroutines(t *testing.T) {
	const processes, goroutines, rounds = 6, 6, 40
	dir := t.TempDir()

	var wg sync.WaitGroup
	errs := make(chan error, processes+goroutines)
	for p := 0; p < processes; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
			cmd.Env = append(os.Environ(), helperDirEnv+"="+dir, helperNameEnv+"=p"+strconv.Itoa(p), helperRoundsEnv+"="+strconv.Itoa(rounds))
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("process %d: %v\n%s", p, err, out)
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := appendRounds(dir, "g"+strconv.Itoa(g), rounds); err != nil {
				errs <- fmt.Errorf("goroutine %d: %v", g, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	l, _ := Open(dir)
	log, err := l.Read()
	if err != nil {
		t.Fatalf("the log does not read back: %v", err)
	}
	if err := core.CheckLog(log); err != nil {
		t.Fatal(err)
	}
	next := map[string]int{}
	calls := 0
	for i, e := range log {
		if e.Seq != i+1 {
			t.Fatalf("event at position %d has seq %d", i+1, e.Seq)
		}
		if e.Kind != core.KindCall {
			continue
		}
		calls++
		c := e.Call.Call
		if c.Question != strconv.Itoa(i) {
			t.Fatalf("event %d was written by a worker that saw a log of %s events: look-then-append was not atomic", e.Seq, c.Question)
		}
		next[c.Session]++
		if c.Answer != next[c.Session] {
			t.Fatalf("worker %s: round %d arrived as its event number %d", c.Session, c.Answer, next[c.Session])
		}
	}
	if want := (processes + goroutines) * rounds; calls != want || len(next) != processes+goroutines {
		t.Errorf("%d calls from %d workers, want %d from %d", calls, len(next), want, processes+goroutines)
	}
}

// sound writes a log of nine rounds and returns it with its path and bytes.
func sound(t *testing.T) (*Log, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	if err := appendRounds(dir, "w", 9); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := Open(dir)
	return l, path, raw
}

// lines splits a log into its lines, each with its newline but the last.
func lines(raw []byte) [][]byte {
	return bytes.SplitAfter(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
}

// TestDamagedFilesAreErrors damages a sound log in ways that change its
// structure. Each must make Read and Update fail and leave the file alone.
func TestDamagedFilesAreErrors(t *testing.T) {

	damage := map[string]func(raw []byte) []byte{
		"torn last line":                      func(raw []byte) []byte { return raw[:len(raw)-7] },
		"torn after first byte":               func(raw []byte) []byte { return append(raw, '{') },
		"last newline missing but line whole": nil, // see below: must still read as the full log
		"first line removed":                  func(raw []byte) []byte { return bytes.Join(lines(raw)[1:], nil) },
		"middle line removed": func(raw []byte) []byte {
			l := lines(raw)
			return append(bytes.Join(l[:4], nil), bytes.Join(l[5:], nil)...)
		},
		"two lines swapped": func(raw []byte) []byte {
			l := lines(raw)
			l[2], l[3] = l[3], l[2]
			return append(bytes.Join(l, nil), '\n')
		},
		"line duplicated": func(raw []byte) []byte {
			l := lines(raw)
			return append(bytes.Join(l[:3], nil), bytes.Join(l[2:], nil)...)
		},
		"blank line inside": func(raw []byte) []byte {
			l := lines(raw)
			return append(append(bytes.Join(l[:3], nil), '\n'), bytes.Join(l[3:], nil)...)
		},
		"null line":          func(raw []byte) []byte { return append(raw, "null\n"...) },
		"NUL bytes appended": func(raw []byte) []byte { return append(raw, 0, 0, 0, 0) },
		"garbage appended":   func(raw []byte) []byte { return append(raw, "not json\n"...) },
		"kind changed": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"kind":"call"`), []byte(`"kind":"cull"`), 1)
		},
		"payload removed": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"call":{"call"`), []byte(`"cal1":{"call"`), 1)
		},
		"label misspelt": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"gains":[]`), []byte(`"gains":["contaminate"]`), 1)
		},
		"reference to the future": func(raw []byte) []byte { return bytes.Replace(raw, []byte(`"ref":1,`), []byte(`"ref":9,`), 1) },
		"two events on one line":  func(raw []byte) []byte { return bytes.Replace(raw, []byte("}\n{"), []byte("} {"), 1) },
	}
	for name, fn := range damage {
		if fn == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			l, path, raw := sound(t)
			bad := fn(append([]byte(nil), raw...))
			if bytes.Equal(bad, raw) {
				t.Fatal("the damage changed nothing")
			}
			if err := os.WriteFile(path, bad, 0o600); err != nil {
				t.Fatal(err)
			}
			if log, err := l.Read(); err == nil {
				t.Errorf("Read gave %d events and no error", len(log))
			}
			if _, err := l.Update(func([]core.Event) ([]core.Event, error) { return []core.Event{call("late")}, nil }); err == nil {
				t.Error("Update appended to a damaged log")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, bad) {
				t.Error("a failed Update changed the file")
			}
		})
	}

	// A whole event that lost its newline is still an event. Appending
	// after it used to put two events on one line, which made the log
	// unreadable for good.
	t.Run("whole last line without its newline", func(t *testing.T) {
		l, path, raw := sound(t)
		if err := os.WriteFile(path, bytes.TrimSuffix(raw, []byte("\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		before := len(lines(raw))
		if log, err := l.Read(); err != nil || len(log) != before {
			t.Fatalf("Read gave %d of %d events, %v", len(log), before, err)
		}
		if _, err := l.Update(func([]core.Event) ([]core.Event, error) { return []core.Event{call("late")}, nil }); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if log, err := l.Read(); err != nil || len(log) != before+1 {
			t.Errorf("after the append, Read gave %d events, %v; want %d", len(log), err, before+1)
		}
	})
}

// TestFailedAppendIsTakenBack fills the disk halfway through an append.
// The half that was written must be gone before the lock is released, or
// the log would end in a cut-off line and stop every later decision.
func TestFailedAppendIsTakenBack(t *testing.T) {
	l, path, raw := sound(t)
	l.write = func(f *os.File, b []byte) error {
		f.Write(b[:len(b)/2])
		return errors.New("no space left on device")
	}
	if _, err := l.Update(func([]core.Event) ([]core.Event, error) { return []core.Event{call("late")}, nil }); err == nil {
		t.Fatal("Update reported success for an append that failed")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) {
		t.Errorf("the failed append left %d bytes behind", len(after)-len(raw))
	}
	l.write = writeAndSync
	if _, err := l.Update(func([]core.Event) ([]core.Event, error) { return []core.Event{call("late")}, nil }); err != nil {
		t.Errorf("the next append failed: %v", err)
	}
}

// TestRepairSetsAsideOnlyACutOffLastLine covers the one kind of damage a
// crash can cause, and checks that Repair will not touch any other kind.
func TestRepairSetsAsideOnlyACutOffLastLine(t *testing.T) {
	t.Run("cut-off last line", func(t *testing.T) {
		l, path, raw := sound(t)
		before := len(lines(raw))
		torn := []byte(`{"seq":99,"kind":"call","call":{"call":{"sess`)
		if err := os.WriteFile(path, append(append([]byte(nil), raw...), torn...), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Read(); !errors.Is(err, ErrCutOff) {
			t.Fatalf("Read: %v, want ErrCutOff", err)
		}
		if repaired, err := l.Repair(); err != nil || !repaired {
			t.Fatalf("Repair = %v, %v", repaired, err)
		}
		if log, err := l.Read(); err != nil || len(log) != before {
			t.Errorf("after the repair, Read gave %d events, %v; want the %d sound ones", len(log), err, before)
		}
		if aside, _ := os.ReadFile(path + cutOffSuffix); !bytes.Equal(aside, append(torn, '\n')) {
			t.Errorf("the line set aside is %q, want %q", aside, torn)
		}
		if _, err := l.Update(func([]core.Event) ([]core.Event, error) { return []core.Event{call("late")}, nil }); err != nil {
			t.Errorf("appending after the repair: %v", err)
		}
	})
	t.Run("sound log", func(t *testing.T) {
		l, path, raw := sound(t)
		if repaired, err := l.Repair(); err != nil || repaired {
			t.Errorf("Repair = %v, %v; want nothing to do", repaired, err)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) {
			t.Error("Repair changed a sound log")
		}
	})
	t.Run("damage before the last line", func(t *testing.T) {
		l, path, raw := sound(t)
		// A line is missing from the middle and the last line is cut
		// off as well. Setting the last line aside would leave a log
		// that reads cleanly and is missing events.
		ls := lines(raw)
		bad := append(bytes.Join(ls[:2], nil), bytes.Join(ls[3:], nil)...)
		bad = bad[:len(bad)-7]
		if err := os.WriteFile(path, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if repaired, err := l.Repair(); err == nil || repaired || errors.Is(err, ErrCutOff) {
			t.Errorf("Repair = %v, %v; want it to refuse", repaired, err)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(after, bad) {
			t.Error("Repair changed a log it should not have touched")
		}
	})
}

// FuzzReadNeverShortens writes arbitrary bytes after a sound log. Read
// must fail or return at least the sound events, unchanged.
func FuzzReadNeverShortens(f *testing.F) {
	f.Add([]byte(""), 0)
	f.Add([]byte("{}\n"), 0)
	f.Add([]byte(`{"seq":3,"kind":"result","result":{"ref":1}}`+"\n"), 0)
	f.Add([]byte("\n"), 5)
	f.Add([]byte("\r\n"), 20)
	f.Fuzz(func(t *testing.T, tail []byte, cut int) {
		dir := t.TempDir()
		if err := appendRounds(dir, "w", 2); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, FileName)
		raw, _ := os.ReadFile(path)
		l, _ := Open(dir)
		want, err := l.Read()
		if err != nil {
			t.Fatal(err)
		}
		if cut < 0 {
			cut = -cut
		}
		cut %= len(raw)
		// Either keep the whole log and add a tail, or cut inside it.
		bad := append(append([]byte(nil), raw...), tail...)
		if cut > 0 && !bytes.HasSuffix(raw[:cut], []byte("\n")) {
			bad = append(append([]byte(nil), raw[:cut]...), tail...)
		}
		os.WriteFile(path, bad, 0o600)
		got, err := l.Read()
		if err != nil {
			return
		}
		if len(got) < len(want) {
			t.Fatalf("Read gave %d events, the sound log had %d, and there was no error\nfile: %q", len(got), len(want), bad)
		}
	})
}
