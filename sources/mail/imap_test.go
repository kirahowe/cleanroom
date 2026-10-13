package main

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	testUser     = "kira"
	testPassword = "bridge-password"
)

var testMessages = []string{
	"From: Ann <ann@example.com>\nTo: kira@example.com\nSubject: Lunch\nDate: Sun, 01 Mar 2026 12:00:00 +0000\n\nNoon on Tuesday?\n",
	"From: Reunion Committee <family@example.com>\nTo: kira@example.com\nSubject: Family reunion\nDate: Mon, 02 Mar 2026 12:00:00 +0000\n\nThe reunion is on July 17.\n",
	"From: Ann <ann@example.com>\nTo: kira@example.com\nSubject: Re: Family reunion\nDate: Tue, 03 Mar 2026 12:00:00 +0000\n\nSee you at the reunion.\n",
}

// testIMAP is an in-memory IMAP server that speaks TLS on a local port.
type testIMAP struct {
	addr   string
	caFile string
	user   *imapmemserver.User
}

// startIMAP serves testMessages from INBOX. With startTLS the server
// listens in the clear and offers STARTTLS; otherwise it speaks TLS from
// the first byte.
func startIMAP(t *testing.T, startTLS bool) testIMAP {
	t.Helper()
	// httptest carries a certificate for 127.0.0.1. Borrowing it saves
	// generating one here.
	web := httptest.NewUnstartedServer(nil)
	web.StartTLS()
	t.Cleanup(web.Close)
	tlsConfig := &tls.Config{Certificates: web.TLS.Certificates}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: web.Certificate().Raw})
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	user := imapmemserver.NewUser(testUser, testPassword)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	for _, raw := range testMessages {
		if _, err := user.Append("INBOX", strings.NewReader(crlf(raw)), &imap.AppendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(user)

	options := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Logger: log.New(io.Discard, "", 0),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if startTLS {
		options.TLSConfig = tlsConfig
	} else {
		ln = tls.NewListener(ln, tlsConfig)
	}
	server := imapserver.New(options)
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	return testIMAP{addr: addr, caFile: caFile, user: user}
}

func (s testIMAP) options(t *testing.T) imapOptions {
	t.Helper()
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte(testPassword+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return imapOptions{
		addr: s.addr, host: "127.0.0.1", user: testUser,
		passwordFile: passwordFile, caFile: s.caFile, mailbox: "INBOX",
	}
}

func (s testIMAP) box(t *testing.T, change func(*imapOptions)) *imapBox {
	t.Helper()
	opts := s.options(t)
	if change != nil {
		change(&opts)
	}
	box, err := newIMAP(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		box.mu.Lock()
		defer box.mu.Unlock()
		box.drop()
	})
	return box
}

func subjects(found []Summary) []string {
	out := []string{}
	for _, s := range found {
		out = append(out, s.Subject)
	}
	return out
}

func TestIMAPSearch(t *testing.T) {
	box := startIMAP(t, false).box(t, nil)
	ctx := context.Background()

	for query, want := range map[string][]string{
		"reunion":                    {"Re: Family reunion", "Family reunion"},
		"REUNION july":               {"Family reunion"},
		"from:ann":                   {"Re: Family reunion", "Lunch"},
		`subject:"family reunion"`:   {"Re: Family reunion", "Family reunion"},
		"from:ann subject:lunch":     {"Lunch"},
		"since:2020-01-01 tuesday":   {"Lunch"},
		"before:2020-01-01 tuesday":  {},
		"nothing-mentions-this-word": {},
	} {
		found, err := box.Search(ctx, query, 10)
		if err != nil {
			t.Errorf("Search(%q): %v", query, err)
			continue
		}
		if got := subjects(found); !slices.Equal(got, want) {
			t.Errorf("Search(%q) = %q, want %q", query, got, want)
		}
	}

	found, err := box.Search(ctx, "example.com", 2)
	if err != nil || !slices.Equal(subjects(found), []string{"Re: Family reunion", "Family reunion"}) {
		t.Errorf("limit 2 should keep the two newest: %q, %v", subjects(found), err)
	}
	first := found[0]
	if first.From != "Ann <ann@example.com>" || !slices.Equal(first.To, []string{"kira@example.com"}) ||
		first.Snippet != "" || !imapIDPattern.MatchString(first.ID) {
		t.Errorf("summary: %+v", first)
	}
	if _, err := time.Parse(time.RFC3339, first.Date); err != nil {
		t.Errorf("date %q: %v", first.Date, err)
	}

	if _, err := box.Search(ctx, "since:whenever", 10); err == nil {
		t.Error("a query that does not parse should fail")
	}
}

func TestIMAPRead(t *testing.T) {
	server := startIMAP(t, false)
	box := server.box(t, nil)
	ctx := context.Background()

	found, err := box.Search(ctx, "july", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("search: %+v, %v", found, err)
	}
	msg, err := box.Read(ctx, found[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	want := Message{
		ID: found[0].ID, From: "Reunion Committee <family@example.com>", To: []string{"kira@example.com"},
		Subject: "Family reunion", Date: "2026-03-02T12:00:00Z", Text: "The reunion is on July 17.",
	}
	if msg.ID != want.ID || msg.From != want.From || !slices.Equal(msg.To, want.To) ||
		msg.Subject != want.Subject || msg.Date != want.Date || msg.Text != want.Text {
		t.Errorf("got  %+v\nwant %+v", msg, want)
	}

	validity, _, _ := parseIMAPID(found[0].ID)
	for id, wantErr := range map[string]string{
		imapID(validity, 9999):   "message not found",
		imapID(validity+1, 1):    "the mailbox was rebuilt",
		"abc":                    "invalid message id",
		"1-0":                    "invalid message id",
		"1-99999999999":          "invalid message id",
		"1-1 UID STORE 1 +FLAGS": "invalid message id",
		"":                       "invalid message id",
	} {
		if _, err := box.Read(ctx, id); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Read(%q): %v, want %q", id, err, wantErr)
		}
	}
}

// Reading must not change the mailbox. In particular it must leave the
// message unread, which is what a careless FETCH would undo.
func TestIMAPReadLeavesTheMessageUnread(t *testing.T) {
	server := startIMAP(t, false)
	box := server.box(t, nil)
	ctx := context.Background()

	found, err := box.Search(ctx, "july", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("search: %+v, %v", found, err)
	}
	if _, err := box.Read(ctx, found[0].ID); err != nil {
		t.Fatal(err)
	}

	observer, err := imapclient.DialTLS(server.addr, &imapclient.Options{TLSConfig: box.tls})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if err := observer.Login(testUser, testPassword).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	seen, err := observer.UIDSearch(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagSeen}}, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if uids := seen.AllUIDs(); len(uids) != 0 {
		t.Errorf("messages %v were marked read", uids)
	}
}

func TestIMAPStartTLS(t *testing.T) {
	server := startIMAP(t, true)
	box := server.box(t, func(o *imapOptions) { o.startTLS = true })
	count, err := box.Count(context.Background())
	if err != nil || count != uint32(len(testMessages)) {
		t.Errorf("count = %d, %v", count, err)
	}
}

// A call that finds the connection gone reconnects once and still answers.
func TestIMAPReconnects(t *testing.T) {
	box := startIMAP(t, false).box(t, nil)
	ctx := context.Background()
	if _, err := box.Search(ctx, "lunch", 10); err != nil {
		t.Fatal(err)
	}
	box.client.Close()
	found, err := box.Search(ctx, "lunch", 10)
	if err != nil || len(found) != 1 {
		t.Errorf("after the connection dropped: %+v, %v", found, err)
	}
}

func TestIMAPConnectErrors(t *testing.T) {
	ctx := context.Background()
	server := startIMAP(t, false)

	for name, tc := range map[string]struct {
		change func(*imapOptions)
		want   string
	}{
		"wrong user":       {func(o *imapOptions) { o.user = "someone-else" }, "logging in"},
		"no such mailbox":  {func(o *imapOptions) { o.mailbox = "Nowhere" }, `opening mailbox "Nowhere"`},
		"untrusted server": {func(o *imapOptions) { o.caFile = "" }, "certificate"},
		"wrong host name":  {func(o *imapOptions) { o.host = "mail.invalid" }, "certificate"},
		// The server here speaks TLS at once, so asking it in the clear
		// for STARTTLS gets nowhere. Nothing falls back to plain text.
		"starttls against a TLS port": {func(o *imapOptions) { o.startTLS = true }, "TLS"},
	} {
		if _, err := server.box(t, tc.change).Count(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error mentioning %q", name, err, tc.want)
		}
	}
}

// A server without STARTTLS is refused before the password is sent.
func TestIMAPNeverLogsInWithoutTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	loggedIn := make(chan struct{}, 1)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return loginSpy{loggedIn: loggedIn}, nil, nil
		},
		InsecureAuth: true,
		Logger:       log.New(io.Discard, "", 0),
	})
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })

	real := startIMAP(t, false)
	for _, startTLS := range []bool{true, false} {
		box := real.box(t, func(o *imapOptions) { o.addr, o.startTLS = ln.Addr().String(), startTLS })
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := box.Count(ctx)
		cancel()
		if err == nil {
			t.Errorf("startTLS=%v: connected to a server without TLS", startTLS)
		}
	}
	select {
	case <-loggedIn:
		t.Error("the password was sent in the clear")
	default:
	}
}

// loginSpy is a session that records a login attempt and refuses it.
type loginSpy struct {
	imapserver.Session
	loggedIn chan<- struct{}
}

func (s loginSpy) Login(string, string) error {
	s.loggedIn <- struct{}{}
	return imapserver.ErrAuthFailed
}

func (s loginSpy) Close() error { return nil }

func TestIMAPHonorsContext(t *testing.T) {
	// A server that accepts the connection and then says nothing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()

	box := startIMAP(t, false).box(t, func(o *imapOptions) { o.addr = ln.Addr().String() })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := box.Search(ctx, "x", 1); err == nil {
		t.Error("expected an error from a silent server")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v to give up", elapsed)
	}
}

func TestReadSecret(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		// WriteFile's mode is cut down by the umask, so set it again.
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got, err := readSecret(write("good", "s3cret with spaces \r\n", 0o600)); err != nil || got != "s3cret with spaces " {
		t.Errorf("got %q, %v", got, err)
	}
	for name, path := range map[string]string{
		"group readable": write("group", "s3cret\n", 0o640),
		"world readable": write("world", "s3cret\n", 0o604),
		"empty":          write("empty", "\n", 0o600),
		"a directory":    dir,
		"missing":        filepath.Join(dir, "missing"),
	} {
		if got, err := readSecret(path); err == nil {
			t.Errorf("%s: got %q, want an error", name, got)
		}
	}
}

func TestNewIMAPRejectsABadCAFile(t *testing.T) {
	opts := startIMAP(t, false).options(t)
	opts.caFile = opts.passwordFile
	if _, err := newIMAP(opts); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Errorf("got %v", err)
	}
}
