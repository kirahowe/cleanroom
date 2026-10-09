package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T) (*mcp.ClientSession, *Store) {
	t.Helper()
	store := newTestStore(t)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewServer(store).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, store
}

// call invokes a tool and decodes its structured content into out.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		data, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, data, err)
		}
	}
	return res
}

func TestToolAnnotations(t *testing.T) {
	cs, _ := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type hints struct{ readOnly, destructive bool }
	want := map[string]hints{
		"create_event":    {false, false},
		"create_reminder": {false, false},
		"list_events":     {true, false},
		"list_reminders":  {true, false},
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		w, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		a := tool.Annotations
		if a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: openWorldHint should be false", tool.Name)
			continue
		}
		if a.ReadOnlyHint != w.readOnly {
			t.Errorf("%s: readOnlyHint = %v", tool.Name, a.ReadOnlyHint)
		}
		if !w.readOnly && (a.DestructiveHint == nil || *a.DestructiveHint || a.IdempotentHint) {
			t.Errorf("%s: want destructive=false idempotent=false", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s: no description", tool.Name)
		}
	}
}

func TestCreateAndListEvents(t *testing.T) {
	cs, store := connect(t)

	var created createEventOut
	res := call(t, cs, "create_event", map[string]any{
		"title": "Offsite", "date": "2026-03-10", "end_date": "2026-03-12", "location": "Lisbon",
	}, &created)
	if res.IsError {
		t.Fatalf("create_event failed: %+v", res.Content)
	}
	if created.UID != "id1@cleanroom" || created.File != store.ICSPath() {
		t.Errorf("created = %+v", created)
	}
	if _, ok := res.Content[0].(*mcp.TextContent); !ok {
		t.Errorf("first content block is %T, want text", res.Content[0])
	}

	var listed listEventsOut
	call(t, cs, "list_events", map[string]any{}, &listed)
	want := Event{UID: "id1@cleanroom", Title: "Offsite", Date: "2026-03-10", EndDate: "2026-03-12", Location: "Lisbon"}
	if len(listed.Events) != 1 || listed.Events[0] != want {
		t.Errorf("events = %+v", listed.Events)
	}
}

func TestCreateAndListReminders(t *testing.T) {
	cs, store := connect(t)

	var created createReminderOut
	call(t, cs, "create_reminder", map[string]any{"title": "Call", "due": "2026-04-01", "note": "ask"}, &created)
	if created.ID != "id1" || created.File != store.RemindersPath() {
		t.Errorf("created = %+v", created)
	}

	var listed listRemindersOut
	call(t, cs, "list_reminders", map[string]any{}, &listed)
	want := Reminder{ID: "id1", Title: "Call", Due: "2026-04-01", Note: "ask"}
	if len(listed.Reminders) != 1 || listed.Reminders[0] != want {
		t.Errorf("reminders = %+v", listed.Reminders)
	}
}

func TestEmptyListsOverMCP(t *testing.T) {
	cs, _ := connect(t)
	for _, name := range []string{"list_events", "list_reminders"} {
		if res := call(t, cs, name, map[string]any{}, nil); res.IsError {
			t.Errorf("%s on empty store failed: %+v", name, res.Content)
		}
	}
}

func TestInvalidInput(t *testing.T) {
	cs, _ := connect(t)
	cases := []struct {
		tool string
		args map[string]any
		msg  string
	}{
		{"create_event", map[string]any{"title": "x", "date": "2026-02-30"}, "date"},
		{"create_event", map[string]any{"title": "", "date": "2026-03-10"}, "title"},
		{"create_event", map[string]any{"title": "x", "date": "2026-03-10", "end_date": "2026-03-01"}, "before"},
		{"create_reminder", map[string]any{"title": "x", "due": "tomorrow"}, "due"},
		{"create_reminder", map[string]any{"title": strings.Repeat("x", 201), "due": "2026-03-10"}, "title"},
	}
	for _, c := range cases {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil {
			t.Errorf("%s %v: protocol error %v", c.tool, c.args, err)
			continue
		}
		if !res.IsError {
			t.Errorf("%s %v: expected a tool error", c.tool, c.args)
			continue
		}
		if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, c.msg) {
			t.Errorf("%s: error %q does not mention %q", c.tool, text, c.msg)
		}
	}
}

func TestMissingRequiredArgument(t *testing.T) {
	cs, _ := connect(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_event", Arguments: map[string]any{"title": "no date"},
	})
	if err == nil && !res.IsError {
		t.Error("expected an error for a missing date")
	}
}
