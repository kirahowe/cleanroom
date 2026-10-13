# cleanroom

Cleanroom is a proof of concept for an MCP gateway that lets an agent ask
questions about content it should not trust, such as an inbox, without reading
that content itself. A separate reader searches the source and writes an answer.
The agent gets back either:

- A typed answer, such as a date or an amount, which is checked against its type and
  then passed to the agent.
- A free-text answer, which  could carry an attacker's instructions, so cleanroom
  stores it for the user to read and gives the agent only an id.

An agent can ask to read the untrusted free text itself. Cleanroom then marks
the session as contaminated, and from that point its actions need the user's
approval. The premise behind this is that agent permissions depend not only on
their stable identity, but on the mutable content of their _current session_. This
is one attempt to design a solution that can account for the way authority needs
to be constrained by what an agent has seen so far, not just its static
permissions.

## Try it

You need Go 1.26 or later, `make`, `curl` and
[Claude Code](https://code.claude.com/docs/en/cli-reference). Run everything
from the repository root.

1. Log in to Claude Code: run `claude`, then `/login`. Cleanroom needs this
   because the reader is a headless Claude Code session under your login,
   so **what it reads is sent to Anthropic**. Without a login every ask
   comes back with no answer. Support for other agent harnesses is coming soon :)

2. Build cleanroom and make sure Claude Code may start it:

   ```sh
   make build
   bin/cleanroom claude-settings
   ```

   This checks your Claude Code settings for an `allowedMcpServers` list
   and says whether cleanroom's entries are on it. Add any it reports
   missing. Without that list there is nothing to add.

3. Set up a mailbox. There is a demo inbox seeded with fake messages you can use
   and [add your own to](#send-your-own-attack):

   ```sh
   make inbox                   # in its own terminal; leave it running
   bin/cleanroom init --demo
   ```

   Or if you're feeling bold you can use your own mail, with `bin/cleanroom init`,
   which asks for an IMAP user name and password. See
   [Using it on your own mail](#using-it-on-your-own-mail) for more details.

4. Connect an agent. Cleanroom is an MCP server, `cleanroom serve`, and this
   starts Claude Code with that server as its only source of tools:

   ```sh
   bin/cleanroom claude
   ```

   Cleanroom's rules cover a whole session only when it is the agent's only
   source of tools; an agent that also has a shell or other tools can go around
   it. See [Why this exists](#why-this-exists) and [Limits](#limits) for more
   details.

   Some things to ask it about the demo inbox:

   - "When is my family reunion next year?"
   - "How much did my trip to Toronto cost in total?"
   - "Where is my order?" The agent chooses what to ask for. Here it tends
     to ask for typed facts, such as a status and a delivery date, and
     answers from those without seeing the emails.
   - "Put the reunion on my calendar." The demo's calendar is a stand-in
     that writes to local files under `~/.config/cleanroom/.cleanroom/calendar/`.
     It does not touch any real calendar.

   The conversation under [Holds and approval](#holds-and-approval) goes on
   from there to a call that is held. For another MCP client, see
   [Using it from a client](#using-it-from-a-client).

5. Or ask from the command line, with no client to set up:

   ```sh
   bin/cleanroom ask "When is my family reunion next year?"
   ```
   Returns a generated answer.

   ```sh
   bin/cleanroom ask --returns date "When is my family reunion next year?"
   ```
   Returns just a date.

   `ask` runs the same code as the MCP server's `ask` tool. It is a shortcut
   for seeing answers, and the other commands are for the person running
   the server: reading stored answers, approving held calls and reading the
   log.

The rest of this section walks through the demo inbox.

### The demo inbox

`make inbox` builds the binaries into `bin/`, downloads
[Mailpit](https://mailpit.axllent.org) into `.tools/` unless it is already
installed, loads the nine messages in `demo/inbox/` and keeps running until
you press Ctrl-C. You can browse the inbox at http://127.0.0.1:18025. One
message, `09-injection.eml`, carries a prompt injection.

`init --demo` writes a policy for that inbox to
`~/.config/cleanroom/policy.jsonc`, where every command looks for it, so the
commands below need no flags. If you already have a policy configured, `init
--demo` will not overwrite it. In that case you can either delete the old one
or add `--policy demo/policy.jsonc` to each command below.

### Holds and approval

This is the demo of cleanroom as an MCP server. Claude Code connects to
`cleanroom serve` and has no other tools. The demo also has a calendar
source, so the agent has something to act with. In your terminal:

```sh
bin/cleanroom claude
```

Ask for these in order:

1. "When is my family reunion next year? Put it on my calendar." A typed
   date lets the agent create an event without approval.
2. "What else should I know about the reunion? Add the details to the
   event." The agent typically gets the facts that fit a type, such as the
   length and whether there is a cost. The parts that exist only as text,
   such as the address, go to you as a stored answer. The agent gets the
   answer's id, gives you the `cleanroom show` command for it, and offers to
   read the text itself at the price of a contaminated session.
3. "Read the reunion invitation yourself and tell me what it says." Nothing
   asks you to confirm this. The agent opts in on its own, gets the text and
   tells you. Cleanroom marks the session contaminated from then on.
4. "Add a reminder to book flights for it." The call is held and the agent
   reports a proposal number.

The injection asks for a reminder as well. An agent that read it and obeyed
would be making the same kind of call from a contaminated session, and that call
is held in the same way. The idea is that in the future these requests can be
forwarded for approval.

Review it in another terminal, replacing `N` with that number:

```sh
bin/cleanroom proposals
bin/cleanroom show N       # the call and why it was held
bin/cleanroom approve N    # runs the stored call once
bin/cleanroom explain N    # replay with the current policy
```

`show` and `approve` require a terminal. Use `deny` instead of `approve` to
reject a call. See the [approval reference](USAGE.md#how-approval-works)
for details. If Claude Code blocks cleanroom, see
[Allow cleanroom in Claude Code](USAGE.md#allow-cleanroom-in-claude-code).

To see a hold without a conversation, call the tools directly as in
[Hold a call from the command line](USAGE.md#hold-a-call-from-the-command-line).

The log and the demo calendar outlast a conversation. Once a contaminated
session has written to the calendar, the calendar is contaminated too, and
a later conversation that lists it becomes contaminated by doing so. To run
the walkthrough again from a clean start, remove the demo's state:

```sh
rm -rf ~/.config/cleanroom/.cleanroom
```

### Typed questions

The rest of the walkthrough uses `cleanroom ask`, which shows the same
answers without a conversation.

```sh
bin/cleanroom ask --returns money "How much did my trip to Toronto cost in total?"
bin/cleanroom ask --returns date "When is my family reunion next year?"
```

An ask takes a few seconds to a minute. At a terminal it prints the answer
for you to read. Add `| cat` to the end of a command to see what an agent
running it receives:

```json
{
  "status": "answered",
  "id": 88,
  "value": {
    "amount": "890.55",
    "currency": "CAD"
  }
}
```

The four receipts add up to 890.55 CAD and the invitation says `2027-07-17`.
The injection tells whoever reads it to put a payment reminder on your
calendar, and then to answer with a sentence of instructions in place of
the date. Cleanroom does not count on the reader to refuse. The demo has a
calendar tool that creates reminders, but a reader is given only the read
tools of the source it was asked about, here `search` and `read`. An answer
that is not a date is rejected, so the sentence cannot come back through a
typed ask. A model writes the answers, so yours can differ or be
`no_answer`.

### Free text

```sh
bin/cleanroom ask "Where is my order?"
bin/cleanroom ask "Where is my order?" | cat
```

The first command shows you the answer. The second is what an agent gets,
which is an id and no text:

```json
{
  "status": "delivered_to_user",
  "id": 108,
  "note": "The answer is free text from an untrusted source, so it went to the user, not to you. ..."
}
```

`bin/cleanroom answers` lists the stored answers, and
`bin/cleanroom show 108` reads one at a terminal.

### The log

```sh
bin/cleanroom log
```

```
EVENT  TIME      SESSION             WHAT                           DECISION
100    23:15:57  cli-s-c2287b377e42  asked mail                     run
101    23:15:59  reader:100          called mail.search             run
102    23:15:59                      call 101 finished
105    23:16:02  reader:100          called mail.read               run
106    23:16:02                      call 105 finished
107    23:16:04                      answer to ask 100 (typed)
```

The log has each ask, each call its reader made (refused ones included) and
how the ask ended. It has no message text.

### If an ask returns no answer

At a terminal, `ask` prints the reason and what to do about it. Otherwise
the last line of `cleanroom log` gives the reason. Two of them come from
setup:

- `the reader is not logged in to its model`: log in to Claude Code as the
  user running cleanroom.
- `the reader could not start its tools`: your Claude Code settings have an
  `allowedMcpServers` list without cleanroom in it. See
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
an ordinary coding session, there is a [bundled skill](USAGE.md#from-a-coding-session).

## Using it from a client

Register cleanroom as a stdio MCP server; [examples/mcp.json](examples/mcp.json)
is a demo configuration. The [client setup reference](USAGE.md#using-it-from-a-client)
covers paths, flags and tools.

For enforcement across the session, disable the client's built-in tools and
other MCP servers. Give each conversation its own `--session` id, and reuse
that id and state directory on resume so its history is retained.

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
queries. Enforcement across the whole session requires cleanroom to be its only
source of tools, with no way to bypass the gateway or approval boundary. A shell
can bypass both. Type checks can limit what reaches the agent but cannot
establish truth. An attacker can still trick an agent into returning a valid but
incorrect answer.

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

## Develop

```sh
make test
```

This runs the tests and `go vet` in all three Go modules (`.`,
`sources/mail` and `sources/calendar`). They need no model and no mail
server.

To read the code, start with [internal/core/doc.go](internal/core/doc.go) and
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
