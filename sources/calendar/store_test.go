package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestStore returns a store with a fixed clock and sequential ids.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC) }
	var mu sync.Mutex
	n := 0
	s.newID = func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("id%d", n)
	}
	return s
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateEventWritesICS(t *testing.T) {
	s := newTestStore(t)
	ev, err := s.CreateEvent(EventInput{Title: "Offsite", Date: "2026-03-10", EndDate: "2026-03-12", Location: "Lisbon"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.UID != "id1@cleanroom" {
		t.Errorf("uid = %q", ev.UID)
	}
	want := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//cleanroom//calendar-source//EN",
		"CALSCALE:GREGORIAN",
		"BEGIN:VEVENT",
		"UID:id1@cleanroom",
		"DTSTAMP:20260301T093000Z",
		"DTSTART;VALUE=DATE:20260310",
		"DTEND;VALUE=DATE:20260313",
		"SUMMARY:Offsite",
		"LOCATION:Lisbon",
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")
	if got := readFile(t, s.ICSPath()); got != want {
		t.Errorf("ics mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEndDateDefaultsToDate(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateEvent(EventInput{Title: "Day", Date: "2026-12-31"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, s.ICSPath()); !strings.Contains(got, "DTEND;VALUE=DATE:20270101\r\n") {
		t.Errorf("DTEND should roll over the year:\n%s", got)
	}
	events, _ := s.ListEvents()
	if events[0].EndDate != "2026-12-31" {
		t.Errorf("end_date = %q", events[0].EndDate)
	}
}

func TestEscaping(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateEvent(EventInput{
		Title:       "a,b;c\\d\r\nEND:VEVENT",
		Date:        "2026-03-10",
		Description: "line1\nline2\rline3",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, s.ICSPath())
	for _, want := range []string{
		"SUMMARY:a\\,b\\;c\\\\d\\nEND:VEVENT\r\n",
		"DESCRIPTION:line1\\nline2\\nline3\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\r\nEND:VEVENT\r\n"); n != 1 {
		t.Errorf("END:VEVENT lines = %d, want 1", n)
	}
}

func TestFoldingKeepsUTF8Intact(t *testing.T) {
	title := strings.Repeat("日本語のイベント", 12)
	got := fold("SUMMARY:" + escapeText(title))
	for _, l := range strings.Split(got, "\r\n") {
		if len(l) > maxLineBytes {
			t.Errorf("line of %d octets: %q", len(l), l)
		}
	}
	unfolded := strings.ReplaceAll(got, "\r\n ", "")
	if unfolded != "SUMMARY:"+title {
		t.Error("unfolding did not restore the original")
	}
}

func TestReminders(t *testing.T) {
	s := newTestStore(t)
	r, err := s.CreateReminder(ReminderInput{Title: "Renew passport", Due: "2026-04-01", Note: "bring photos"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "id1" {
		t.Errorf("id = %q", r.ID)
	}
	list, err := s.ListReminders()
	if err != nil || len(list) != 1 || list[0] != r {
		t.Errorf("list = %v, %v", list, err)
	}
}

func TestEmptyListsAreNotNil(t *testing.T) {
	s := newTestStore(t)
	if e, _ := s.ListEvents(); e == nil {
		t.Error("events is nil")
	}
	if r, _ := s.ListReminders(); r == nil {
		t.Error("reminders is nil")
	}
}

func TestValidation(t *testing.T) {
	s := newTestStore(t)
	long := func(n int) string { return strings.Repeat("x", n) }
	events := map[string]EventInput{
		"empty title":      {Title: "  ", Date: "2026-03-10"},
		"long title":       {Title: long(201), Date: "2026-03-10"},
		"long location":    {Title: "t", Date: "2026-03-10", Location: long(201)},
		"long description": {Title: "t", Date: "2026-03-10", Description: long(2001)},
		"bad date":         {Title: "t", Date: "2026-02-30"},
		"bad format":       {Title: "t", Date: "10/03/2026"},
		"bad end date":     {Title: "t", Date: "2026-03-10", EndDate: "nope"},
		"end before start": {Title: "t", Date: "2026-03-10", EndDate: "2026-03-09"},
		"invalid utf8":     {Title: "\xff", Date: "2026-03-10"},
	}
	for name, in := range events {
		if _, err := s.CreateEvent(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("event %s: err = %v", name, err)
		}
	}
	reminders := map[string]ReminderInput{
		"empty title": {Title: "", Due: "2026-03-10"},
		"bad due":     {Title: "t", Due: "soon"},
		"long note":   {Title: "t", Due: "2026-03-10", Note: long(2001)},
	}
	for name, in := range reminders {
		if _, err := s.CreateReminder(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("reminder %s: err = %v", name, err)
		}
	}
	if list, _ := s.ListEvents(); len(list) != 0 {
		t.Errorf("rejected events were stored: %v", list)
	}
}

func TestConcurrentCreates(t *testing.T) {
	s := newTestStore(t)
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CreateEvent(EventInput{Title: fmt.Sprintf("event %d", i), Date: "2026-03-10"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	events, err := s.ListEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	if got := strings.Count(readFile(t, s.ICSPath()), "BEGIN:VEVENT"); got != n {
		t.Errorf("ics has %d events, want %d", got, n)
	}
}

func TestPersistsAcrossStores(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateEvent(EventInput{Title: "kept", Date: "2026-03-10"}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if events, _ := s2.ListEvents(); len(events) != 1 || events[0].Title != "kept" {
		t.Errorf("events = %v", events)
	}
}
