package main

import (
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestNewOutgoingRejects(t *testing.T) {
	ok := []string{"a@example.com"}
	tests := []struct {
		name    string
		to      []string
		subject string
		body    string
	}{
		{"no recipients", nil, "s", "b"},
		{"too many recipients", make([]string, maxRecipients+1), "s", "b"},
		{"bad address", []string{"not an address"}, "s", "b"},
		// A quoted local part that would become a source route in RCPT TO.
		{"source route in a quoted local part", []string{`"@evil.example:kira"@example.com`}, "s", "b"},
		{"quoted local part", []string{`"a b"@example.com`}, "s", "b"},
		{"newline in address", []string{"a@example.com\r\nBcc: x@example.com"}, "s", "b"},
		{"quoted newline in address", []string{"\"a\r\nb\"@example.com"}, "s", "b"},
		{"non-ascii address", []string{"é@example.com"}, "s", "b"},
		{"empty subject", ok, "", "b"},
		{"CRLF subject", ok, "hi\r\nBcc: x@example.com", "b"},
		{"LF subject", ok, "hi\nthere", "b"},
		{"CR subject", ok, "hi\rthere", "b"},
		{"long subject", ok, strings.Repeat("s", maxSubjectBytes+1), "b"},
		{"invalid utf-8 subject", ok, "\xff", "b"},
		{"long body", ok, "s", strings.Repeat("b", maxBodyBytes+1)},
		{"NUL in body", ok, "s", "a\x00b"},
		{"invalid utf-8 body", ok, "s", "\xff"},
	}
	for _, tt := range tests {
		if _, err := newOutgoing(tt.to, tt.subject, tt.body); err == nil {
			t.Errorf("%s: expected an error", tt.name)
		}
	}
}

func TestNewOutgoingAccepts(t *testing.T) {
	out, err := newOutgoing([]string{"Ann <ann@example.com>", "bob@example.com"}, "Tab\there", "line one\nline two")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.To, ",") != "ann@example.com,bob@example.com" {
		t.Errorf("To = %v", out.To)
	}
}

func TestBuild(t *testing.T) {
	out, err := newOutgoing([]string{"bob@example.com"}, "Hi", "a\nb\r\nc\rd é")
	if err != nil {
		t.Fatal(err)
	}
	from := &mail.Address{Name: "Assistant", Address: "assistant@cleanroom.test"}
	raw, err := out.build(from, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), "<id@cleanroom.test>")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") || strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\r") {
		t.Errorf("bare CR or LF in output: %q", s)
	}
	for _, want := range []string{
		"Date: Sun, 01 Mar 2026 12:00:00 +0000\r\n",
		"From: \"Assistant\" <assistant@cleanroom.test>\r\n",
		"Message-ID: <id@cleanroom.test>\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\n\r\na\r\nb\r\nc",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

func TestBuildEncodesLongNonASCIISubject(t *testing.T) {
	out, err := newOutgoing([]string{"bob@example.com"}, strings.Repeat("é", 90), "b")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := out.build(&mail.Address{Address: "a@example.com"}, time.Now(), "<id@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Header["Subject"]) != 1 {
		t.Errorf("subject headers: %v", msg.Header["Subject"])
	}
}

func FuzzOutgoing(f *testing.F) {
	f.Add("a@example.com", "Hello", "body")
	f.Add("a@example.com\r\nBcc: x@y", "s", "b")
	f.Add("\"a\r\nb\"@example.com", "s", "b")
	f.Add("a@example.com", "s\r\nBcc: x@y", "b")
	f.Add("a@example.com", "Café \u2028 ünï", "x\r.\ry\n.\n")
	f.Add("<a@example.com>", "=?utf-8?q?x?=\n", "\x00")
	from := &mail.Address{Name: "Assistant", Address: "assistant@cleanroom.test"}
	wantHeaders := map[string]bool{
		"Date": true, "From": true, "To": true, "Subject": true, "Message-Id": true,
		"Mime-Version": true, "Content-Type": true, "Content-Transfer-Encoding": true,
	}
	f.Fuzz(func(t *testing.T, to, subject, body string) {
		out, err := newOutgoing([]string{to}, subject, body)
		if err != nil {
			return
		}
		raw, err := out.build(from, time.Unix(0, 0), "<id@cleanroom.test>")
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		// Every line ending is CRLF.
		if s := strings.ReplaceAll(string(raw), "\r\n", ""); strings.ContainsAny(s, "\r\n") {
			t.Fatalf("bare CR or LF in output: %q", raw)
		}
		// The header block parses to exactly the headers we wrote, so
		// nothing the caller supplied became a header of its own.
		msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatalf("output does not parse: %v\n%q", err, raw)
		}
		if len(msg.Header) != len(wantHeaders) {
			t.Fatalf("unexpected headers %v in %q", msg.Header, raw)
		}
		for k, v := range msg.Header {
			if !wantHeaders[k] || len(v) != 1 {
				t.Fatalf("unexpected header %q = %q", k, v)
			}
		}
		// The envelope address is a single clean token.
		for _, a := range out.To {
			if strings.ContainsAny(a, "\r\n <>") {
				t.Fatalf("bad envelope address %q", a)
			}
		}
	})
}
