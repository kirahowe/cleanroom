package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"cleanroom/internal/approval"
	"cleanroom/internal/core"
	"cleanroom/internal/reader"
)

// askCommand asks one question from the command line. The ask goes through
// the gateway like an agent's: it is decided and recorded, a reader does
// the reading, and the answer is checked.
//
// What is printed depends on who is asking. A person at a terminal sees
// the answer, free text included, on the same screen "cleanroom show"
// uses. Anything else gets what the ask tool would return: a checked value,
// or for free text only the id under which the person can read it.
func askCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	returns := fs.String("returns", "text", `the type of answer: text, date, datetime, number, money, boolean, or a JSON spec such as '{"type":"choice","choices":["yes","no"]}'`)
	source := fs.String("source", "", "the source to ask (default: the only one that has a reader)")
	return func(ctx context.Context) error {
		question := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if question == "" {
			return errors.New(`give the question, after any flags: cleanroom ask --returns date "When is the reunion?"`)
		}
		g, closeGateway, err := c.gateway("cli-" + newSessionID())
		if err != nil {
			return err
		}
		defer closeGateway()
		if *source == "" {
			if *source, err = onlySource(g.Policy); err != nil {
				return err
			}
		}

		if c.terminal {
			fmt.Fprintf(c.stderr, "Asking %s. A reader is searching and reading; this can take a minute.\n", *source)
		}
		res, err := g.Ask(ctx, *source, question, returnsSpec(*returns), false)
		if err != nil {
			return err
		}
		if !c.terminal {
			out, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(c.stdout, string(out))
			return nil
		}

		events, err := g.Log.Read()
		if err != nil {
			return err
		}
		screen, err := approval.AnswerScreen(events, res.ID)
		if err != nil {
			return err
		}
		fmt.Fprint(c.stdout, "\n"+screen)
		ans, ok := core.AnswerTo(events, res.ID)
		if ok && ans.Outcome == core.OutcomeNone && ans.Detail == reader.ErrToolsBlocked.Error() {
			self, err := selfPath()
			if err != nil {
				return err
			}
			fmt.Fprint(c.stdout, toolsBlockedHelp([]string{self, "reader-tools"}, allowlistFile(claudeSettingsFiles())))
			return nil
		}
		if ok && ans.Outcome == core.OutcomeNone && ans.Detail == reader.ErrNotLoggedIn.Error() {
			fmt.Fprint(c.stdout, notLoggedInHelp(claudeConfigDir()))
			return nil
		}
		fmt.Fprintf(c.stdout, "\nTo review reader logs: cleanroom log\n")
		if ok && ans.Outcome == core.OutcomeNone && strings.HasPrefix(ans.Detail, "the reader failed") {
			fmt.Fprint(c.stdout, readerFailedHelp)
		}
		if ok && ans.Outcome == core.OutcomeText {
			desk := &approval.Desk{Policy: g.Policy, Log: g.Log}
			return desk.MarkDelivered(res.ID, "cli")
		}
		return nil
	}
}

// readerFailedHelp is for a failure the reader gave no reason for.
const readerFailedHelp = `
The reader did not finish. Its own messages are in children.log, in the state
directory beside the policy.
`

// returnsSpec turns the --returns flag into the JSON the ask takes. A bare
// word such as date is a type name. Anything that starts like JSON is
// passed through for the gateway to validate.
func returnsSpec(flagValue string) json.RawMessage {
	v := strings.TrimSpace(flagValue)
	if strings.HasPrefix(v, "{") || strings.HasPrefix(v, `"`) {
		return json.RawMessage(v)
	}
	quoted, _ := json.Marshal(v)
	return quoted
}

// onlySource names the one upstream that has a reader.
func onlySource(p core.Policy) (string, error) {
	var sources []string
	for name, up := range p.Upstreams {
		if len(up.Reader) > 0 {
			sources = append(sources, name)
		}
	}
	switch len(sources) {
	case 0:
		return "", errors.New("the policy has no source with a reader to ask")
	case 1:
		return sources[0], nil
	}
	return "", errors.New("the policy has several sources; pick one with --source")
}
