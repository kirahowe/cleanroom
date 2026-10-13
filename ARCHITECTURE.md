# Gateway design

See the [README](README.md) for the rationale and demo, and
[USAGE.md](USAGE.md) for setup, approval commands and adding sources.

## How ask works

```
   agent                    cleanroom                       reader             mail server
     |  ask(mail, "When is     |                               |                    |
     |   the reunion?", date)  |                               |                    |
     |------------------------>|  decide, record the question  |                    |
     |                         |  start a reader ------------->|                    |
     |                         |                               | search, read       |
     |                         |<------- each call decided ----|                    |
     |                         |-------- and recorded, then forwarded ------------->|
     |                         |                               |<-- mail text ------|
     |                         |<-- {"answer": "2027-07-17"} --|                    |
     |                         |  check it against "date"      x  (reader exits)
     |<-- "2027-07-17" --------|
```

`ask(source, question, returns)` starts a reader process with access to one
source. Readers can use read tools and, for non-private sources, tools
explicitly classed as `egress`. Cleanroom treats the reader as compromised
and checks its calls and answers. Each reader has a budget of 40 tool calls.

For an uncontaminated session querying an untrusted source, `returns` controls
what reaches the caller:

| `returns` | Caller receives | Session labels added |
| --- | --- | --- |
| `date`, UTC `datetime`, `number`, `money`, `boolean`, `choice`, or `record` | A checked value or `no_answer` | `private`, if the source is private |
| `text` | An id; the text is stored for the user to review | None |
| `text` with `accept_contamination: true` | The text | `contaminated`, and `private` if applicable |

Typed answers use a fixed grammar: dates, numbers, booleans, currency codes,
and strings supplied by the caller as choices or field names. Cleanroom
serialises the checked value itself. The checker is in
`internal/typed/typed.go`; a fuzz test checks its output against an independent
grammar. It validates structure, not accuracy: a compromised reader can return
the wrong date.

An uncontaminated session must pass `accept_contamination: true` to receive
untrusted free text. Once contaminated, a session stays contaminated. Text
can return directly when the session already carries its labels or the
source is uncontaminated.

With the demo policy, an uncontaminated agent can create a "Family reunion"
event using a typed date without approval. Copying the invitation's address
requires reading free text, which contaminates the session and makes the
event require approval.

### Reader back ends

A reader is a program that receives a question, calls the supplied MCP server,
and prints `{"answer": ...}`. The policy specifies the command:

```jsonc
"reader": ["/path/to/bin/claude-reader", "--mcp-config", "{mcp_config}",
           "--question", "{question}", "--returns", "{returns}"]
```

`{mcp_config}` points to a JSON file in the `mcpServers` format. It connects
the reader to one source through `cleanroom reader-tools` and a temporary Unix
socket, whose path is in the server's `env` block. The gateway authorizes each call and reuses its upstream connections.
The socket closes when the question finishes.

A reader whose MCP client refuses to start that server exits with status 78,
and one whose model refuses its login exits with status 77. Cleanroom shows
nobody what a reader writes, so the status is how `ask` learns the cause and
can tell the person what to allow or where to log in.

`cmd/claude-reader` is a reader backed by a model. It runs a headless
[Claude Code](https://code.claude.com/docs/en/cli-reference) session with the
user's own login and prints the session's answer.

It starts the session with no built-in tools (`--tools ""`), only the MCP
server cleanroom supplies (`--strict-mcp-config`), its own system prompt, an
empty working directory and no saved transcript. Your Claude Code settings
stay in force, so your hooks run in the session. If they list the MCP
servers that are allowed, see
[Allow cleanroom in Claude Code](USAGE.md#allow-cleanroom-in-claude-code).
The answer is requested as structured output that matches the wanted type. `--model` selects the model
and defaults to `opus`.

These flags restrict the session; they are not a sandbox. The reader process
runs as the user, and the restriction is only as good as Claude Code's
handling of those flags. What the reader reads is sent to Anthropic. The
system prompt tells the model to treat source text as data, which lowers the
chance that a message steers it and guarantees nothing: the gateway still
checks every call and the answer.

It has been run against the demo inbox through Mailpit, with typed answers.
It has not been evaluated for accuracy or against prompt injection.
`examples/imap-policy.jsonc` pairs it with the IMAP mail source.

Another headless agent or a program that calls a model API can use the same
interface, provided it has no other tools.

## What the gateway enforces

These rules govern calls routed through cleanroom. They cover all of an
agent's tool calls only when it has no tools or access that bypass the gateway
(see [Limits](#limits)).

Cleanroom tracks two labels per session: `contaminated` for exposure to
untrusted content, and `private` for access to private data. Labels are added
when a call is allowed, before its result arrives, and persist for the
session's lifetime. Policy can also make sessions private from the start.

| Tool class | Meaning | Held for approval when the session is |
| --- | --- | --- |
| `read` | Returns content | Never; the session acquires the content's labels |
| `write` | Changes state inside the system | Never; the upstream acquires the writer's labels |
| `egress` | Can send data out, such as a URL fetch | Contaminated and private |
| `act` | Acts on the user's behalf, such as creating an event | Contaminated |

These are the default hold rules. A tool's `always_hold` policy setting
requires approval for every call, regardless of class or session labels.

The policy assigns tool classes. For upstreams without labels, MCP annotations
(`readOnlyHint`, `destructiveHint`, `openWorldHint`) supply defaults. For
upstreams with labels, every tool needs an explicit policy entry. Unclassified
tools are refused and omitted from the tool list.

`discard_result` suppresses a tool's output, including error text, and returns
only whether it ran. This lets an agent call a tool for its effect without
acquiring labels from the response.

The mail source exposes only `search` and `read`. It has no send tool or SMTP
code, so approval cannot enable sending mail.

The main invariants are:

- The append-only event log is the source of session state. `core.Decide` is
  a pure function of the policy, log and call. `cleanroom explain N` replays
  a decision.
- Each call is decided and recorded under the log's lock before any
  argument-dependent I/O. Read labels apply at that point, so concurrent
  calls see them (`TestParallelCallsCannotSlipPast`).
- Held and refused calls never reach an upstream. Calls to unoffered tools
  are also refused and logged (`TestUnofferedToolIsRefusedAndRecorded`).
  Log summaries omit the attempted tool name; the raw log retains it.
- Labels follow data. Stored answers carry the source's and asker's labels;
  writes propagate labels to the upstream (`TestLabelsFollowStoredValues`,
  `TestAnswersCarryTheAskersLabels`).
- A contaminated, private session cannot ask a source whose reader can send
  data out (`TestAskIsNotAWayOut`). The question itself could contain data.
- Arguments with duplicate keys or keys that differ only in case are refused
  to prevent ambiguous interpretation.
- Reusing a `--session` id restores its history, including contamination
  (`TestResumedSessionIsStillContaminated`).

`TestHistoryProperties` checks random histories across three sessions against
an independent model. Purity tests reject I/O and clock access in the decision
and type-checking packages.

## Limits

- **Tool access:** cleanroom governs only calls routed through it. A shell or
  another tool provider can bypass its restrictions. Typed answer validation
  still applies to `ask`, but it cannot protect other input paths.
- **Process isolation:** readers run as the current user with the inherited
  environment. Their MCP access is restricted, but the processes are not
  sandboxed. A shell-capable agent can read the log, run approval commands,
  or start a server with a new session id. Strong isolation requires a
  sandbox, separate OS user, or approval on another device.
- **Trusted configuration:** the policy and upstream server code are trusted.
  Incorrect classes or labels defeat tracking. Upstream descriptions and
  input schemas reach the agent without content filtering. Tool names receive
  an upstream prefix, and descriptions include the gateway's policy notes.
- **Answer accuracy and covert channels:** type checks do not establish truth.
  A reader can encode bits in valid values: about 17 in a date, 71 in a number,
  or 140 bytes in a record of sixteen numbers (`TestCapacity`). Timing,
  `no_answer`, and ask ids can also convey information. Ask ids reveal earlier
  readers' call counts, bounded by 40 calls per reader.
- **Label granularity:** labels apply to an entire upstream. One contaminated
  write labels the whole store because the gateway cannot track individual
  items inside it.
- **Model privacy:** a hosted reader's model provider sees the source content.
- **Approval:** contaminated, private sessions cannot ask sources whose readers
  can send data out; approval for these asks is not implemented. Reviewing
  many held calls can cause approval fatigue, and their arguments may contain
  attacker-controlled text.
- **Replay:** `explain` uses the current policy. It reports a mismatch if that
  policy produces a different decision from the recorded one.
- **Mail:** reading a message through Mailpit marks it as read. The IMAP back
  end leaves messages unread and supports password login only. It reads one
  mailbox: the server's all-mail mailbox, or INBOX on a server without one.
  See the [mail source](sources/mail/README.md).
- **Log storage:** each decision reads the entire log. It has no checksum and
  cannot detect truncation at a line boundary or tampering by another process
  running as the same user. An unreadable or malformed log stops decisions.
  Failed partial appends are rolled back. `cleanroom repair` can preserve an
  incomplete final line in `events.jsonl.cut-off`; other damage requires
  manual repair.
- **Process cleanup:** cleanroom kills the reader's process group after reaping
  the reader. There is a brief risk of the group id being reused.
- **Platform:** Unix only; log locking uses `flock`.

## Not built yet

Remaining work:

- Readers for other models, including local ones. `claude-reader` needs
  Claude Code and a hosted model.
- A web source, with a check that a held fetch makes no DNS request. The
  core already implements and tests the `egress` rule.
- Approval through MCP elicitation.
- MCP resources for proposals, answers and session state. Currently these use
  CLI commands and the `status` tool.
- An approval web page.

A shared, long-running server is deferred. Each stdio process currently serves
one conversation and keeps its own upstream connections. A shared server
would need another transport and a way for clients to identify their sessions.

## Layout

```
cmd/cleanroom/       CLI, stdio server and reader-tools connection
cmd/claude-reader/   reader that runs a headless Claude Code session
skills/cleanroom/    Claude Code skill that asks through `cleanroom ask`
internal/core/       pure decisions, labels, policy and event state
internal/typed/      pure answer validation
internal/eventlog/   log storage and locking
internal/gateway/    MCP proxy, ask, read_answer and status
internal/reader/     reader processes and tool access
internal/approval/   approval screens and execution
internal/upstream/   upstream MCP connections
internal/config/     policy loading
sources/mail/        mail server (separate Go module)
sources/calendar/    calendar and reminders server (separate Go module)
demo/               policy, messages and seed command for the demo inbox
```

Start with `internal/core/doc.go`, then `internal/core/decide.go`.
