package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func newTestMailpit(t *testing.T, apiURL string) *mailpit {
	t.Helper()
	u, err := url.Parse(apiURL)
	if err != nil {
		t.Fatal(err)
	}
	return newMailpit(u)
}

func TestMailpitSearch(t *testing.T) {
	mp := newTestMailpit(t, fakeMailpitAPI(t).URL)
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
	mp := newTestMailpit(t, fakeMailpitAPI(t).URL)
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
	mp := newTestMailpit(t, fakeMailpitAPI(t).URL)
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
	mp := newTestMailpit(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := mp.Search(ctx, "x", 1); err == nil {
		t.Error("expected an error from a stalled server")
	}
}
