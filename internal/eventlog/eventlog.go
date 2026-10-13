//go:build unix

// Package eventlog stores the event log in a file.
//
// The log is one JSON event per line, append-only, and shared by every
// cleanroom process on the machine: each agent session runs its own server,
// readers run in their own processes, and the person's command line is
// another. They coordinate through a file lock. Update holds the lock while
// it reads the log, lets the caller decide, and appends, so "decide on the
// current log and record the decision" is one atomic step across processes.
//
// A log that cannot be read stops every decision, because a shorter log
// could be missing the event that contaminated a session. So the package
// tries not to produce one. An append that fails partway is taken back
// before the lock is released. A last line that is whole but has lost its
// newline is still read, and the next append supplies the newline. What is
// left is a last line cut off by a crash or a power cut in the middle of a
// write: that is reported as ErrCutOff, and Repair sets it aside. Damage
// anywhere else is never repaired here, because nothing can tell which
// events are missing.
package eventlog

import (
	"bytes"
	"encoding/json"
	"errors"
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

// cutOffSuffix is added to FileName for the file where Repair keeps the
// lines it sets aside.
const cutOffSuffix = ".cut-off"

// ErrCutOff reports that the log's last line is incomplete: an append began
// and did not finish. Every event before it is sound. Nothing was ever
// decided on the strength of that line, since an append is only reported as
// done once the whole line is on disk, so Repair may set it aside.
var ErrCutOff = errors.New("the last line of the log is cut off, so an append did not finish; run `cleanroom repair` to set it aside")

// Log is a handle on the log file. It holds no events in memory: the file
// is the state. A Log is safe for concurrent use.
type Log struct {
	path string
	now  func() time.Time
	// write puts an append on disk. Tests replace it to make one fail.
	write func(f *os.File, b []byte) error
}

// Open returns the log in dir, creating the directory if it does not exist.
// The directory and the file are readable only by the user, because the log
// holds the text of answers and of held calls.
func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dir, FileName), now: time.Now, write: writeAndSync}, nil
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
	f, err := l.openLocked()
	if err != nil {
		return nil, err
	}
	defer f.Close() // also releases the lock

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	log, err := parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l.path, err)
	}
	added, err := fn(log)
	if err != nil || len(added) == 0 {
		return nil, err
	}

	var buf bytes.Buffer
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		// The last event is whole but its newline is missing. Supply
		// it, or the new event would share its line.
		buf.WriteByte('\n')
	}
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
	if err := l.write(f, buf.Bytes()); err != nil {
		// Part of the append may be in the file: a full disk writes
		// what fits. Take it back while the lock is still held, so the
		// failure costs this one call and not the whole log.
		if undo := f.Truncate(int64(len(raw))); undo != nil {
			return nil, fmt.Errorf("%w (and the partial append could not be removed: %v)", err, undo)
		}
		return nil, err
	}
	return added, nil
}

func writeAndSync(f *os.File, b []byte) error {
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

// Repair sets aside a last line that was cut off, so that the log can be
// read again. It reports whether there was one. The line is kept in a file
// beside the log, not deleted.
//
// Repair touches nothing unless every event before the last line is sound.
// Damage anywhere else is returned as the error it is.
func (l *Log) Repair() (repaired bool, err error) {
	f, err := l.openLocked()
	if err != nil {
		return false, err
	}
	defer f.Close()

	raw, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	if _, err := parse(raw); !errors.Is(err, ErrCutOff) {
		return false, err // sound, or damaged in a way Repair must not hide
	}
	keep := bytes.LastIndexByte(raw, '\n') + 1
	aside, err := os.OpenFile(l.path+cutOffSuffix, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer aside.Close()
	if err := writeAndSync(aside, append(raw[keep:], '\n')); err != nil {
		return false, err
	}
	if err := f.Truncate(int64(keep)); err != nil {
		return false, err
	}
	return true, f.Sync()
}

// openLocked opens the log file and takes the exclusive lock on it. Closing
// the file releases the lock.
func (l *Log) openLocked() (*os.File, error) {
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", l.path, err)
	}
	return f, nil
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

// parse reads the whole file. A log that does not parse or is not sound is
// an error, never a shorter log: dropping events could drop the one that
// contaminated a session.
func parse(raw []byte) ([]core.Event, error) {
	var log []core.Event
	// One event per line, and nothing else: a blank line or two events
	// on a line is damage. A line has no length limit, so no event that
	// was written can make the log unreadable.
	for len(raw) > 0 {
		line, rest, terminated := bytes.Cut(raw, []byte("\n"))
		var e core.Event
		if err := json.Unmarshal(line, &e); err != nil {
			if !terminated {
				// Everything before this line must be sound for
				// it to count as a cut-off append and nothing
				// worse.
				if err := core.CheckLog(log); err != nil {
					return nil, err
				}
				return nil, ErrCutOff
			}
			return nil, fmt.Errorf("line %d is not an event: %w", len(log)+1, err)
		}
		log = append(log, e)
		raw = rest
	}
	if err := core.CheckLog(log); err != nil {
		return nil, err
	}
	return log, nil
}
