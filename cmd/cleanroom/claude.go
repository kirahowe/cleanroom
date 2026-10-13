package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"cleanroom/internal/config"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// claudeCommand starts a Claude Code conversation whose only tools are
// cleanroom's. That is the condition under which cleanroom's rules cover
// everything the agent does: with a shell or another MCP server beside it,
// the agent could go around the gateway.
//
// The conversation and the cleanroom session share one id. Resuming with
// that id brings back both, so a conversation that became contaminated is
// still contaminated when it continues.
func claudeCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	resume := fs.String("resume", "", "id of a conversation this command started earlier")
	claude := fs.String("claude", "claude", "the Claude Code executable")
	return func(ctx context.Context) error {
		if !c.terminal {
			return errNeedsTerminal
		}
		if *resume != "" && !uuidPattern.MatchString(*resume) {
			return errors.New("--resume takes the id this command printed when the conversation ended")
		}
		policyPath, err := c.resolvedPolicyPath()
		if err != nil {
			return err
		}
		if _, err := config.Load(policyPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("no policy at %s; write one with cleanroom init", policyPath)
			}
			return err
		}
		policyFlag, stateDir, err := c.absoluteFlags()
		if err != nil {
			return err
		}
		self, err := selfPath()
		if err != nil {
			return err
		}
		id := *resume
		if id == "" {
			id = newUUID()
		}

		cmd := exec.Command(*claude, claudeSessionArgs(serveArgv(self, policyFlag, stateDir), id, *resume != "")...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = c.stdin, c.stdout, c.stderr
		// Not tied to ctx: Ctrl-C belongs to the conversation, which
		// uses it to interrupt a reply.
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("could not start Claude Code: %w", err)
		}
		runErr := cmd.Wait()
		fmt.Fprint(c.stderr, resumeHint(id, c.policyPath, c.stateDir))
		return runErr
	}
}

// claudeSessionArgs is the Claude Code command line for a conversation
// that has cleanroom and nothing else: no built-in tools, and no MCP
// server but the one given here. The session id reaches the server through
// its environment, which leaves serve's command line the same every time.
func claudeSessionArgs(serve []string, id string, resume bool) []string {
	type server struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	mcpConfig, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]server{"cleanroom": {
			Command: serve[0], Args: serve[1:], Env: map[string]string{sessionEnv: id},
		}},
	})
	conversation := []string{"--session-id", id}
	if resume {
		conversation = []string{"--resume", id}
	}
	return append(conversation,
		"--tools", "",
		"--strict-mcp-config", "--mcp-config", string(mcpConfig),
	)
}

// absoluteFlags returns --policy and --state as absolute paths, each empty
// when the flag was not given.
func (c *common) absoluteFlags() (policyFlag, stateDir string, err error) {
	if c.policyPath != "" {
		if policyFlag, err = filepath.Abs(c.policyPath); err != nil {
			return "", "", err
		}
	}
	if c.stateDir != "" {
		if stateDir, err = filepath.Abs(c.stateDir); err != nil {
			return "", "", err
		}
	}
	return policyFlag, stateDir, nil
}

// claudeSettingsCommand prints what to add to Claude Code's settings so
// that it may start cleanroom, for a user whose settings list the MCP
// servers that are allowed.
func claudeSettingsCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		policyFlag, stateDir, err := c.absoluteFlags()
		if err != nil {
			return err
		}
		self, err := selfPath()
		if err != nil {
			return err
		}
		fmt.Fprint(c.stdout, claudeSettingsText([]string{self, "reader-tools"}, serveArgv(self, policyFlag, stateDir)))
		return nil
	}
}

// claudeSettingsText names each command exactly. An entry that allowed a
// server by its name would allow any server given that name.
func claudeSettingsText(readerTools, serve []string) string {
	entry := func(argv []string) string {
		b, _ := json.Marshal(argv)
		return `    { "serverCommand": ` + string(b) + ` }`
	}
	return "Claude Code starts cleanroom as an MCP server in two places. If your Claude Code\n" +
		"settings have an \"allowedMcpServers\" list, add these entries to it:\n\n" +
		entry(readerTools) + ",\n" + entry(serve) + "\n\n" +
		"The first is the reader behind \"cleanroom ask\". The second is \"cleanroom claude\",\n" +
		"and depends on the --policy and --state you run it with. Each entry allows that\n" +
		"exact command and no other. Settings without the list need no change.\n"
}

func resumeHint(id, policyFlag, stateFlag string) string {
	command := "cleanroom claude --resume " + id
	if policyFlag != "" {
		command += " --policy " + policyFlag
	}
	if stateFlag != "" {
		command += " --state " + stateFlag
	}
	return "\nTo continue this conversation with cleanroom still in place:\n\n    " + command + "\n\n" +
		"Do not use \"claude --resume\" for it. That would bring the conversation back\n" +
		"with Claude Code's own tools, outside cleanroom's rules.\n"
}

// newUUID returns a random (version 4) UUID, the form Claude Code requires
// of a session id.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) // never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
