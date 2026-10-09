package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeMailbox records calls and returns canned data.
type fakeMailbox struct {
	searchQuery string
	searchLimit int
	sent        []Outgoing
	err         error
}

func (f *fakeMailbox) Search(_ context.Context, q string, limit int) ([]Summary, error) {
	f.searchQuery, f.searchLimit = q, limit
	return []Summary{{ID: "m1", From: "ann@example.com", To: []string{"bob@example.com"},
		Subject: "Hi", Date: "2026-03-01T12:00:00Z", Snippet: "yo"}}, f.err
}

func (f *fakeMailbox) Read(_ context.Context, id string) (Message, error) {
	if id == "gone" {
		return Message{}, errors.New("message not found")
	}
	return Message{ID: id, From: "ann@example.com", Subject: "Hi", Text: "body"}, nil
}

func (f *fakeMailbox) Send(_ context.Context, m Outgoing) error {
	f.sent = append(f.sent, m)
	return f.err
}

func connect(t *testing.T, mb Mailbox, allowed []string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := newServer(mb, allowed).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func errorText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestToolList(t *testing.T) {
	cs := connect(t, &fakeMailbox{}, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type hints struct{ readOnly, destructive, openWorld *bool }
	flag := func(b bool) *bool { return &b }
	want := map[string]hints{
		"search": {readOnly: flag(true), openWorld: flag(false)},
		"read":   {readOnly: flag(true), openWorld: flag(false)},
		"send":   {readOnly: flag(false), destructive: flag(false), openWorld: flag(true)},
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("got %d tools", len(res.Tools))
	}
	for _, tool := range res.Tools {
		w, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		a := tool.Annotations
		if tool.Description == "" {
			t.Errorf("%s: no description", tool.Name)
		}
		if a.ReadOnlyHint != *w.readOnly {
			t.Errorf("%s: readOnlyHint = %v", tool.Name, a.ReadOnlyHint)
		}
		if a.OpenWorldHint == nil || *a.OpenWorldHint != *w.openWorld {
			t.Errorf("%s: openWorldHint = %v", tool.Name, a.OpenWorldHint)
		}
		if w.destructive != nil && (a.DestructiveHint == nil || *a.DestructiveHint != *w.destructive) {
			t.Errorf("%s: destructiveHint = %v", tool.Name, a.DestructiveHint)
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if strings.Count(string(schema), `"description"`) < 1 {
			t.Errorf("%s: arguments lack descriptions: %s", tool.Name, schema)
		}
	}
}

func TestSearch(t *testing.T) {
	mb := &fakeMailbox{}
	cs := connect(t, mb, nil)

	res := call(t, cs, "search", map[string]any{"query": " from:ann "})
	if res.IsError {
		t.Fatal(errorText(res))
	}
	if mb.searchQuery != "from:ann" || mb.searchLimit != defaultLimit {
		t.Errorf("backend got %q, %d", mb.searchQuery, mb.searchLimit)
	}
	var out searchOutput
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Messages) != 1 || out.Messages[0].ID != "m1" {
		t.Errorf("structured content: %s, %v", raw, err)
	}
	if !strings.Contains(errorText(res), `"id":"m1"`) {
		t.Errorf("text content lacks result: %s", errorText(res))
	}

	for name, args := range map[string]map[string]any{
		"empty query":   {"query": "  "},
		"limit too big": {"query": "x", "limit": 51},
		"negative":      {"query": "x", "limit": -1},
		"long query":    {"query": strings.Repeat("x", maxQueryLen+1)},
	} {
		if res := call(t, cs, "search", args); !res.IsError {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRead(t *testing.T) {
	cs := connect(t, &fakeMailbox{}, nil)
	res := call(t, cs, "read", map[string]any{"id": "m1"})
	if res.IsError || !strings.Contains(errorText(res), `"text":"body"`) {
		t.Errorf("read: %v %s", res.IsError, errorText(res))
	}
	if res := call(t, cs, "read", map[string]any{"id": "gone"}); !res.IsError || !strings.Contains(errorText(res), "not found") {
		t.Errorf("missing message: %v %s", res.IsError, errorText(res))
	}
	if res := call(t, cs, "read", map[string]any{"id": ""}); !res.IsError {
		t.Error("empty id should fail")
	}
}

func TestSend(t *testing.T) {
	mb := &fakeMailbox{}
	cs := connect(t, mb, nil)
	res := call(t, cs, "send", map[string]any{"to": []string{"Bob <bob@example.com>"}, "subject": "Hi", "body": "text"})
	if res.IsError {
		t.Fatal(errorText(res))
	}
	if len(mb.sent) != 1 || mb.sent[0].To[0] != "bob@example.com" || mb.sent[0].Body != "text" {
		t.Errorf("sent = %+v", mb.sent)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if string(raw) != `{"recipients":["bob@example.com"],"status":"sent"}` {
		t.Errorf("structured content: %s", raw)
	}
}

func TestSendBackendError(t *testing.T) {
	cs := connect(t, &fakeMailbox{err: errors.New("smtp down")}, nil)
	res := call(t, cs, "send", map[string]any{"to": []string{"a@example.com"}, "subject": "s", "body": "b"})
	if !res.IsError || !strings.Contains(errorText(res), "smtp down") {
		t.Errorf("got %v %s", res.IsError, errorText(res))
	}
}

func TestSendAllowlist(t *testing.T) {
	mb := &fakeMailbox{}
	cs := connect(t, mb, []string{"ok@example.com"})

	res := call(t, cs, "send", map[string]any{"to": []string{"OK@Example.com"}, "subject": "s", "body": "b"})
	if res.IsError {
		t.Errorf("allowed recipient refused: %s", errorText(res))
	}
	mb.sent = nil

	res = call(t, cs, "send", map[string]any{"to": []string{"ok@example.com", "evil@example.com"}, "subject": "s", "body": "b"})
	if !res.IsError || !strings.Contains(errorText(res), "evil@example.com") {
		t.Errorf("got %v %s", res.IsError, errorText(res))
	}
	if len(mb.sent) != 0 {
		t.Error("mailbox was called despite a refused recipient")
	}
}

func TestSendRejectsHeaderInjection(t *testing.T) {
	mb := &fakeMailbox{}
	cs := connect(t, mb, nil)
	for name, args := range map[string]map[string]any{
		"subject": {"to": []string{"a@example.com"}, "subject": "hi\r\nBcc: evil@example.com", "body": "b"},
		"address": {"to": []string{"a@example.com\r\nBcc: evil@example.com"}, "subject": "s", "body": "b"},
	} {
		if res := call(t, cs, "send", args); !res.IsError {
			t.Errorf("%s injection was accepted", name)
		}
	}
	if len(mb.sent) != 0 {
		t.Error("mailbox was called")
	}
}
