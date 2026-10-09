//go:build unix

// Package eventlog stores the event log in a file.
//
// The log is one JSON event per line, append-only, and shared by every
// cleanroom process on the machine: each agent session runs its own server,
// readers run in their own processes, and the person's command line is
// another. They coordinate through a file lock. Update holds the lock while
// it reads the log, lets the caller decide, and appends, so "decide on the
// current log and record the decision" is one atomic step across processes.
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"cleanroom/internal/core"
)

// FileName is the log's name inside the state directory.
const FileName = "events.jsonl"

// Log is a handle on the log file. It holds no events in memory: the file
// is the state. A Log is safe for concurrent use.
type Log struct {
	path string
	now  func() time.Time
}

// Open returns the log in dir, creating the directory if it does not exist.
// The directory and the file are readable only by the user, because the log
// holds the text of answers and of held calls.
func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dir, FileName), now: time.Now}, nil
}

// Read returns every event in the log.
func (l *Log) Read() ([]core.Event, error) {
	var events []core.Event
	_, err := l.Update(func(log []core.Event) ([]core.Event, error) {
		events = log
		return nil, nil
	})
	return events, err
}

// Update calls fn with the current log and appends the events fn returns,
// all under an exclusive lock. It assigns each new event its Seq and Time
// and returns the events as appended. If fn returns an error nothing is
// appended.
//
// fn should be quick and must not do I/O that depends on the outcome: decide,
// build events, return. Effects happen after Update returns.
func (l *Log) Update(fn func(log []core.Event) ([]core.Event, error)) ([]core.Event, error) {
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close() // also releases the lock

	if err := lock(f); err != nil {
		return nil, fmt.Errorf("locking %s: %w", l.path, err)
	}

	log, err := read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l.path, err)
	}
	added, err := fn(log)
	if err != nil || len(added) == 0 {
		return nil, err
	}

	var buf bytes.Buffer
	now := l.now().UTC()
	for i := range added {
		added[i].Seq = len(log) + 1 + i
		added[i].Time = now
		line, err := json.Marshal(added[i])
		if err != nil {
			return nil, fmt.Errorf("encoding event: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	// Check before writing that the log will still be sound. A fault
	// here is a bug in the caller, and must not reach the file.
	if err := core.CheckLog(append(log, added...)); err != nil {
		return nil, fmt.Errorf("refusing to append: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	return added, nil
}

// lock takes an exclusive lock on f, waiting for it. A signal can interrupt
// the wait, in which case it is simply tried again.
func lock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

// read parses the whole file. A log that does not parse or is not sound is
// an error, never a shorter log: dropping events could drop the one that
// contaminated a session.
func read(f *os.File) ([]core.Event, error) {
	var log []core.Event
	// One event per line, and nothing else: a blank line or two events
	// on a line is damage. ReadBytes has no limit on a line's length, so
	// no event that was written can make the log unreadable.
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e core.Event
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, fmt.Errorf("line %d is not an event: %w", len(log)+1, err)
			}
			log = append(log, e)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if err := core.CheckLog(log); err != nil {
		return nil, err
	}
	return log, nil
}
