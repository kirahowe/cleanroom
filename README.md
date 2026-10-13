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

Agentic systems often need all three components of the [lethal
trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/) to be
useful (access to private data, a way to send data out, and exposure to content
that an attacker can use to inject instructions).

Many agents with shell access running locally already have all three, but I am
experimenting with ways to make working with agents locally safer. Connecting a
typical agent to an email inbox adds a particularly accessible injection path --
anyone who can send you mail can add content to your agent's context.
Dependencies, fetched pages and issue comments can also carry malicious
instructions, but we mostly just accept these risks for now since they are
relatively low.

Traditional access controls usually base authority on identity and roles. Those
can change, but they remain properties the system can check on each request. For
an agent, though, authority also needs to depend on the current session. The
same agent, with the same credentials, may need its permissions restricted after
reading untrusted content, for example. For agentic systems, authority is
derived from mutable session state, not only identity.

Cleanroom is an attempt to make that history part of access control. It tracks
what each session has read and written, and uses that state to decide whether a
tool call can run, needs approval, or must be refused. Typed queries let the
agent use information from untrusted sources without reading their text.

In a coding session with a shell, this provides a quarantine path for mail
queries. Enforcement across the whole session requires cleanroom to be its
only source of tools, with no way to bypass the gateway or approval boundary.
A shell can bypass both. Type checks limit what reaches the agent; they do
not establish truth. An attacker can still supply a valid but false date.

## Try it

You need Go 1.26 or later, `make`, and
[Claude Code](https://code.claude.com/docs/en/cli-reference) installed and
logged in. The reader is a headless Claude Code session: **what it reads is
sent to Anthropic**. Start with the fictional demo inbox, or
[set up your own mailbox](#using-it-on-your-own-mail).

```sh
make test     # tests and vet in all three Go modules; no model or mail server needed
make inbox    # builds binaries; web UI: http://127.0.0.1:18025 (Ctrl-C to stop)
```

`make inbox` downloads [Mailpit](https://mailpit.axllent.org) into `.tools/`
if needed (requires `curl`), replaces its inbox with the nine messages in
`demo/inbox/`, and keeps running. One message carries a prompt injection.
In another terminal:

```sh
bin/cleanroom ask --policy demo/policy.jsonc --returns money "How much did my trip to Toronto cost in total?"
bin/cleanroom ask --policy demo/policy.jsonc --returns date "When is my family reunion next year?"
bin/cleanroom ask --policy demo/policy.jsonc "Where is my order?"
bin/cleanroom log --policy demo/policy.jsonc
```

The receipts total `{"amount":"890.55","currency":"CAD"}` and the invitation
says `2027-07-17`. The injection asks the reader to call a `send` tool and
answer with instructions. The mail source has no send tool, and an answer
that is not a date is rejected. A model writes the answers, so actual results
can differ or be `no_answer`. `log` lists reader calls, including refused
ones, without content.

For the free-text order question, `ask` shows the answer at a terminal.
A program or agent running the same command receives only an id for the
user to review with `cleanroom show`.

### Holds and approval

The demo also has a calendar source. In your terminal, start a conversation
whose only tools are cleanroom's:

```sh
bin/cleanroom claude --policy demo/policy.jsonc
```

Ask for these in order:

1. "When is my family reunion next year? Put it on my calendar." A typed
   date lets the agent create an event without approval.
2. "Read the reunion invitation yourself and tell me what it says." Reading
   the text requires accepting contamination, which persists for the session.
3. "Add a reminder to book flights for it." The call is held and the agent
   reports a proposal number.

Review it in another terminal, replacing `N` with that number:

```sh
bin/cleanroom proposals --policy demo/policy.jsonc
bin/cleanroom show --policy demo/policy.jsonc N       # the call and why it was held
bin/cleanroom approve --policy demo/policy.jsonc N    # runs the stored call once
bin/cleanroom explain --policy demo/policy.jsonc N    # replay with the current policy
```

`show` and `approve` require a terminal. Use `deny` instead of `approve` to
reject a call. See the [approval reference](USAGE.md#how-approval-works)
for details. If Claude Code blocks cleanroom, see
[Allow cleanroom in Claude Code](USAGE.md#allow-cleanroom-in-claude-code).

### Send your own attack

Copy and edit a message from `demo/inbox/` into a directory of `.eml` files,
each with `From` and `To` headers. With `make inbox` running:

```sh
bin/seed --dir path/to/your/messages            # adds them to the inbox
bin/seed --dir path/to/your/messages --reset    # replaces the inbox with them
```

Seed only messages you are willing to send to Anthropic; the reader can read
any message in the inbox. Ask a question your message is relevant to, then
inspect `cleanroom log`. A valid but false answer can pass the type check.

### Call the server with MCP Inspector

See the [MCP Inspector commands](USAGE.md#call-the-server-with-mcp-inspector)
to list tools and call `ask` directly, or use its web interface.

## Using it on your own mail

```sh
make build
bin/cleanroom init      # in your terminal: consent, then IMAP credentials
bin/cleanroom ask --returns date "When is my next dentist appointment?"
```

`init` asks consent to send mail content to Anthropic, checks the login and
saves the policy and password under `~/.config/cleanroom/`. It defaults to
Proton Mail Bridge and reads the server's all-mail mailbox, or INBOX if none
exists. See [mail setup](USAGE.md#using-it-on-your-own-mail) for overrides.

Use `bin/cleanroom claude` for a conversation with cleanroom as its only
tools. Resume it with the printed `cleanroom claude --resume ID` command;
plain `claude --resume` restores Claude Code's own tools. To query mail from
an ordinary coding session, install the [bundled skill](USAGE.md#from-a-coding-session).

## Using it from a client

Register cleanroom as a stdio MCP server; [examples/mcp.json](examples/mcp.json)
is a demo configuration. The [client setup reference](USAGE.md#using-it-from-a-client)
covers paths, flags and tools.

For enforcement across the session, disable the client's built-in tools and
other MCP servers. Give each conversation its own `--session` id, and reuse
that id and state directory on resume so its history is retained.

## What the gateway enforces

Cleanroom tracks two persistent labels: `contaminated` for exposure to
untrusted content, and `private` for access to private data. The policy can
mark sessions private from the start to account for context the gateway
cannot observe.

| Tool class | Effect | Held for approval when the session is |
| --- | --- | --- |
| `read` | Returns content; the session acquires its labels | Never by default |
| `write` | Changes internal state; the upstream acquires the writer's labels | Never by default |
| `egress` | Can send data out | Contaminated and private |
| `act` | Acts for the user, such as creating an event | Contaminated |

Policy assigns tool classes and can require approval for every call with
`always_hold`. Unclassified tools are refused. Each call is decided and
recorded before any argument-dependent I/O; held and refused calls never
reach an upstream.

`ask` gives a separate reader access to one source and checks its answer.
Supported types are `date`, UTC `datetime`, `number`, `money`, `boolean`,
caller-defined `choice` and `record`. For an uncontaminated session querying
an untrusted source, free text is stored for the user unless the agent opts
in with `accept_contamination: true`.

See [ARCHITECTURE.md](ARCHITECTURE.md#how-ask-works) for the reader protocol, label rules,
invariants and tests, and [USAGE.md](USAGE.md#how-to-add-a-source) to add a source.

## Limits

Cleanroom governs calls routed through it. Readers run as the current user
and are **not sandboxed**; their tool restrictions depend on the reader
implementation. An agent with shell access can bypass the gateway, read its
logs or run approval commands. Policy, upstream code and tool metadata are
trusted.

Typed answers can be false and can encode information in valid values.
Labels apply to whole upstreams, and the log cannot detect tampering by
another process running as the same user. The hosted model provider sees
source content. The reader has not been evaluated for accuracy or against
prompt injection.

The implementation is Unix-only. See [detailed limits](ARCHITECTURE.md#limits)
and [remaining work](ARCHITECTURE.md#not-built-yet).

## Code and background

Start with [internal/core/doc.go](internal/core/doc.go) and
[internal/core/decide.go](internal/core/decide.go), then
[internal/typed/typed.go](internal/typed/typed.go) and
[internal/gateway/ask.go](internal/gateway/ask.go). Decisions and type checks
are pure; surrounding packages handle effects. Mail and calendar are
separate modules under `sources/`. See the [layout](ARCHITECTURE.md#layout).

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
