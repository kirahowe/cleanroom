package main

import "context"

// Mailbox is the backend-neutral view of a mailbox. Implementations must
// treat every argument as untrusted text, and must not read credentials or
// addresses from anywhere but their own configuration.
type Mailbox interface {
	// Search returns at most limit messages matching query, newest first.
	// The query syntax is defined by the backend.
	Search(ctx context.Context, query string, limit int) ([]Summary, error)
	// Read returns one message with its plain text body.
	Read(ctx context.Context, id string) (Message, error)
	// Send delivers a message that has already been validated by newOutgoing.
	Send(ctx context.Context, msg Outgoing) error
}

// Summary is one search hit.
type Summary struct {
	ID      string   `json:"id" jsonschema:"message ID to pass to the read tool"`
	From    string   `json:"from" jsonschema:"sender address"`
	To      []string `json:"to" jsonschema:"recipient addresses"`
	Subject string   `json:"subject" jsonschema:"subject line"`
	Date    string   `json:"date" jsonschema:"received time, RFC 3339"`
	Snippet string   `json:"snippet" jsonschema:"short excerpt of the body"`
}

// Message is one full message.
type Message struct {
	ID      string   `json:"id" jsonschema:"message ID"`
	From    string   `json:"from" jsonschema:"sender address"`
	To      []string `json:"to" jsonschema:"recipient addresses"`
	Subject string   `json:"subject" jsonschema:"subject line"`
	Date    string   `json:"date" jsonschema:"sent time, RFC 3339"`
	Text    string   `json:"text" jsonschema:"plain text body; tag-stripped HTML if the message has no plain text part"`
}
