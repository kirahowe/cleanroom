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
	"runtime"
	"syscall"

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

		cmd := exec.CommandContext(ctx, *claude, claudeSessionArgs(serveArgv(self, policyFlag, stateDir), id, *resume != "")...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = c.stdin, c.stdout, c.stderr
		// Ctrl-C belongs to the conversation, which uses it to interrupt
		// a reply. This is a foreground command, so ctx ends only when
		// cleanroom itself is told to terminate, and the conversation is
		// then asked to do the same.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
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

// allowEntry is the "allowedMcpServers" entry for one command. It names the
// command exactly. An entry that allowed a server by its name would allow
// any server given that name.
func allowEntry(argv []string) string {
	b, _ := json.Marshal(argv)
	return `    { "serverCommand": ` + string(b) + ` }`
}

func claudeSettingsText(readerTools, serve []string) string {
	entry := allowEntry
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

// claudeConfigDir is the directory where Claude Code keeps the user's
// settings and login, or "" when there is no home directory to find it in.
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// notLoggedInHelp tells a person whose reader could not log in where Claude
// Code looked for the login. A reader command that moves Claude Code's
// directory itself is not accounted for.
func notLoggedInHelp(dir string) string {
	where := "its config directory"
	if dir != "" {
		where = dir
	}
	return "\nClaude Code is not logged in, or its login has expired, so the reader could not\n" +
		"ask its model. Nothing was read. The reader's session looks for a login in\n" + where + ".\n\n" +
		"To log in there, run \"claude\" and then /login. If your login is in another\n" +
		"directory, set CLAUDE_CONFIG_DIR to it when you run cleanroom. Then ask again.\n"
}

// claudeSettingsFiles lists the settings files Claude Code reads for a
// session started outside any project, as the reader's is: the user's, then
// the one an administrator manages. CLAUDE_CONFIG_DIR is Claude Code's own
// variable for moving the user's directory, and the reader's session
// inherits it.
func claudeSettingsFiles() []string {
	dir := claudeConfigDir()
	if dir == "" {
		return nil
	}
	managed := "/etc/claude-code/managed-settings.json"
	if runtime.GOOS == "darwin" {
		managed = "/Library/Application Support/ClaudeCode/managed-settings.json"
	}
	return []string{filepath.Join(dir, "settings.json"), managed}
}

// allowlistFile returns the first of the files that has an
// "allowedMcpServers" list, or "" when none does.
func allowlistFile(files []string) string {
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var settings map[string]json.RawMessage
		if json.Unmarshal(raw, &settings) != nil {
			continue
		}
		if _, ok := settings["allowedMcpServers"]; ok {
			return file
		}
	}
	return ""
}

// toolsBlockedHelp tells a person whose Claude Code settings kept the
// reader from its tools what to add, and to which file when it was found.
func toolsBlockedHelp(readerTools []string, file string) string {
	where := "your Claude Code settings.json"
	if file != "" {
		where = file
	}
	return "\nClaude Code would not start cleanroom's MCP server, so the reader could not\n" +
		"read anything. Your Claude Code settings list the MCP servers that are allowed,\n" +
		"and this one is not on the list.\n\n" +
		"To allow it, add this entry to \"allowedMcpServers\" in\n" + where + ":\n\n" +
		allowEntry(readerTools) + "\n\n" +
		"The entry allows that exact command and no other. Then ask again.\n"
}
