# mail-source

A stdio MCP server with two mailbox tools: `search` and `read`. It has no send
tool or SMTP code. It reads from an IMAP account or from Mailpit's HTTP API;
other back ends can implement `Mailbox` in `mailbox.go`.

This is a separate Go module. Its direct dependencies outside the standard
library are the official MCP Go SDK, and `go-imap` v2 (a beta) and
`go-message` for IMAP and MIME.

## Tools

Both tools return structured content and a JSON text block. Both advertise
read-only, closed-world MCP annotations.

- `search` takes `query` and an optional `limit` (1–50, default 20). Results
  have the form `{messages: [{id, from, to, subject, date, snippet}]}`, newest
  first. The query syntax depends on the back end, and the tool's description
  states it.
- `read` takes an `id` from a search result and returns
  `{id, from, to, subject, date, text}`. It uses the plain text body when
  available, otherwise a basic HTML-to-text conversion.

## Configuration

Configure the server with command-line flags, supplied by the gateway's policy
when used with cleanroom. The environment is not read.

| Flag | Default | |
| --- | --- | --- |
| `--mailpit-url` | `http://127.0.0.1:8025` | Mailpit back end, used when `--imap-addr` is absent |
| `--imap-addr` | | `host:port`; selects the IMAP back end |
| `--imap-tls` | `implicit` | `implicit` (TLS from the first byte) or `starttls` |
| `--imap-user` | | required with `--imap-addr` |
| `--imap-password-file` | | required with `--imap-addr` |
| `--imap-ca-file` | | PEM certificate to trust instead of the system's |
| `--imap-mailbox` | all mail | the one mailbox to read |
| `--check` | | log in, print the message count and exit |

## IMAP back end

Without `--imap-mailbox`, the server reads the mailbox that holds all mail:
the one the IMAP server marks `\All`, or else one named "All Mail". If there
is neither, it reads INBOX.

The server logs in, opens one mailbox with `EXAMINE` and keeps that connection.
`EXAMINE` is read-only, and messages are fetched with `BODY.PEEK`, so reading
a message leaves it unread. The server uses no IMAP command that changes a
mailbox.

The connection always uses TLS and verifies the server's certificate. There
is no plain-text mode: with `starttls`, a server that does not offer STARTTLS
is refused before the password is sent. For a self-signed certificate that the
system does not trust, pass it with `--imap-ca-file`.

The password comes from a file that only its owner can read (`chmod 600`).
The server refuses a file that the group or others can read. This keeps the
password out of the policy file and the process list. It does not hide it
from other programs running as the same user.

Search queries are translated to IMAP `SEARCH`:

- Every term must match. IMAP matches substrings, ignoring case. There is no
  ranking or stemming.
- `from:`, `to:` and `subject:` match those headers. `since:YYYY-MM-DD` and
  `before:YYYY-MM-DD` bound the arrival date.
- Double quotes keep a phrase together: `subject:"family reunion"`.
- Any other `word:` prefix is searched for as text.

Results are the matches with the highest UIDs, which are the most recently
arrived. Snippets are empty. An id has the form `<UIDVALIDITY>-<UID>`; an id
issued before the server renumbered the mailbox is refused.

`read` fetches at most 4 MiB of a message and returns at most 256 KiB of
text. Attachments are skipped. A message that is malformed part of the way
through yields the text before the damage.

Test the connection without starting the MCP server. This prints a message
count and no mail content:

```sh
./mail-source --check --imap-addr 127.0.0.1:1143 --imap-tls starttls \
  --imap-user you@example.com --imap-password-file ~/.config/cleanroom/imap-password
```

A cleanroom policy entry for the same account:

```jsonc
"mail": {
  "command": ["/path/to/bin/mail-source",
    "--imap-addr", "127.0.0.1:1143", "--imap-tls", "starttls",
    "--imap-user", "you@example.com",
    "--imap-password-file", "/path/to/imap-password"],
  "labels": ["contaminated", "private"],
  "ask_only": true,
  "reader": ["..."],
  "tools": {
    "search": {"class": "read"},
    "read": {"class": "read"},
  },
},
```

The address and `starttls` above are Proton Mail Bridge's defaults. Bridge
installs its certificate in the macOS keychain, so it verifies without
`--imap-ca-file`.

Limits:

- One mailbox per server. A server with no all-mail mailbox gets INBOX, and
  mail filed elsewhere is not found. `--check` says which mailbox was opened.
- Search quality is the IMAP server's. A reader has to try several queries.
- The connection is used for one call at a time.
- Only password login is supported. Accounts that require OAuth cannot be
  used.

## Mailpit back end

Mailpit filters such as `from:`, `subject:` and `is:unread` are passed
through. Reading a message through Mailpit may mark it as read.

The back end uses two endpoints:

- `GET /api/v1/search?query=...&limit=...` for message summaries.
- `GET /api/v1/message/{ID}` for the message body.

See Mailpit's [API documentation](https://mailpit.axllent.org/docs/api-v1/)
and [search filters](https://mailpit.axllent.org/docs/usage/search-filters/).

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

Tests use a fake Mailpit HTTP API, go-imap's in-memory IMAP server over TLS on
a local port, and a fake `Mailbox` over in-memory MCP transports. They do not
require a running Mailpit or IMAP server.
