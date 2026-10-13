# mail-source

A stdio MCP server with two mailbox tools: `search` and `read`. It has no send
tool or SMTP code. The current back end uses Mailpit's HTTP API; other back
ends can implement `Mailbox` in `mailbox.go`.

This is a separate Go module. Its only direct dependency outside the standard
library is the official MCP Go SDK.

## Tools

Both tools return structured content and a JSON text block. Both advertise
read-only, closed-world MCP annotations.

- `search` takes `query` and an optional `limit` (1–50, default 20). Mailpit
  filters such as `from:`, `subject:` and `is:unread` are passed through.
  Results have the form `{messages: [{id, from, to, subject, date, snippet}]}`.
- `read` takes an `id` from a search result and returns
  `{id, from, to, subject, date, text}`. It uses the plain text body when
  available, otherwise a basic HTML-to-text conversion. Reading a message
  through Mailpit may mark it as read.

## Configuration

Configure the server with command-line flags, supplied by the gateway's policy
when used with cleanroom.

| Flag | Default |
| --- | --- |
| `--mailpit-url` | `http://127.0.0.1:8025` |

## Run

From this module's directory:

```sh
mailpit &                      # web UI and API on :8025
go build -o mail-source .
./mail-source
```

Register the binary as a stdio server in your MCP client. Logs use stderr;
stdout carries MCP messages.

## Test

```sh
go test -race ./...
```

Tests use a fake Mailpit HTTP API and a fake `Mailbox` over in-memory MCP
transports. They do not require a running Mailpit server.

## Mailpit API

The back end uses two endpoints:

- `GET /api/v1/search?query=...&limit=...` for message summaries.
- `GET /api/v1/message/{ID}` for the message body.

See Mailpit's [API documentation](https://mailpit.axllent.org/docs/api-v1/)
and [search filters](https://mailpit.axllent.org/docs/usage/search-filters/).
