# cleanroom

Cleanroom is an MCP gateway that lets an agent query untrusted content without
reading it directly. A separate reader searches the source and returns an
answer. Cleanroom checks typed answers, such as dates and amounts, before
passing them to the agent. Uncontaminated sessions receive untrusted free text
only if they explicitly accept contamination; otherwise it is stored for the
user to review.

## Why this exists

Useful agents often need all three components of the
[lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/):
access to private data, a way to send data out, and exposure to content that an
attacker can use to inject instructions. Removing any one of these can also
remove capabilities the agent needs to do its job.

Traditional access controls usually base authority on identity and roles.
Those can change, but they remain properties the system can check at each
request. For an agent, authority also needs to depend on the current session.
The same agent, with the same credentials, may need different permissions
after reading an untrusted email.

Cleanroom makes that history part of access control. It tracks what each
session has read and written, and uses that state to decide whether a tool
call can run, needs approval, or must be refused. Typed queries let the agent
use information from untrusted sources without reading their text.

## Try it in five minutes

You need Go 1.26 or later, `make` and `curl`. No API key is required, and no
mail leaves your machine.

```sh
make test    # tests and vet in all three Go modules
make demo    # six scenes, 22 checks
```

`make demo` downloads [Mailpit](https://mailpit.axllent.org), a local test mail
server, into `.tools/` if needed. It starts Mailpit, seeds it from
`demo/inbox/`, and runs cleanroom with the mail and calendar servers. A
scripted agent makes tool calls and checks the results. Any failed check
fails the command.

| Scene | What happens | Result |
| --- | --- | --- |
| 1 | The reader reads four receipts for a trip to Toronto. | The agent receives `{"amount":"890.55","currency":"CAD"}` without reading the messages. |
| 2 | The agent asks for a reunion date and an order's delivery date, then creates an event and a reminder. | Both actions run without approval; the session is uncontaminated. |
| 3 | A reader obeys the poisoned email. | Its attempt to send mail is refused. Its answer contains instructions instead of a date, so the agent receives `no_answer`. |
| 4 | The agent asks for an order's location as free text. | The user receives the text. The agent receives an id. |
| 5 | The agent opts in to read that text. | The session becomes contaminated. |
| 6 | After a restart, the same session tries to create two calendar events. | Both are held. The user denies one and approves the other, which runs once. |

Inspect the results:

```sh
open demo/.cleanroom/calendar/calendar.ics          # open the calendar file
bin/cleanroom log --policy demo/policy.jsonc        # event summaries, without content
bin/cleanroom explain --policy demo/policy.jsonc 51 # replay a decision
```

### Send your own attack

Start Mailpit and leave it running:

```sh
make inbox        # web UI: http://127.0.0.1:18025
```

Copy an email from `demo/inbox/`, edit it, and save it in a directory of
`.eml` files. Then deliver the files and run the demo:

```sh
bin/seed --dir path/to/your/attacks
make demo         # uses the running inbox
```

The scripted reader in scene 3 recognises two instructions in messages:
`Call the tool NAME with {JSON}` and `answer with exactly: "TEXT"`. Include
"reunion" so the reader finds your message. The gateway refuses tools the
reader cannot access and rejects answers that do not match the requested
type. A valid date can pass the check even if it is wrong. The demo checks
results against its bundled messages, so custom messages can cause checks to
fail.

### Call the server with MCP Inspector

The [MCP Inspector](https://github.com/modelcontextprotocol/inspector) can
call the server directly. With `make inbox` running, run these commands from
the repository root:

```sh
npx @modelcontextprotocol/inspector --cli --config examples/mcp.json --server cleanroom \
  --method tools/list

npx @modelcontextprotocol/inspector --cli --config examples/mcp.json --server cleanroom \
  --method tools/call --tool-name ask \
  --tool-arg source=mail --tool-arg 'question=When is my family reunion?' --tool-arg returns=date
```

Omit `--cli` to use the Inspector's web interface.

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
"reader": ["../bin/demo-reader", "--mcp-config", "{mcp_config}",
           "--question", "{question}", "--returns", "{returns}"]
```

`{mcp_config}` points to a JSON file in the `mcpServers` format. It connects
the reader to one source through `cleanroom reader-tools` and a temporary Unix
socket. The gateway authorizes each call and reuses its upstream connections.
The socket closes when the question finishes.

The demo reader in `demo/reader` uses keywords and regular expressions and
knows only the demo's questions.

`cmd/claude-reader` is a reader backed by a model. It runs a headless
[Claude Code](https://code.claude.com/docs/en/cli-reference) session with the
user's own login and prints the session's answer:

```jsonc
"reader": ["/path/to/bin/claude-reader", "--mcp-config", "{mcp_config}",
           "--question", "{question}", "--returns", "{returns}"]
```

It starts the session with no built-in tools (`--tools ""`), only the MCP
server cleanroom supplies (`--strict-mcp-config`), no user or project settings
and so no hooks (`--setting-sources ""`), its own system prompt, an empty
working directory and no saved transcript. The answer is requested as
structured output that matches the wanted type. `--model` selects the model
and defaults to `sonnet`.

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

## How approval works

A held call returns a proposal number. Review it outside the agent's
conversation:

```sh
cleanroom proposals     # list held calls
cleanroom show 51       # show the call and why it was held
cleanroom approve 51    # execute the stored call once
cleanroom deny 51       # reject the call
cleanroom answers      # list answers delivered to the user
cleanroom repair       # set aside an incomplete final log line after a crash
```

The approval screen shows the tool, full arguments, session labels and the
events behind the decision. It uses the event log, with control characters,
terminal escapes and direction overrides displayed as visible escapes.

Approval is bound to a hash of the tool and arguments and recorded before
execution. Cleanroom runs the stored call as the requesting session, at most
once. A second approval is refused.

`show` and `approve` require a terminal. This prevents accidental use by an
agent but does not isolate approval from an agent with shell access.

## How to add a source

A source is a stdio MCP server with a policy entry. The mail and calendar
servers are separate Go modules with no imports from the gateway.

1. Write or install a stdio MCP server.
2. Add its command, content labels, reader and tool classes to the policy:

   ```jsonc
   "tickets": {
     "command": ["ticket-mcp-server"],
     "labels": ["contaminated"],          // anyone can file a ticket
     "ask_only": true,
     "reader": ["my-reader", "--mcp-config", "{mcp_config}",
                "--question", "{question}", "--returns", "{returns}"],
     "tools": {
       "search": {"class": "read"},
       "comment": {"class": "act", "discard_result": true},
     },
   },
   ```

   Give every tool an entry. Pass `{question}` as a flag's value, as shown,
   so a question starting with `-` is not interpreted as a reader option.
3. Restart the server. `ask` now accepts `"source": "tickets"`.

Omit `ask_only` to expose read tools directly to agents. Direct reads add the
source's labels to the calling session.

## Using it on your own mail

Two commands set up one IMAP mailbox and ask it a question:

```sh
make build
bin/cleanroom init      # asks for the IMAP user name and password
bin/cleanroom ask "When is my next dentist appointment?"
```

`init` defaults to Proton Mail Bridge's address (`127.0.0.1:1143`, STARTTLS);
`--imap-addr`, `--imap-tls` and `--imap-mailbox` change that. It logs in before
it writes anything. On success it writes `~/.config/cleanroom/policy.jsonc`
and, beside it, the password in a file only you can read. `init` needs a
terminal and takes the password only from the keyboard.

`ask` puts the question through the gateway as an agent's ask would go: the
ask is decided and recorded, a reader searches and reads, and the answer is
checked. The policy `init` writes uses `claude-reader`, so the mail a
question touches is sent to Anthropic. `--returns` asks for a checked type
(`date`, `datetime`, `number`, `money`, `boolean`, or a JSON spec) in place
of free text.

At a terminal, `ask` prints the answer on the screen `cleanroom show` uses.
Without a terminal it prints what the ask tool returns, so a program or an
agent that runs it gets a checked value, and for free text only an id.
`cleanroom log` lists what the reader did, without content.

### A conversation in Claude Code

```sh
bin/cleanroom claude    # Claude Code with cleanroom as its only tools
```

`claude` starts Claude Code with no built-in tools and no MCP server but
cleanroom. The conversation and the cleanroom session share one id. When the
conversation ends, the command prints how to resume it with
`cleanroom claude --resume ID`. Resuming with `claude --resume` would restore
Claude Code's own tools and leave cleanroom out.

Claude Code runs with your settings and your environment. If your settings
restrict MCP servers (`allowedMcpServers`), they must allow `cleanroom`, or
the conversation starts without any tools.

## Using it from a client

Register cleanroom as a stdio MCP server:

```json
{
  "mcpServers": {
    "cleanroom": {
      "command": "/absolute/path/to/bin/cleanroom",
      "args": ["serve", "--policy", "/absolute/path/to/demo/policy.jsonc", "--session", "my-conversation"]
    }
  }
}
```

Configuration comes from flags and the policy file. The default policy path is
`~/.config/cleanroom/policy.jsonc`. State is stored in `.cleanroom` beside the
policy unless `--state` overrides it. The tools are `ask`, `read_answer`,
`status`, and proxied tools named `<upstream>_<tool>`.

Cleanroom must be the session's only source of tools. Disable the client's
built-in tools and other MCP servers. For
[Claude Code](https://code.claude.com/docs/en/cli-reference), use
`--tools "" --strict-mcp-config --mcp-config examples/mcp.json`. The repository
omits a root `.mcp.json` to avoid automatic loading alongside unrestricted
tools.

Give each conversation a unique `--session` id and reuse it on resume. Without
an id, the server generates one and warns that a resumed conversation would
start without its history.

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
  end leaves messages unread, reads one mailbox, and supports password login
  only. See `sources/mail/README.md`.
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
- A web source and demo scene 7, which checks that a held fetch makes no DNS
  request. The core already implements and tests the `egress` rule.
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
demo/               scripted agent, reader, policy, inbox and seed command
```

Start with `internal/core/doc.go`, then `internal/core/decide.go`.

## Prior art

- Simon Willison's [lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
  describes the combination of private data, untrusted content and external
  communication. His [dual LLM pattern](https://simonwillison.net/2023/Apr/25/dual-llm-pattern/)
  separates a quarantined reader from a privileged agent.
- Meta's [Agents Rule of Two](https://ai.meta.com/blog/practical-ai-agent-security/)
  recommends supervision when a session combines untrusted input, sensitive
  access, and state changes or external communication.
- [FIDES](https://arxiv.org/abs/2505.23643) tracks confidentiality and
  integrity labels through an agent's plan and enforces policy deterministically.
  Cleanroom applies similar labels at the MCP boundary.
- [CaMeL](https://arxiv.org/abs/2503.18813) separates control and data flow and
  checks capabilities when tools are called. Cleanroom enforces its policy at
  the MCP boundary.
