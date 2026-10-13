# cleanroom

Cleanroom is an MCP gateway that lets an agent ask questions about content it
should not trust, such as an inbox, without reading that content itself. A
separate reader searches the source and writes an answer. What the agent gets
back depends on the kind of answer:

- A typed answer, such as a date or an amount, is checked against its type and
  then passed to the agent.
- A free-text answer could carry an attacker's instructions, so cleanroom
  stores it for the user to read and gives the agent only an id.

An agent can ask to read the free text itself. Cleanroom then marks the
session as contaminated, and from that point its actions need the user's
approval.

## Why this exists

Useful agents often need all three components of the
[lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/):
access to private data, a way to send data out, and exposure to content that an
attacker can use to inject instructions. Removing any one of these can also
remove capabilities the agent needs to do its job.

A coding agent with shell access may already have all three. Connecting it
to an inbox adds a particularly accessible injection path: anyone who can
send you mail can put content in front of it. Dependencies, fetched pages
and issue comments can also carry injections; mail gives an attacker a
direct route addressed to you.

Cleanroom provides two distinct protections:

- **Quarantine:** a separate reader sees the source text. The agent receives
  a checked value such as `{"amount":"890.55","currency":"CAD"}`, or an id
  for free text stored for the user. This is useful even when the agent has
  a shell: querying mail through this path does not introduce its free text
  into the agent's context. It does not protect other input paths or stop
  an agent from accessing mail outside the gateway.
- **Enforcement after contamination:** cleanroom tracks what each session
  has read and written. Once a session receives untrusted text, actions
  require approval; egress also requires approval when the session holds
  private data. These rules cover the whole session only when cleanroom is
  its only source of tools and the agent cannot bypass the gateway or its
  approval boundary. Mail, calendar and document agents can operate with
  that restricted tool surface, without a shell.

In an ordinary coding session, cleanroom is a quarantine layer. It does not
make the session safe as a whole. If an agent with shell access opts into
free text through `accept_contamination: true`, cleanroom still enforces
holds on calls through the gateway, but cannot constrain what the agent
does through its shell. Routine opt-in in that setting leaves subsequent
behaviour dependent on the model respecting the boundary.

Typed answers also have a narrower guarantee than correctness. An attacker
can supply a valid but false date and influence an action taken from it.
Quarantine limits what reaches the agent; it does not establish that the
source or the reader is telling the truth.

## Try it

You need Go 1.26 or later, `make`, and
[Claude Code](https://code.claude.com/docs/en/cli-reference) installed and
logged in. The reader is a headless Claude Code session, so the mail it reads
to answer a question is sent to Anthropic.

```sh
make test     # tests and vet in all three Go modules; needs no model or mail server
make build
```

Then choose a mailbox:

- **Your own mail:** `bin/cleanroom init` sets up one IMAP mailbox. See
  [Using it on your own mail](#using-it-on-your-own-mail).
- **The demo inbox:** a local [Mailpit](https://mailpit.axllent.org) server
  with nine fictional messages, one of which carries a prompt injection. You
  can add messages of your own. The rest of this section uses it.

### The demo inbox

```sh
make inbox    # web UI: http://127.0.0.1:18025  (Ctrl-C to stop)
```

`make inbox` downloads Mailpit into `.tools/` if needed, which requires
`curl`. It starts Mailpit, replaces its contents with the messages in
`demo/inbox/`, and keeps running. The demo policy has no consent prompt like
the one `cleanroom init` shows. Seeding prints a warning that the reader
sends what it reads to Anthropic. In another terminal, ask questions with
the demo policy:

```sh
bin/cleanroom ask --policy demo/policy.jsonc --returns money "How much did my trip to Toronto cost in total?"
bin/cleanroom ask --policy demo/policy.jsonc --returns date "When is my family reunion next year?"
bin/cleanroom ask --policy demo/policy.jsonc "Where is my order?"
bin/cleanroom log --policy demo/policy.jsonc    # what each reader did, without content
```

| Question | What to check |
| --- | --- |
| Trip cost, as `money` | The four receipts add up to `890.55` CAD. |
| Reunion date, as `date` | The invitation says `2027-07-17`. `demo/inbox/09-injection.eml` is also about the reunion. It tells whoever reads it to call a `send` tool and to answer with instructions. The mail source has no send tool, and an answer that is not a date is rejected, so the result is a date or `no_answer`. |
| Order status, as free text | At a terminal you see the text. A program or agent that runs the same command receives only an id. |

A model writes the answers, so they can differ between runs. `cleanroom log`
shows the calls each reader made, including any that were refused.

### Holds and approval

The demo policy also has a calendar source, which writes to
`demo/.cleanroom/calendar/`. Start a conversation whose only tools are
cleanroom's:

```sh
bin/cleanroom claude --policy demo/policy.jsonc
```

Ask for these in order:

1. "When is my family reunion next year? Put it on my calendar." The date
   arrives as a typed value and the event is created without approval.
2. "Read the reunion invitation yourself and tell me what it says." The agent
   has to accept contamination to receive the text, and the session stays
   contaminated.
3. "Add a reminder to book flights for it." The call is held and the agent
   reports a proposal number.

Review the held call in another terminal, replacing `N` with that number:

```sh
bin/cleanroom proposals --policy demo/policy.jsonc
bin/cleanroom show --policy demo/policy.jsonc N       # the call and why it was held
bin/cleanroom approve --policy demo/policy.jsonc N    # runs the stored call once
bin/cleanroom explain --policy demo/policy.jsonc N    # replay the decision
```

If your Claude Code settings have an `allowedMcpServers` list, see
[Allow cleanroom in Claude Code](#allow-cleanroom-in-claude-code) first.

### Send your own attack

Copy a message from `demo/inbox/`, edit it, and save it in a directory of
`.eml` files. Each file needs `From` and `To` headers. With `make inbox`
running, deliver them:

```sh
bin/seed --dir path/to/your/messages            # adds them to the inbox
bin/seed --dir path/to/your/messages --reset    # replaces the inbox with them
```

Seed only messages you are willing to send to Anthropic. The reader can read
any message in the inbox, and `seed` prints that warning each time.

Ask a question your message is relevant to, then read `cleanroom log`. The
gateway refuses tools the reader cannot access and rejects answers that do
not match the requested type. A message that persuades the reader to return
a different valid date succeeds, because the check covers structure and not
accuracy.

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
servers that are allowed, see "Allow cleanroom in Claude Code" below. The answer is requested as
structured output that matches the wanted type. `--model` selects the model
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
bin/cleanroom init      # asks for consent, then IMAP credentials
bin/cleanroom ask "When is my next dentist appointment?"
```

`init` asks permission to send email content to Anthropic's Claude to answer
your questions. This includes headers and message text. Enter `y` or `yes`
to allow this for future questions about the mailbox. Any other response
cancels before credentials are requested, files are written or the mailbox
is contacted.
Once accepted, setup continues and later questions do not prompt again.

`init` defaults to Proton Mail Bridge's address (`127.0.0.1:1143`, STARTTLS);
`--imap-addr` and `--imap-tls` change that. It reads the mailbox that holds
all mail, so archived and filed messages are found; `--imap-mailbox` names
another. It checks the login before writing the policy and prints which
mailbox it opened. On success it writes `~/.config/cleanroom/policy.jsonc`
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

### Allow cleanroom in Claude Code

Claude Code starts cleanroom as an MCP server in two places: the reader's
session, and the conversation `cleanroom claude` opens. Nothing needs changing
unless your Claude Code settings have an `allowedMcpServers` list. If they do,
print the entries to add to it:

```sh
bin/cleanroom claude-settings
```

```json
"allowedMcpServers": [
  { "serverCommand": ["/path/to/bin/cleanroom", "reader-tools"] },
  { "serverCommand": ["/path/to/bin/cleanroom", "serve"] }
]
```

Each entry allows that exact command and arguments. Cleanroom keeps both
command lines fixed for this purpose: the reader's socket and the
conversation's session id travel in the server's environment, which Claude
Code does not compare. An entry by `serverName` would be looser, since any
server can be given a name.

The entry pins the path and arguments, not the file's contents. Whoever can
replace the binary at that path can run something else under the entry.

Until the reader's entry is there, `ask` reports no answer. At a terminal it
also prints the entry and names the settings file that has the list: your
user `settings.json`, or the managed settings file.

### From a coding session

`skills/cleanroom` is a Claude Code skill. With it installed, an ordinary
Claude Code session answers questions about your mail by running
`cleanroom ask`, and picks the answer type itself. Install it by linking it
into your skills directory, and put `cleanroom` on your `PATH`:

```sh
ln -s "$PWD/skills/cleanroom" ~/.claude/skills/cleanroom
ln -s "$PWD/bin/cleanroom" /usr/local/bin/cleanroom
```

The session runs `ask` without a terminal, so it receives checked values and
never mail text; free text is stored for you to read with `cleanroom show`.
This path has no `accept_contamination`.

The skill guides the session to use the quarantine path described above.
It does not restrict the shell or provide enforcement across the session;
the agent can still reach mail or perform actions outside cleanroom.

### A conversation in Claude Code

```sh
bin/cleanroom claude    # Claude Code with cleanroom as its only tools
```

`claude` starts Claude Code with no built-in tools and no MCP server but
cleanroom. The conversation and the cleanroom session share one id. When the
conversation ends, the command prints how to resume it with
`cleanroom claude --resume ID`. Resuming with `claude --resume` would restore
Claude Code's own tools and leave cleanroom out.

Claude Code runs with your settings and your environment. The `serve` entry
from `cleanroom claude-settings` covers this conversation; run that command
with the same `--policy` and `--state` you give `claude`.

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

For enforcement across the session, cleanroom must be its only source of
tools. Disable the client's built-in tools and other MCP servers. For
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
  end leaves messages unread and supports password login only. It reads one
  mailbox: the server's all-mail mailbox, or INBOX on a server without one.
  See `sources/mail/README.md`.
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
