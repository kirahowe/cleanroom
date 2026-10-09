# mail-source

`mail-source` is a stdio MCP server that exposes a mailbox through three tools. The only backend today is Mailpit: it reads through Mailpit's HTTP API and sends through SMTP. The `Mailbox` interface in `mailbox.go` does not mention Mailpit, so another backend can be added behind it.

The module depends only on the standard library and the official Go MCP SDK. It imports nothing else from the repository it lives in.

## Tools

The tools return their result as structured content and as a JSON text block.

`search` takes `query` (string) and an optional `limit` (1 to 50, default 20). The query is passed to Mailpit, so its filters such as `from:`, `subject:` and `is:unread` work. It returns `{messages: [{id, from, to, subject, date, snippet}]}`, newest first. Read-only, closed world.

`read` takes `id` (string, from a search result) and returns `{id, from, to, subject, date, text}`. `text` is the plain text part. If the message has only HTML, it is a minimal tag-stripped version. Read-only, closed world. Mailpit may mark the message as read (see below).

`send` takes `to` (list of addresses), `subject` and `body`, and returns `{status: "sent", recipients: [...]}`. The sender is the configured `--from` address. Not read-only, not destructive, open world.

`send` validates its input before doing any I/O. It rejects control characters in the subject, addresses that `net/mail` cannot parse or that contain anything but printable ASCII, more than 20 recipients, subjects over 200 bytes and bodies over 100 KB. If an allowlist is configured, any recipient outside it is refused. The message is built with Date, Message-ID, MIME-Version and a UTF-8 text/plain quoted-printable body, non-ASCII subjects are encoded as RFC 2047 words, and every line ends in CRLF. `net/smtp` handles dot-stuffing.

## Configuration

Flags take precedence over environment variables, which take precedence over defaults. Nothing comes from tool arguments.

| Flag | Environment | Default |
| --- | --- | --- |
| `--mailpit-url` | `MAILPIT_URL` | `http://127.0.0.1:8025` |
| `--smtp-addr` | `MAILPIT_SMTP` | `127.0.0.1:1025` |
| `--from` | `MAIL_FROM` | `assistant@cleanroom.test` |
| `--allow-recipient` (repeatable) | `MAIL_ALLOWED_RECIPIENTS` (comma separated) | none |

When no allowed recipient is configured, `send` accepts any valid address. SMTP is plain: no authentication, no TLS, so point it at a loopback address.

## Running against a local Mailpit

```sh
mailpit &                      # web UI and API on :8025, SMTP on :1025
go build -o mail-source .
./mail-source --allow-recipient me@example.com
```

Register the binary as a stdio server in your MCP client. Logs go to stderr, because stdout carries the protocol.

## Testing

```sh
go test -race ./...
go test -run xxx -fuzz FuzzOutgoing -fuzztime 10s
```

The tests use an `httptest` fake of the Mailpit API and a small fake SMTP listener. The MCP layer is tested end to end over in-memory transports with a fake `Mailbox`. No real Mailpit is needed.

## Verified facts

Checked on 2026-10-09 against the Mailpit swagger file (https://raw.githubusercontent.com/axllent/mailpit/master/server/ui/api/v1/swagger.json), the API overview (https://mailpit.axllent.org/docs/api-v1/) and the search syntax page (https://mailpit.axllent.org/docs/usage/search-filters/).

- Search is `GET /api/v1/search?query=...&limit=...`. Parameters are `query` (required), `start` (offset, default 0), `limit` (default 50) and `tz`. Results are sorted by received date, newest first. The response has `messages`, `total`, `unread` and other counters. Each message has `ID`, `From` and `To` (objects with `Name` and `Address`), `Subject`, `Created` (RFC 3339), `Snippet` and `Read`.
- Search syntax: unquoted words must all match, quoted phrases match exactly, `from:`, `to:`, `subject:`, `addressed:`, `is:read`, `is:unread`, `before:`, `after:` and others, `!` or `-` to exclude. Matching is case insensitive.
- A single message is `GET /api/v1/message/{ID}`. The fields used are `ID`, `From`, `To`, `Subject`, `Date`, `Text` and `HTML`. The ID can also be `latest`, which this server does not pass through.
- Reading by ID marks the message as read. The swagger description says so explicitly. The `/raw` and `/headers` endpoints do not say they do, so reading without the side effect would mean fetching `/raw` and parsing MIME ourselves. This server does not do that, to stay small, and the `read` tool description says the message may be marked read. `PUT /api/v1/messages` can set the read status back, but the prior state is unknown without an extra search.
- The default ports are 8025 for HTTP (`--listen`, `MP_UI_BIND_ADDR`) and 1025 for SMTP (`--smtp`, `MP_SMTP_BIND_ADDR`), per the runtime options page (https://mailpit.axllent.org/docs/configuration/runtime-options/). The API needs no authentication unless Mailpit is started with `--ui-auth-file`.
