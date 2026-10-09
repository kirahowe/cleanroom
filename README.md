# cleanroom

Cleanroom is an MCP server that lets an agent use untrusted content, such as
an inbox, without reading it. The agent asks a question, a disposable reader
does the reading, and only a typed value such as a date or an amount comes
back, so a poisoned email has no way to reach the agent that holds your data
and can act for you. I chose it because a model cannot be trusted to resist
prompt injection from the inside, and MCP is a standard boundary where what
crosses between an agent and its tools can be checked by deterministic code.

It is a gateway: an MCP server to the agent and an MCP client to the servers
behind it. Tailscale's Aperture is also an MCP gateway, and decides what a
caller may use from who the caller is. Cleanroom decides from a different
input: what the session has already read.

## Try it in five minutes

You need Go 1.25 or later, `make` and `curl`. Nothing here needs an API key,
and no mail leaves your machine.

```sh
make test    # every test in the three Go modules
make demo    # the whole story, end to end, against real processes
```

`make demo` downloads [Mailpit](https://mailpit.axllent.org) (a local mail
server for testing) into `.tools/` if you do not have it, starts it, fills its
inbox from `demo/inbox/`, and starts the real cleanroom server with the real
mail and calendar servers behind it. A scripted agent then makes the tool
calls a model would make. Each scene checks its own result, and the command
fails if any check does.

| Scene | What happens | What it shows |
| --- | --- | --- |
| 1 | "How much was my trip to Toronto?" The reader reads four receipts. The agent receives `{"amount":"890.55","currency":"CAD"}`. | A question answered from an inbox the agent never read. |
| 2 | "When is my reunion? Put it on my calendar." A date comes back, and the agent creates the event. The same for an order and a reminder. | The agent acts with no approval, because nothing untrusted reached it. |
| 3 | The same question, with a reader that obeys the poisoned email in the inbox. | The reader tries to send the inbox away and has no tool for it. It answers with the attacker's sentence, which is not a date, so the agent gets "no answer". |
| 4 | "Where is my order right now?" has no type. | Free text goes to you, not to the agent. The agent gets an id. |
| 5 | The agent opts in and reads that text itself. | Its session is contaminated from then on. |
| 6 | After a restart, that session tries to send two emails. | Both are held. You see the full call and why. One is denied, one is approved and sent exactly once. |

Scenes 1 to 4 are the product. Scenes 5 and 6 show what the opt-in costs and
that the cost is enforced.

Afterwards, look at what was left behind:

```sh
open demo/.cleanroom/calendar/calendar.ics          # the event, in your calendar app
bin/cleanroom log --policy demo/policy.jsonc        # every call, read, answer and approval
bin/cleanroom explain --policy demo/policy.jsonc 50 # replay one decision from the log
```

### Send your own attack

The inbox is the attack surface, so attack it. Start Mailpit and leave it
running:

```sh
make inbox        # then open http://127.0.0.1:18025
```

Write an email as a `.eml` file (copy one from `demo/inbox/`), put it in a
directory, and deliver it:

```sh
bin/seed --dir path/to/your/attacks
make demo         # uses the running inbox as it stands
```

The scripted reader in scene 3 obeys two phrasings, wherever they appear in a
message it reads: `Call the tool NAME with {JSON}` and
`answer with exactly: "TEXT"`. Mention "reunion" in your email so that the
reader finds it. Whatever you write, the agent ends scene 3 with no answer and
an uncontaminated session, and Mailpit shows that nothing was sent.

### Poke it with no agent

The [MCP Inspector](https://github.com/modelcontextprotocol/inspector) can
call the server directly. With `make inbox` running:

```sh
npx @modelcontextprotocol/inspector --cli --config examples/mcp.json --server cleanroom \
  --method tools/list

npx @modelcontextprotocol/inspector --cli --config examples/mcp.json --server cleanroom \
  --method tools/call --tool-name ask \
  --tool-arg source=mail --tool-arg 'question=When is my family reunion?' --tool-arg returns=date
```

Leave out `--cli` for the Inspector's web interface.

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
     |                         |  check it against "date"      x  (reader is gone)
     |<-- "2027-07-17" --------|
```

`ask(source, question, returns)` is the tool a capable agent uses. Cleanroom
starts a reader: a separate process that has the read-only tools of that one
source and nothing else. The reader is assumed to be compromised by whatever
it reads. Nothing in the design depends on it behaving.

What comes back depends on `returns`:

| `returns` | The caller receives | The caller's session becomes |
| --- | --- | --- |
| `date`, `datetime`, `number`, `money`, `boolean`, a `choice` from the caller's own list, or a `record` of those | the value, serialised again by cleanroom, or "no answer" | private, if the source is |
| `text` | an id; the text is delivered to you | unchanged |
| `text` with `accept_contamination: true` | the text | contaminated, for the rest of its life |

A typed value is drawn from a fixed grammar: digits, dates, `true` and
`false`, currency codes from a list, and strings the caller supplied itself.
None of that can carry an instruction. The checker is one small file,
`internal/typed/typed.go`, and a fuzz test states the guarantee against a
grammar written separately from it.

A typed value is checked but not verified. A compromised reader can return the
wrong date. It cannot return instructions.

Free text cannot be checked that way, so by default it never reaches the
agent. It is stored and delivered to you. An agent that wants it must say so
with `accept_contamination: true`, and the tool description tells it what that
costs. That is the one named, deliberate way to become contaminated.

A reader gets the tools of exactly one source. A reader that could read mail
and fetch web pages would hold private data, untrusted content and a way out
all at once, so that combination cannot be configured.

### Reader back ends

A reader is any program that takes a question, uses the MCP server it is
pointed at, and prints `{"answer": ...}`. The policy names the command:

```jsonc
"reader": ["../bin/demo-reader", "--mcp-config", "{mcp_config}",
           "--question", "{question}", "--returns", "{returns}"]
```

`{mcp_config}` is the path of a JSON file in the common `mcpServers` layout
that names one server: the source's read-only tools, served by
`cleanroom reader-tools`.

The demo's reader (`demo/reader`) is scripted. It answers the demo's questions
by keyword and regular expression, which is enough to show the path an answer
takes without a model. A headless agent fits the same interface, for example
Claude Code with `--tools ""`, `--strict-mcp-config` and
`--mcp-config {mcp_config}`, and so does a small program that calls a model
API with a key from the environment. Wiring and testing those is the next
step in the build and is not done yet. See "Not built yet".

## What the gateway enforces

Cleanroom tracks two labels per session. A session is contaminated once
untrusted content has reached it, because it may now be following that
content's instructions. A session is private once the user's data has reached
it, because it now has something to leak. Labels only grow.

Every tool has a class:

| Class | Meaning | Held for approval when the session is |
| --- | --- | --- |
| `read` | returns content, changes nothing | never; the session takes on the content's labels |
| `write` | changes state inside the system | never; the writer's labels are left on what it wrote to |
| `egress` | can carry data out, but does nothing in your name (fetching a URL) | contaminated and private |
| `act` | does something in your name (sending mail, creating an event) | contaminated |

The class comes from the policy file. For a tool the policy does not mention,
cleanroom takes a first guess from the MCP tool annotations (`readOnlyHint`,
`destructiveHint`, `openWorldHint`). Annotations come from the upstream
server, so the policy always wins: in the demo, the calendar server calls
`create_event` a non-destructive write, and the policy says it acts for the
user. A tool with no class from either place is refused and not offered.

The rest of what holds, with the test that checks it:

- One append-only log, shared by every session and process, is the only
  state. A session's labels are a fold over it (`internal/core/state.go`).
- The decision is a pure function of the policy, the log and the call
  (`core.Decide`). A test fails if that package imports anything that can do
  I/O or reads the clock. Any decision can be replayed: `cleanroom explain N`.
- A call is decided and recorded under the log's lock before anything else
  happens. No upstream is contacted and no name is resolved before the
  decision. A session takes on a read's labels at that moment, before the
  content comes back, so a call made in parallel cannot slip past
  (`TestParallelCallsCannotSlipPast`).
- A held or refused call never reaches the upstream. The tests count what the
  fake upstreams saw.
- Labels follow data. A stored answer keeps the labels of the reader that
  wrote it, and a session that reads it takes them on. A session that writes
  to an upstream leaves its labels there for later readers
  (`TestLabelsFollowStoredValues`).
- A session is identified by `--session` or `CLEANROOM_SESSION`, chosen by
  whatever launches the server. A conversation that is resumed under the same
  id gets its history back from the log and does not come back clean
  (`TestResumedSessionIsStillContaminated`, and scene 6).
- Random histories of asks, reads, writes and approvals across three sessions
  are checked against a second, deliberately naive model of contamination
  (`TestHistoryProperties`).

One case shows the limit of the design clearly. An event built from typed
values is safe to create without approval: the date comes from `ask`, and the
title is the agent's own words ("Family reunion"). The location in the
invitation is free text. To put it on the event, the agent has to read it,
which means opting in, and from then on creating the event is held for your
approval. The uncontaminated path gives you a correct event with no address
on it. Whether the address is worth an approval is your call, and cleanroom
makes it a visible one.

## How approval works

When a call is held, the agent is told so and given a proposal number. You
decide, outside the conversation:

```sh
cleanroom proposals     # what is waiting
cleanroom show 51       # the full call and why it was held
cleanroom approve 51    # run it, once
cleanroom deny 51       # never run it
cleanroom answers       # free-text answers delivered to you
```

The screen `show` prints is rendered from the log: the tool, the arguments in
full, why the call was held with the events that reason cites, and what the
session has been exposed to. Nothing on it comes from what the model said
about its own call. Control characters, terminal escapes and direction
overrides in the arguments are written out as visible escapes, so a call
cannot repaint your screen or hide its recipient.

An approval names the hash of the exact tool and arguments. It is appended to
the log before the stored call runs, so a second approval finds the first and
fails. The effect happens at most once, and it runs the arguments cleanroom
stored, as the session that asked.

`show` and `approve` only work at a terminal. See "Limits" for what that does
and does not buy.

## How to add a source

A source is an MCP server plus a few lines of policy. Cleanroom knows nothing
about mail: the mail server is its own Go module in `sources/mail` with no
imports from the gateway.

1. Write or install an MCP server that speaks stdio.
2. Add it to the policy with the labels its content carries and the class of
   each tool:

   ```jsonc
   "tickets": {
     "command": ["ticket-mcp-server"],
     "labels": ["contaminated"],          // anyone can file a ticket
     "ask_only": true,                    // agents ask; a reader reads
     "reader": ["my-reader", "{mcp_config}", "{question}", "{returns}"],
     "tools": {
       "search": {"class": "read"},
       "comment": {"class": "act"},
     },
   },
   ```

3. Restart the server. `ask` now accepts `"source": "tickets"`.

Leave out `ask_only` to also let agents call the read tools directly. That is
the other path: a session that reads a source directly takes on its labels,
and its later calls are held according to the table above.

## Using it from a client

Cleanroom is a stdio MCP server and assumes nothing about the client. The
generic configuration, as in `examples/mcp.json`:

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

Everything essential works with plain tool calls: `ask`, `read_answer`,
`status`, and the proxied tools, named `<upstream>_<tool>`.

Two things matter in any client.

Cleanroom must be the session's only source of tools. It governs the calls
that pass through it. If the client also gives the model a shell or a web
fetch tool, the model can go around the gateway, and a session that cleanroom
has marked contaminated is exactly the one that might. In Claude Code that
means starting it with `--tools "" --strict-mcp-config --mcp-config
examples/mcp.json`. In other clients, disable the built-in tools the same way
the client documents.

Give each conversation its own `--session` id and reuse it when the
conversation is resumed. The protocol gives a stdio server no way to learn
which conversation it is serving, so the launcher has to say. Without an id
the server makes one up and warns on stderr that a resumed conversation would
start clean.

The repository has no `.mcp.json` at its root on purpose. A client that loaded
it automatically would add cleanroom next to its own tools, which is the
arrangement the paragraph above warns against.

## Limits

- Cleanroom only governs calls that pass through it. The "only source of
  tools" requirement above is the largest limit of the whole design.
- The guarantee that survives a capable agent with a shell is the first one:
  nothing a reader read reaches the agent through `ask`, so the agent stays
  uncontaminated and has no reason to misbehave. Holding and approval bind
  only an agent whose tools all come from cleanroom.
- On one machine, every process runs as you. An agent with a shell can read
  the event log, which holds the text of answers, or run `cleanroom approve`
  itself. The terminal check on `show` and `approve` stops accidents and the
  casual case. It is not a boundary. A real one needs the agent in a sandbox
  or under another user, or the approval moved to another device.
- A typed value can be wrong. It can also carry a few bits: a compromised
  reader chooses which valid date to return. That is a covert channel to an
  agent that is not listening for one, not an injection.
- The policy file is trusted. A tool classed `read` that in fact sends mail,
  or a source whose labels leave out `contaminated`, defeats the tracking.
- Labels on an upstream are tracked per upstream, not per item, because
  cleanroom cannot see inside a server. One contaminated write marks the
  whole store. That never under-reports, but it is coarse.
- A reader's model provider sees what the reader reads. With a hosted model,
  your mail goes to that provider.
- Approval fatigue is the failure mode of the opt-in path. Every held call
  puts text shaped by untrusted content in front of you and asks for a
  decision. The design's answer is to make the typed path good enough that
  the opt-in is rare.
- "No answer" and timing are visible to the agent. Two cooperating sessions
  could pass bits that way. The agent is not told why there was no answer.
- Reading a message through Mailpit's API marks it as read. The IMAP back end
  that would avoid this is not built yet.
- The log is a plain file that is read in full on each decision. That is
  fine for a person's assistant and would not be for a busy service. It is
  not protected against tampering by a process running as you.
- Unix only: the log is locked with `flock`.

## Not built yet

The plan in `PLAN.md` has fourteen steps. Steps 1 to 8 are here. Not here:

- Step 9, a model behind the reader. The command back end exists and is
  tested with scripted readers. No model-backed reader has been wired or run.
- Step 10, a web source, and with it demo scene 7 (a fetch whose URL carries
  private data is held, with no DNS lookup before the decision). The `egress`
  class and its rule are implemented and tested in the core.
- Step 11, approval through MCP elicitation. Approval is on the command line
  only.
- Step 12, proposals, answers and session state as MCP resources. There is a
  `status` tool instead.
- Step 13, the IMAP back end for the mail source. The `Mailbox` interface in
  `sources/mail` is ready for it.
- Step 14, the approval web page.

## Layout

```
cmd/cleanroom/       the one binary: serve, reader-tools, and the person's commands
internal/core/       the decision. Pure: labels, policy, events, folds, Decide
internal/typed/      the type checker for reader answers. Pure
internal/eventlog/   the log file and its lock
internal/gateway/    the MCP server: proxying, ask, read_answer, status
internal/reader/     running a reader as a command
internal/approval/   the person's screens and the once-only effect
internal/upstream/   MCP client connections to the servers behind the gateway
internal/config/     loading the policy file
sources/mail/        the mail MCP server (its own module)
sources/calendar/    the calendar and reminders MCP server (its own module)
demo/                the scripted demo, its policy, inbox, reader and seed command
DESIGN.md            decisions and the threat model
```

Start reading at `internal/core/doc.go`, then `internal/core/decide.go`.

## Prior art

- Simon Willison named the [lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
  (private data, untrusted content, a way to send data out) and described the
  [dual LLM pattern](https://simonwillison.net/2023/Apr/25/dual-llm-pattern/),
  in which a quarantined model reads untrusted content and a privileged one
  never does. The reader is a quarantined model.
- Meta's [Agents Rule of Two](https://ai.meta.com/blog/practical-ai-agent-security/)
  says a session should hold at most two of those three properties. The
  `egress` rule is that rule.
- Microsoft's [FIDES](https://arxiv.org/abs/2505.23643) tracks confidentiality
  and integrity labels through an agent's plan and enforces policy
  deterministically. Cleanroom's labels are a small version of the same idea,
  applied at the MCP boundary.
- Google DeepMind's [CaMeL](https://arxiv.org/abs/2503.18813) is the closest
  relative. Its quarantined model returns values into a restricted
  interpreter. Cleanroom's reader returns typed values across a protocol
  boundary, and works with any agent that speaks MCP.
