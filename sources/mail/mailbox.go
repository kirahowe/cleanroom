package main

import "context"

// Mailbox is the backend-neutral view of a mailbox. It can only be read:
// there is no method that sends, so no tool built on it can. Implementations
// must treat every argument as untrusted text, and must not read credentials
// or addresses from anywhere but their own configuration.
type Mailbox interface {
	// Search returns at most limit messages matching query, newest first.
	// The query syntax is defined by the backend.
	Search(ctx context.Context, query string, limit int) ([]Summary, error)
	// Read returns one message with its plain text body.
	Read(ctx context.Context, id string) (Message, error)
	// QuerySyntax tells the caller of the search tool, in a sentence or
	// two, what a query may contain. It is appended to the tool's
	// description.
	QuerySyntax() string
}

type Summary struct {
	ID      string   `json:"id" jsonschema:"message ID to pass to the read tool"`
	From    string   `json:"from" jsonschema:"sender address"`
	To      []string `json:"to" jsonschema:"recipient addresses"`
	Subject string   `json:"subject" jsonschema:"subject line"`
	Date    string   `json:"date" jsonschema:"received time, RFC 3339"`
	Snippet string   `json:"snippet" jsonschema:"short excerpt of the body"`
}

type Message struct {
	ID      string   `json:"id" jsonschema:"message ID"`
	From    string   `json:"from" jsonschema:"sender address"`
	To      []string `json:"to" jsonschema:"recipient addresses"`
	Subject string   `json:"subject" jsonschema:"subject line"`
	Date    string   `json:"date" jsonschema:"sent time, RFC 3339"`
	Text    string   `json:"text" jsonschema:"plain text body; tag-stripped HTML if the message has no plain text part"`
}
