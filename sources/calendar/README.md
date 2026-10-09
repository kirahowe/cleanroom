# calendar-source

A stdio MCP server that writes calendar events to a local `.ics` file and
reminders to a local JSON file. It depends only on the Go standard library and
the official MCP Go SDK, and it imports nothing else from the repository that
contains it.

## Tools

- `create_event`: adds an all-day event. Arguments: `title`, `date`
  (`YYYY-MM-DD`), and optionally `end_date` (the inclusive last day),
  `location`, `description`. Returns `uid` and `file`.
- `create_reminder`: adds a reminder. Arguments: `title`, `due`
  (`YYYY-MM-DD`), and optionally `note`. Returns `id` and `file`.
- `list_events`: returns every event.
- `list_reminders`: returns every reminder.

Titles and locations are limited to 200 characters, and descriptions and notes
to 2000. Dates must be real calendar dates, and `end_date` cannot be before
`date`. Invalid input comes back as a tool error.

The create tools are marked as non-read-only, non-destructive, and
non-idempotent. The list tools are marked read-only. All four are marked closed
world.

## Configuration

Files are kept in one directory, set with `--dir` or the `CALENDAR_DIR`
environment variable. The default is `./calendar-data`. The server creates it
if it is missing.

| File | Contents |
| --- | --- |
| `events.json` | Source of truth for events |
| `calendar.ics` | iCalendar file, regenerated from `events.json` on every change |
| `reminders.json` | Reminders |

Writes go to a temporary file that is renamed into place. Each operation
holds a lock on a `.lock` file in the directory, so several servers can share
one directory without losing entries. The directory and its files are readable
only by the user. The server logs to stderr only.

## Run

```
go build -o calendar-source .
./calendar-source --dir ./calendar-data
```

Register the binary as a stdio server in your MCP client, for example with the
command `/path/to/calendar-source` and the argument `--dir /path/to/data`.

## Test

```
go test -race ./...
go test -run '^$' -fuzz FuzzRenderICS -fuzztime=10s
```

The fuzz test generates events from arbitrary text and checks that each result
parses back as one event with the original title, that lines are at most 75
octets, and that every line ends in CRLF.

## Open the calendar

Double-click `calendar.ics` to import it into Apple Calendar, or use
Import in Google Calendar settings. Re-importing the file after adding events
does not duplicate older ones, because each event has a stable `UID`.
