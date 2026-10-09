package main

// Adversarial tests for sending that pass: header injection, SMTP command
// injection and ways around the recipient allowlist. What got through is in

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

var wantHeaders = map[string]bool{
	"Date": true, "From": true, "To": true, "Subject": true, "Message-Id": true,
	"Mime-Version": true, "Content-Type": true, "Content-Transfer-Encoding": true,
}

// checkBuilt parses a built message the way a mail server would and
// compares it with what was asked for.
func checkBuilt(t *testing.T, out Outgoing, subject, body string) {
	t.Helper()
	from := &mail.Address{Address: "assistant@cleanroom.test"}
	data, err := out.build(from, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), "<id@cleanroom.test>")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range data {
		if b >= 0x80 || (b < 0x20 && b != '\r' && b != '\n' && b != '\t') {
			t.Fatalf("byte %#x on the wire in %q", b, data)
		}
	}
	if bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) ||
		bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\r")) {
		t.Fatalf("bare CR or LF on the wire: %q", data)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the message does not parse: %v\n%q", err, data)
	}
	for key, values := range msg.Header {
		if !wantHeaders[key] || len(values) != 1 {
			t.Fatalf("header %q appears %d times in %q", key, len(values), data)
		}
	}
	if len(msg.Header) != len(wantHeaders) {
		t.Fatalf("headers: %v", msg.Header)
	}
	to, err := msg.Header.AddressList("To")
	if err != nil {
		t.Fatalf("To does not parse: %v (%q)", err, msg.Header.Get("To"))
	}
	if len(to) != len(out.To) {
		t.Fatalf("To has %d addresses, want %d: %q", len(to), len(out.To), msg.Header.Get("To"))
	}
	for i := range to {
		if to[i].Address != out.To[i] || to[i].Name != "" {
			t.Fatalf("To[%d] = %+v, want %q", i, to[i], out.To[i])
		}
	}
	if got, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject")); err != nil || strings.TrimSpace(got) != strings.TrimSpace(subject) {
		t.Fatalf("Subject = %q, %v, want %q", got, err, subject)
	}
	got, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatalf("body does not decode: %v", err)
	}
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n") }
	if norm(string(got)) != norm(body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func FuzzOutgoingRoundTrip(f *testing.F) {
	f.Add("a@example.com", "Hello", "body")
	f.Add("Ann <ann@example.com>", "hi\r\nBcc: x@example.com", "line\r\n.\r\nMAIL FROM:<x@y.z>")
	f.Add(`"a\r\nBcc: e@evil.example"@example.com`, "=?utf-8?q?x?=", "\r\n\r\nSubject: second")
	f.Add(`"quoted \" local"@example.com`, "tab\tsubject \u2028 x", "a\rb\nc\r\n")
	f.Add("a@[127.0.0.1]", strings.Repeat("é", 100), strings.Repeat("=", 200))
	f.Add("a@b.c, d@e.f", "s", "b")
	f.Add("a@b.c (comment\r\n more)", "s", "From me\n.\n..\n")
	f.Fuzz(func(t *testing.T, to, subject, body string) {
		out, err := newOutgoing([]string{to}, subject, body)
		if err != nil {
			return
		}
		for _, addr := range out.To {
			for _, r := range addr {
				if r <= ' ' || r >= 0x7f || r == '<' || r == '>' {
					t.Fatalf("recipient %q contains %q", addr, r)
				}
			}
			if !strings.Contains(addr, "@") {
				t.Fatalf("recipient %q has no @", addr)
			}
		}
		checkBuilt(t, out, subject, body)
	})
}

// TestSMTPSessionHasNoExtraCommands sends bodies and recipients that try
// to end the DATA section early or start a second message.
func TestSMTPSessionHasNoExtraCommands(t *testing.T) {
	bodies := []string{
		"hi\r\n.\r\nMAIL FROM:<a@b.c>\r\nRCPT TO:<eve@evil.example>\r\nDATA\r\nSubject: x\r\n\r\npwned\r\n.\r\n",
		"hi\n.\nRCPT TO:<eve@evil.example>\n",
		".\r\n", "\r.\r", ".", "..\n.\n...", "a\r\n.\rQUIT\r\n", strings.Repeat("x", 2000) + "\n.\n",
	}
	for _, body := range bodies {
		addr, result := fakeSMTP(t)
		m := newTestMailpit(t, "http://127.0.0.1:1", addr)
		out, err := newOutgoing([]string{"kira@example.com", "b@example.com"}, "s", body)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Send(context.Background(), out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		got := <-result
		var verbs []string
		for _, c := range got.commands {
			verb, _, _ := strings.Cut(c, " ")
			verbs = append(verbs, strings.ToUpper(verb))
		}
		if strings.Join(verbs, " ") != "EHLO MAIL RCPT RCPT DATA QUIT" {
			t.Errorf("body %q: SMTP commands were %q", body, got.commands)
		}
		if strings.Contains(strings.Join(got.commands, "\n"), "evil") {
			t.Errorf("body %q put a recipient in the envelope: %q", body, got.commands)
		}
	}
}

// TestAllowlistBypasses tries to reach eve while only kira is allowed.
// Each attempt must be refused, or must be delivered to kira and no one
// else.
func TestAllowlistBypasses(t *testing.T) {
	attempts := []string{
		"eve@evil.example",
		"kira@example.com, eve@evil.example",
		"kira@example.com; eve@evil.example",
		"kira@example.com eve@evil.example",
		"kira@example.com <eve@evil.example>",
		`"kira@example.com" <eve@evil.example>`,
		"eve@evil.example (kira@example.com)",
		"(kira@example.com) eve@evil.example",
		"kira@example.com(,)eve@evil.example",
		"<@example.com:eve@evil.example>",
		"<@evil.example:kira@example.com>",
		`"kira@example.com"@evil.example`,
		"kira@example.com@evil.example",
		"kira@example.com.evil.example",
		"kira@example.com.",
		"kira@example.com\x00@evil.example",
		"kira@example.com\r\nRCPT TO:<eve@evil.example>",
		"kira@example.com\nBcc: eve@evil.example",
		"kira@example.com>\r\nRCPT TO:<eve@evil.example",
		"kira@example.com%eve@evil.example",
		"eve%evil.example@example.com",
		"kira+eve@example.com",
		"Kira@example.com",       // Kelvin sign lower-cases to k
		"kıra@example.com",       // dotless i
		"KİRA@example.com",       // dotted capital I
		"kira@exаmple.com",       // Cyrillic a
		"kira@example.com\u200b", // zero width space
		"kira@EXAMPLE.COM　",
		"friends: eve@evil.example;",
		"kira@example.com, ",
		"=?utf-8?q?kira@example.com?= <eve@evil.example>",
		"kira@[evil.example]",
		"kira@example.com:25",
		"kira@example.com?to=eve@evil.example",
		"mailto:kira@example.com",
		"kira@example.com\t",
	}
	for _, attempt := range attempts {
		mb := &fakeMailbox{}
		cs := connect(t, mb, []string{"kira@example.com"})
		for _, to := range [][]string{{attempt}, {"kira@example.com", attempt}, {attempt, "KIRA@example.com"}} {
			call(t, cs, "send", map[string]any{"to": to, "subject": "s", "body": "b"})
		}
		for _, sent := range mb.sent {
			for _, rcpt := range sent.To {
				if !strings.EqualFold(rcpt, "kira@example.com") {
					t.Errorf("attempt %q: a message went to %q", attempt, rcpt)
				}
			}
		}
	}

	// And the allowlist as configured cannot be widened by its own syntax.
	if cfg, err := parseConfig(nil, func(k string) string {
		return map[string]string{"MAIL_ALLOWED_RECIPIENTS": "kira@example.com"}[k]
	}); err != nil || len(cfg.allowed) != 1 {
		t.Errorf("config: %+v, %v", cfg.allowed, err)
	}
}

func TestReadIDCannotLeaveTheMessagePath(t *testing.T) {
	m := newTestMailpit(t, "http://127.0.0.1:1", "127.0.0.1:1")
	for _, id := range []string{"../search?query=x", "a/b", "a?b", "a#b", "..", "a%2f..", "a b", "", "a\nb", strings.Repeat("a", 500), "é"} {
		if _, err := m.Read(context.Background(), id); err == nil || !strings.Contains(err.Error(), "invalid message id") {
			t.Errorf("Read(%q): %v, want it refused before any request", id, err)
		}
	}
}
