package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"testing"
	"time"
)

const searchJSON = `{"messages":[
 {"ID":"abc123","From":{"Name":"Ann","Address":"ann@example.com"},
  "To":[{"Name":"","Address":"bob@example.com"}],"Subject":"Lunch",
  "Created":"2026-03-01T12:00:00.5+01:00","Snippet":"Noon?"}]}`

func fakeMailpitAPI(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "from:ann lunch" || r.URL.Query().Get("limit") != "5" {
			http.Error(w, "bad query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		io.WriteString(w, searchJSON)
	})
	mux.HandleFunc("/api/v1/message/plain", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ID":"plain","From":{"Address":"ann@example.com"},"To":[],"Subject":"S","Date":"2026-03-01T11:00:00Z","Text":"hello","HTML":"<p>ignored</p>"}`)
	})
	mux.HandleFunc("/api/v1/message/html", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ID":"html","Text":"","HTML":"<html><head><title>x</title></head><body><style>p{}</style><p>Hi &amp; bye</p><p>Line<br>two</p></body></html>"}`)
	})
	mux.HandleFunc("/api/v1/message/huge", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Text":"`+strings.Repeat("a", maxResponseSize)+`"}`)
	})
	mux.HandleFunc("/api/v1/message/broken", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestMailpit(t *testing.T, apiURL, smtpAddr string) *mailpit {
	t.Helper()
	u, err := url.Parse(apiURL)
	if err != nil {
		t.Fatal(err)
	}
	return newMailpit(u, smtpAddr, &mail.Address{Address: "assistant@cleanroom.test"})
}

func TestMailpitSearch(t *testing.T) {
	mp := newTestMailpit(t, fakeMailpitAPI(t).URL, "")
	got, err := mp.Search(context.Background(), "from:ann lunch", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results", len(got))
	}
	want := Summary{ID: "abc123", From: `"Ann" <ann@example.com>`, To: []string{"bob@example.com"},
		Subject: "Lunch", Date: "2026-03-01T11:00:00Z", Snippet: "Noon?"}
	if g := got[0]; g.ID != want.ID || g.From != want.From || g.To[0] != want.To[0] ||
		g.Subject != want.Subject || g.Date != want.Date || g.Snippet != want.Snippet {
		t.Errorf("got %+v, want %+v", g, want)
	}
}

func TestMailpitRead(t *testing.T) {
	mp := newTestMailpit(t, fakeMailpitAPI(t).URL, "")
	ctx := context.Background()

	msg, err := mp.Read(ctx, "plain")
	if err != nil || msg.Text != "hello" || msg.Date != "2026-03-01T11:00:00Z" {
		t.Errorf("plain: %+v, %v", msg, err)
	}
	msg, err = mp.Read(ctx, "html")
	if err != nil || msg.Text != "Hi & bye\nLine\ntwo" {
		t.Errorf("html: %q, %v", msg.Text, err)
	}
}

func TestMailpitReadErrors(t *testing.T) {
	mp := newTestMailpit(t, fakeMailpitAPI(t).URL, "")
	for id, want := range map[string]string{
		"missing":               "not found",
		"broken":                "500",
		"huge":                  "too large",
		"../etc":                "invalid message id",
		"a?b=c":                 "invalid message id",
		"":                      "invalid message id",
		strings.Repeat("a", 65): "invalid message id",
	} {
		_, err := mp.Read(context.Background(), id)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Read(%q) error = %v, want %q", id, err, want)
		}
	}
}

func TestMailpitHonorsContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	mp := newTestMailpit(t, srv.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := mp.Search(ctx, "x", 1); err == nil {
		t.Error("expected an error from a stalled server")
	}
}

// fakeSMTP accepts one connection, speaks just enough SMTP, and reports the
// commands and message data it saw.
type smtpCapture struct {
	commands []string
	data     string
}

func fakeSMTP(t *testing.T) (addr string, result <-chan smtpCapture) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan smtpCapture, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var got smtpCapture
		r := bufio.NewReader(conn)
		reply := func(s string) { io.WriteString(conn, s+"\r\n") }
		reply("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			line = strings.TrimRight(line, "\r\n")
			got.commands = append(got.commands, line)
			switch cmd := strings.ToUpper(line); {
			case strings.HasPrefix(cmd, "EHLO"):
				reply("250 fake")
			case cmd == "DATA":
				reply("354 go ahead")
				var sb strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					sb.WriteString(l)
				}
				got.data = sb.String()
				reply("250 queued")
			case cmd == "QUIT":
				reply("221 bye")
				ch <- got
				return
			default:
				reply("250 ok")
			}
		}
		ch <- got
	}()
	return ln.Addr().String(), ch
}

func TestMailpitSend(t *testing.T) {
	addr, result := fakeSMTP(t)
	mp := newTestMailpit(t, "http://127.0.0.1:1", addr)
	mp.now = func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) }

	msg, err := newOutgoing([]string{"Bob <bob@example.com>", "cy@example.com"}, "Café", "Hello\n.\nDot above")
	if err != nil {
		t.Fatal(err)
	}
	if err := mp.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := <-result

	wantCmds := []string{"MAIL FROM:<assistant@cleanroom.test>", "RCPT TO:<bob@example.com>", "RCPT TO:<cy@example.com>", "DATA"}
	cmds := strings.Join(got.commands, "|")
	for _, c := range wantCmds {
		if !strings.Contains(cmds, c) {
			t.Errorf("missing command %q in %q", c, cmds)
		}
	}
	parsed, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"From":         "<assistant@cleanroom.test>",
		"To":           "<bob@example.com>, <cy@example.com>",
		"Subject":      "=?utf-8?q?Caf=C3=A9?=",
		"Mime-Version": "1.0",
		"Content-Type": "text/plain; charset=utf-8",
	} {
		if g := parsed.Header.Get(k); g != want {
			t.Errorf("header %s = %q, want %q", k, g, want)
		}
	}
	if d, err := parsed.Header.Date(); err != nil || !d.Equal(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Date = %v, %v", d, err)
	}
	if id := parsed.Header.Get("Message-ID"); !strings.HasSuffix(id, "@cleanroom.test>") {
		t.Errorf("Message-ID = %q", id)
	}
	// net/smtp dot-stuffs the lone "." line on the wire; the server side of
	// this fake does not unstuff it, so the line shows up as "..".
	if !strings.Contains(got.data, "\r\n..\r\n") {
		t.Errorf("body line was not dot-stuffed: %q", got.data)
	}
}

func TestMailpitSendConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	mp := newTestMailpit(t, "http://127.0.0.1:1", addr)
	msg, _ := newOutgoing([]string{"a@example.com"}, "s", "b")
	if err := mp.Send(context.Background(), msg); err == nil {
		t.Error("expected an error")
	}
}
