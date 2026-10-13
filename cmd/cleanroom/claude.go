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
		stateDir := c.stateDir
		if stateDir != "" {
			if stateDir, err = filepath.Abs(stateDir); err != nil {
				return err
			}
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		id := *resume
		if id == "" {
			id = newUUID()
		}

		cmd := exec.Command(*claude, claudeSessionArgs(self, id, *resume != "", policyPath, stateDir)...)
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
// server but the one given here.
func claudeSessionArgs(self, id string, resume bool, policyPath, stateDir string) []string {
	serve := []string{"serve", "--session", id, "--policy", policyPath}
	if stateDir != "" {
		serve = append(serve, "--state", stateDir)
	}
	type server struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	mcpConfig, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]server{"cleanroom": {Command: self, Args: serve}},
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
