package main

// Adversarial tests for the calendar that pass: property injection through
// every text field, folding, and symlinks planted in the data directory.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

var icsNames = map[string]bool{
	"BEGIN": true, "END": true, "VERSION": true, "PRODID": true, "CALSCALE": true, "UID": true, "DTSTAMP": true,
	"DTSTART;VALUE=DATE": true, "DTEND;VALUE=DATE": true, "SUMMARY": true, "LOCATION": true, "DESCRIPTION": true,
}

// FuzzStoreICS goes through the store, validation included, and reads the
// file back. Each event must be one VEVENT with one of each property, and
// no text may come out as a property of its own.
func FuzzStoreICS(f *testing.F) {
	f.Add("Meeting", "Room 1", "Agenda")
	f.Add("x\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nSUMMARY:forged", "L\nATTENDEE:mailto:eve@evil.example", "d\rDTSTART:19700101")
	f.Add("a\\nb", "\\", "END:VCALENDAR")
	f.Add("t\u2028BEGIN:VALARM", "l\u0085ACTION:EMAIL", "d\vx\fy")
	f.Add(strings.Repeat("é", 200), strings.Repeat("日", 200), strings.Repeat("\\,;", 600))
	f.Add("a:b;c=d", ";VALUE=URI:http://evil.example", "SUMMARY;LANGUAGE=en:x")
	f.Fuzz(func(t *testing.T, title, location, description string) {
		s := newTestStore(t)
		if _, err := s.CreateEvent(EventInput{Title: title, Date: "2026-03-10", Location: location, Description: description}); err != nil {
			return
		}
		if _, err := s.CreateEvent(EventInput{Title: "second", Date: "2026-03-11"}); err != nil {
			t.Fatal(err)
		}
		ics := readFile(t, s.ICSPath())
		if !utf8.ValidString(ics) {
			t.Fatalf("the file is not UTF-8: %q", ics)
		}
		counts := map[string]int{}
		var got = map[string]string{}
		events := 0
		for _, l := range unfold(t, ics) {
			name, value, ok := strings.Cut(l, ":")
			if !ok || !icsNames[name] {
				t.Fatalf("unexpected content line %q", l)
			}
			if l == "BEGIN:VEVENT" {
				events++
			}
			if events == 1 {
				counts[name]++
				got[name] = value
			}
		}
		if events != 2 {
			t.Fatalf("%d events in the file, want 2", events)
		}
		want := map[string]string{"SUMMARY": title, "LOCATION": location, "DESCRIPTION": description}
		for name, text := range want {
			n := 1
			if text == "" {
				n = 0
			}
			if counts[name] != n {
				t.Fatalf("%s appears %d times in the first event, want %d", name, counts[name], n)
			}
			if n == 1 && unescape(got[name]) != wantText(text) {
				t.Fatalf("%s = %q, want %q", name, unescape(got[name]), wantText(text))
			}
		}
		if counts["UID"] != 1 || counts["DTSTART;VALUE=DATE"] != 1 || counts["DTEND;VALUE=DATE"] != 1 || counts["END"] != 1 {
			t.Fatalf("first event properties: %v", counts)
		}
	})
}

// TestSymlinksInTheDataDirAreNotFollowedForWriting plants links where the
// store writes. The files they point at must be left alone.
func TestSymlinksInTheDataDirAreNotFollowedForWriting(t *testing.T) {
	outside := t.TempDir()
	dir := t.TempDir()
	for _, name := range []string{icsFile, remindersFile} {
		target := filepath.Join(outside, name)
		if err := os.WriteFile(target, []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEvent(EventInput{Title: "x", Date: "2026-03-10"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateReminder(ReminderInput{Title: "x", Due: "2026-03-10"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{icsFile, remindersFile} {
		if got := readFile(t, filepath.Join(outside, name)); got != "[]" {
			t.Errorf("writing %s went through a symlink and changed the file outside the directory: %q", name, got)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

// TestConcurrentWritersInOneStore is the in-process case: nothing is lost
// and the file is whole after every write.
func TestConcurrentWritersInOneStore(t *testing.T) {
	s := newTestStore(t)
	const writers, each = 8, 15
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := s.CreateEvent(EventInput{Title: "e", Date: "2026-03-10"}); err != nil {
					t.Error(err)
				}
				if _, err := s.CreateReminder(ReminderInput{Title: "r", Due: "2026-03-10"}); err != nil {
					t.Error(err)
				}
				if strings.Count(readFile(t, s.ICSPath()), "BEGIN:VEVENT") != strings.Count(readFile(t, s.ICSPath()), "END:VEVENT") {
					t.Error("a reader saw a partial calendar.ics")
				}
			}
		}()
	}
	wg.Wait()
	events, _ := s.ListEvents()
	reminders, _ := s.ListReminders()
	if len(events) != writers*each || len(reminders) != writers*each {
		t.Errorf("%d events and %d reminders, want %d of each", len(events), len(reminders), writers*each)
	}
	if n := strings.Count(readFile(t, s.ICSPath()), "BEGIN:VEVENT"); n != writers*each {
		t.Errorf("calendar.ics has %d events, want %d", n, writers*each)
	}
}

func TestOddDatesAreRefused(t *testing.T) {
	s := newTestStore(t)
	for _, date := range []string{"", "2026-3-10", "2026-02-30", "20260310", "2026-03-10T00:00:00Z", "2026-03-10\r\nDTEND:19700101", " 2026-03-10", "2026-03-10 ", "+2026-03-10", "-001-01-01", "10000-01-01"} {
		if _, err := s.CreateEvent(EventInput{Title: "x", Date: date}); err == nil {
			t.Errorf("date %q was accepted", date)
		}
		if _, err := s.CreateEvent(EventInput{Title: "x", Date: "2026-03-10", EndDate: date}); err == nil && date != "" {
			t.Errorf("end_date %q was accepted", date)
		}
		if _, err := s.CreateReminder(ReminderInput{Title: "x", Due: date}); err == nil {
			t.Errorf("due %q was accepted", date)
		}
	}
}
