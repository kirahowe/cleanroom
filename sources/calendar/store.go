package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxShort = 200
	maxLong  = 2000

	icsFile       = "calendar.ics"
	eventsFile    = "events.json"
	remindersFile = "reminders.json"
)

// ErrInvalid marks errors caused by bad caller input.
var ErrInvalid = errors.New("invalid input")

// EventInput is the data needed to create an event.
type EventInput struct {
	Title       string `json:"title" jsonschema:"Event title, 1 to 200 characters."`
	Date        string `json:"date" jsonschema:"First day of the all-day event, formatted YYYY-MM-DD."`
	EndDate     string `json:"end_date,omitempty" jsonschema:"Last day of the event, inclusive, formatted YYYY-MM-DD. Defaults to date."`
	Location    string `json:"location,omitempty" jsonschema:"Where the event takes place, up to 200 characters."`
	Description string `json:"description,omitempty" jsonschema:"Longer notes about the event, up to 2000 characters."`
}

// Event is a calendar event as returned to callers.
type Event struct {
	UID         string `json:"uid" jsonschema:"Unique event id."`
	Title       string `json:"title"`
	Date        string `json:"date" jsonschema:"First day, YYYY-MM-DD."`
	EndDate     string `json:"end_date" jsonschema:"Last day, inclusive, YYYY-MM-DD."`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// storedEvent is an Event plus the creation time used for DTSTAMP.
type storedEvent struct {
	Event
	Created time.Time `json:"created"`
}

// ReminderInput is the data needed to create a reminder.
type ReminderInput struct {
	Title string `json:"title" jsonschema:"Reminder title, 1 to 200 characters."`
	Due   string `json:"due" jsonschema:"Due date, formatted YYYY-MM-DD."`
	Note  string `json:"note,omitempty" jsonschema:"Extra detail, up to 2000 characters."`
}

// Reminder is a reminder as stored and returned.
type Reminder struct {
	ID    string `json:"id" jsonschema:"Unique reminder id."`
	Title string `json:"title"`
	Due   string `json:"due" jsonschema:"Due date, YYYY-MM-DD."`
	Note  string `json:"note"`
}

// Store keeps events and reminders in a directory. Events live in
// events.json, and calendar.ics is regenerated from it after every change.
// That is simpler than parsing the .ics back and loses nothing. All
// operations hold one mutex, and files are replaced atomically.
type Store struct {
	dir   string
	mu    sync.Mutex
	now   func() time.Time
	newID func() string
}

// NewStore creates dir if needed and returns a Store using it.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, now: time.Now, newID: randomID}, nil
}

// ICSPath is the location of the generated calendar file.
func (s *Store) ICSPath() string { return filepath.Join(s.dir, icsFile) }

// RemindersPath is the location of the reminders file.
func (s *Store) RemindersPath() string { return filepath.Join(s.dir, remindersFile) }

// CreateEvent validates in, stores the event, and rewrites calendar.ics.
func (s *Store) CreateEvent(in EventInput) (Event, error) {
	if in.EndDate == "" {
		in.EndDate = in.Date
	}
	if err := validateEvent(in); err != nil {
		return Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	events, err := readJSON[storedEvent](filepath.Join(s.dir, eventsFile))
	if err != nil {
		return Event{}, err
	}
	ev := Event{
		UID: s.newID() + "@cleanroom", Title: in.Title, Date: in.Date, EndDate: in.EndDate,
		Location: in.Location, Description: in.Description,
	}
	events = append(events, storedEvent{Event: ev, Created: s.now().UTC()})
	if err := writeJSON(filepath.Join(s.dir, eventsFile), events); err != nil {
		return Event{}, err
	}
	if err := writeFile(s.ICSPath(), []byte(renderICS(events))); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// ListEvents returns all events in creation order.
func (s *Store) ListEvents() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := readJSON[storedEvent](filepath.Join(s.dir, eventsFile))
	if err != nil {
		return nil, err
	}
	events := make([]Event, len(stored))
	for i, e := range stored {
		events[i] = e.Event
	}
	return events, nil
}

// CreateReminder validates in and appends the reminder to reminders.json.
func (s *Store) CreateReminder(in ReminderInput) (Reminder, error) {
	if err := validateReminder(in); err != nil {
		return Reminder{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	reminders, err := readJSON[Reminder](s.RemindersPath())
	if err != nil {
		return Reminder{}, err
	}
	r := Reminder{ID: s.newID(), Title: in.Title, Due: in.Due, Note: in.Note}
	if err := writeJSON(s.RemindersPath(), append(reminders, r)); err != nil {
		return Reminder{}, err
	}
	return r, nil
}

// ListReminders returns all reminders in creation order.
func (s *Store) ListReminders() ([]Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSON[Reminder](s.RemindersPath())
}

func validateEvent(in EventInput) error {
	if err := validateText("title", in.Title, maxShort, true); err != nil {
		return err
	}
	if err := validateText("location", in.Location, maxShort, false); err != nil {
		return err
	}
	if err := validateText("description", in.Description, maxLong, false); err != nil {
		return err
	}
	start, err := parseDate("date", in.Date)
	if err != nil {
		return err
	}
	last, err := parseDate("end_date", in.EndDate)
	if err != nil {
		return err
	}
	if last.Before(start) {
		return fmt.Errorf("%w: end_date %s is before date %s", ErrInvalid, in.EndDate, in.Date)
	}
	return nil
}

func validateReminder(in ReminderInput) error {
	if err := validateText("title", in.Title, maxShort, true); err != nil {
		return err
	}
	if err := validateText("note", in.Note, maxLong, false); err != nil {
		return err
	}
	_, err := parseDate("due", in.Due)
	return err
}

func validateText(field, v string, max int, required bool) error {
	switch {
	case required && strings.TrimSpace(v) == "":
		return fmt.Errorf("%w: %s must not be empty", ErrInvalid, field)
	case !utf8.ValidString(v):
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, field)
	case utf8.RuneCountInString(v) > max:
		return fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, field, max)
	}
	return nil
}

func parseDate(field, v string) (time.Time, error) {
	t, err := time.Parse(dateLayout, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s %q is not a valid YYYY-MM-DD date", ErrInvalid, field, v)
	}
	return t, nil
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms.
	}
	return hex.EncodeToString(b)
}

// readJSON reads a JSON array. A missing file is an empty list.
func readJSON[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []T{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := []T{}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return items, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'))
}

// writeFile writes data to a temporary file in the same directory and
// renames it over path, so readers never see a partial file.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
