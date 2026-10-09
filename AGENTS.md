# Instructions for agents

This repository is cleanroom, an MCP gateway that lets an agent use untrusted
content (an inbox, in the demo) without reading it. `README.md` explains the
design. This file tells you how to show someone that it works.

## If you are asked to show how this works

Run these from the repository root, in order. None of them prompts for input
or needs an API key, and no mail leaves the machine.

```sh
go version      # needs Go 1.25 or later
make test       # under a minute: every test in the three Go modules
make demo       # about 10 seconds after the first build: six scenes, 22 checks
```

`make demo` builds the binaries, downloads Mailpit (a local test mail server)
into `.tools/` if it is not installed, starts it on ports 18025 and 11025,
fills its inbox from `demo/inbox/`, plays the scenes and stops Mailpit again.
It prints `All 22 checks passed.` and exits 0, or names the check that failed
and exits 1. If the download fails because there is no network, say so: the
tests still run without Mailpit.

Then explain what the output shows. Go scene by scene:

1. Scene 1: the agent asks what a trip cost. Point out the `reader` lines:
   a separate process searched and read four messages. The agent received
   only `{"amount":"890.55","currency":"CAD"}`, and its session is `private`
   but not contaminated.
2. Scene 2: a date comes back and the agent creates a calendar event with it.
   Point out that the event ran with no approval, and why: the agent never
   read the mail, so nothing could have told it what to do.
3. Scene 3: the same question with a reader that obeys the poisoned email
   `demo/inbox/09-injection.eml`. Point out the two `(obeying the email)`
   lines: the reader tried to send the inbox away and had no such tool, then
   answered with the attacker's sentence. Point out `no_answer`: the sentence
   is not a date, so none of it reached the agent.
4. Scene 4: a question with no type. The agent gets an id and nothing else.
   The text is shown to the person.
5. Scene 5: the agent opts in with `accept_contamination: true` and reads the
   text. Point out that its session is now `contaminated, private`, and the
   line saying which event did it.
6. Scene 6: after a restart under the same session id, two sends are held.
   Point out the approval screen: every line of it comes from the event log.
   One send is denied, one is approved and runs once, a second approval is
   refused, and Mailpit holds exactly one message.

After the demo, these read-only commands show the record behind it:

```sh
bin/cleanroom log --policy demo/policy.jsonc         # every event, one line each
bin/cleanroom proposals --policy demo/policy.jsonc   # the two held sends
bin/cleanroom explain --policy demo/policy.jsonc 50  # replay the decision to hold a send
```

`explain` runs the decision function again on the log as it stood before
event 50 and reports that the result matches what was recorded. That works
because the decision is a pure function of the policy, the log and the call.

To show the code, start with `internal/core/doc.go` and
`internal/core/decide.go` (the decision), then `internal/typed/typed.go` (the
check on a reader's answer), then `internal/gateway/ask.go`.

## Things that will not work, by design

- `bin/cleanroom show N` and `bin/cleanroom approve N` refuse to run unless
  they are at a terminal. They are for the person. Do not work around this.
  If the person wants to see those screens, ask them to run the command in
  their own terminal. The demo prints the same screens for scenes 4 and 6.
- Do not read `demo/.cleanroom/events.jsonl` to explain the demo. Use
  `bin/cleanroom log`, which prints summaries and no content. The log file
  holds the text of answers, which in a real setup is text from someone's
  inbox that an agent is meant not to read. The same goes for querying
  Mailpit's API directly.

## If the person wants to try it themselves

- Their own attack email: `make inbox` in one terminal (it stays running),
  then `bin/seed --dir <a directory of .eml files>`, then `make demo`. The
  README section "Send your own attack" has the details.
- Calling the server by hand: the MCP Inspector commands in the README
  section "Poke it with no agent".
- Connecting a client: `examples/mcp.json`, and the README section "Using it
  from a client". Cleanroom must be the session's only source of tools for
  its guarantees to hold.

## If you are asked to change the code

- `make test` must pass. Run `gofmt -l .` and `go vet ./...` in each module
  you touched (`.`, `sources/mail`, `sources/calendar`).
- `internal/core` and `internal/typed` are pure. A test fails if they import
  a package that can do I/O or read the clock. Decisions go there. Effects go
  in the packages around them.
- Nothing that depends on a call's arguments may happen before `core.Decide`
  has been called and the decision recorded.
- The gateway knows nothing about mail or calendars. Code that does belongs
  under `sources/` or `demo/`.
- Say "contaminated" for a session or value that carries untrusted content.
- Instructions for agents live in this file. Do not add a `CLAUDE.md`.
