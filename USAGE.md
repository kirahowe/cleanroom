# Usage reference

See the [README](README.md#try-it) for the demo walkthrough. Run commands
from the repository root unless cleanroom is on your `PATH`.

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

The skill guides the session to query mail through `ask`.
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

## Call the server with MCP Inspector

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
