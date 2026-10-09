package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type createEventOut struct {
	UID  string `json:"uid" jsonschema:"Id of the new event."`
	File string `json:"file" jsonschema:"Path of the calendar.ics file that now contains it."`
}

type createReminderOut struct {
	ID   string `json:"id" jsonschema:"Id of the new reminder."`
	File string `json:"file" jsonschema:"Path of the reminders JSON file that now contains it."`
}

type listEventsOut struct {
	Events []Event `json:"events" jsonschema:"All events in creation order."`
}

type listRemindersOut struct {
	Reminders []Reminder `json:"reminders" jsonschema:"All reminders in creation order."`
}

type noInput struct{}

// NewServer returns an MCP server exposing the store as four tools.
func NewServer(s *Store) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "calendar-source", Version: "0.1.0"}, nil)

	writes := &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	reads := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_event",
		Description: "Add an all-day event to the local calendar.ics file.",
		Annotations: writes,
	}, func(_ context.Context, _ *mcp.CallToolRequest, in EventInput) (*mcp.CallToolResult, createEventOut, error) {
		ev, err := s.CreateEvent(in)
		return nil, createEventOut{UID: ev.UID, File: s.ICSPath()}, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_reminder",
		Description: "Add a reminder with a due date to the local reminders JSON file.",
		Annotations: writes,
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ReminderInput) (*mcp.CallToolResult, createReminderOut, error) {
		r, err := s.CreateReminder(in)
		return nil, createReminderOut{ID: r.ID, File: s.RemindersPath()}, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_events",
		Description: "List all events in the local calendar.",
		Annotations: reads,
	}, func(context.Context, *mcp.CallToolRequest, noInput) (*mcp.CallToolResult, listEventsOut, error) {
		events, err := s.ListEvents()
		return nil, listEventsOut{Events: events}, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_reminders",
		Description: "List all reminders in the local reminders file.",
		Annotations: reads,
	}, func(context.Context, *mcp.CallToolRequest, noInput) (*mcp.CallToolResult, listRemindersOut, error) {
		reminders, err := s.ListReminders()
		return nil, listRemindersOut{Reminders: reminders}, err
	})

	return srv
}

func ptr[T any](v T) *T { return &v }
