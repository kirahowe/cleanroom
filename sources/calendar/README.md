# calendar-source

A stdio MCP server that stores calendar events in a local `.ics` file and
reminders in JSON. This is a separate Go module. Its only direct dependency
outside the standard library is the official MCP Go SDK.

## Tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `create_event` | Required: `title`, `date`. Optional: `end_date` (inclusive), `location`, `description`. | `uid`, `file` |
| `create_reminder` | Required: `title`, `due`. Optional: `note`. | `id`, `file` |
| `list_events` | None | All events in creation order |
| `list_reminders` | None | All reminders in creation order |

Events are all-day events. Dates use `YYYY-MM-DD`, with years from 1900 to
9000. `end_date` defaults to `date` and cannot precede it. Titles are required.
Titles and locations allow up to 200 characters; descriptions and notes allow
up to 2000. Invalid input returns a tool error.

Create tools advertise non-destructive writes; list tools advertise reads.
All four tools advertise closed-world MCP annotations.

## Configuration

`--dir` sets the storage directory, defaulting to `./calendar-data`. The server
creates the directory if needed.

| File | Contents |
| --- | --- |
| `events.json` | Stored events |
| `calendar.ics` | iCalendar export, regenerated when an event is added |
| `reminders.json` | Stored reminders |

Each operation holds a `.lock` file lock so multiple server processes can
share the directory. Files are written to temporary files and renamed into
place. New directories use mode `0700`; data files use `0600`. Existing
directory permissions are unchanged. Logs go to stderr.

## Run

From this module's directory:

```sh
go build -o calendar-source .
./calendar-source --dir ./calendar-data
```

Register the binary as a stdio server in your MCP client, with `--dir` and the
storage path as separate arguments.

## Test

```sh
go test -race ./...
go test -run '^$' -fuzz FuzzRenderICS -fuzztime=10s
```

The fuzz test checks event boundaries, text escaping, line lengths and CRLF
line endings.

## Open the calendar

Import `calendar.ics` into a calendar application. Stored events retain their
UIDs when the file is regenerated; the importing application determines how
repeated imports are handled.
