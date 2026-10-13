// Command claude-reader is a reader backed by a model. It answers a
// question by running a headless Claude Code session whose only tools are
// those of one source, reached through the MCP config cleanroom supplies.
// It prints {"answer": ...} for cleanroom to check.
//
// The session uses the Claude Code login of the user who runs it. What the
// reader reads from the source is sent to Anthropic.
//
// The user's Claude Code settings apply to the session. If they list the
// MCP servers that are allowed, the user has to add the reader's command.
//
// Like any reader it is untrusted. The flags in claudeArgs take away the
// session's other tools, but the process is not sandboxed. See "Limits" in
// the README.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"cleanroom/internal/reader"
	"cleanroom/internal/typed"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("claude-reader: ")
	var (
		configPath = flag.String("mcp-config", "", "MCP config file naming the source's server")
		question   = flag.String("question", "", "the question to answer")
		returns    = flag.String("returns", `"text"`, "the wanted type, as JSON")
		model      = flag.String("model", "opus", "Claude Code model alias or name")
		claude     = flag.String("claude", "claude", "the Claude Code executable")
	)
	flag.Parse()
	if flag.NArg() > 0 || *configPath == "" || strings.TrimSpace(*question) == "" {
		log.Fatal("usage: claude-reader --mcp-config FILE --question TEXT [--returns JSON] [--model NAME] [--claude PATH]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	answer, report, err := run(ctx, *claude, *model, *configPath, *question, *returns, time.Now())
	if report != "" {
		log.Print(report)
	}
	if err != nil {
		log.Print(err)
		os.Exit(exitStatus(err))
	}
	fmt.Println(string(answer))
}

// exitStatus is the status the reader exits with after err. A cause that
// cleanroom can explain to the person has a status of its own, because the
// status is all of a reader's failure that cleanroom shows anyone.
func exitStatus(err error) int {
	switch {
	case errors.Is(err, errToolsBlocked):
		return reader.ExitToolsBlocked
	case errors.Is(err, errNotLoggedIn):
		return reader.ExitNotLoggedIn
	}
	return 1
}

// run asks the model and returns the line to print. The report is a line
// for the log. It holds counts and no text from the model or the source.
func run(ctx context.Context, claude, model, configPath, question, returns string, now time.Time) (answer []byte, report string, err error) {
	spec, err := typed.ParseSpec(json.RawMessage(returns))
	if err != nil {
		return nil, "", err
	}
	source, server, err := sourceServer(configPath)
	if err != nil {
		return nil, "", err
	}
	schemaJSON, err := json.Marshal(answerSchema(spec))
	if err != nil {
		return nil, "", err
	}

	// Claude Code reads instruction files from its working directory and
	// the directories above it. An empty directory gives it none.
	tmp, err := os.MkdirTemp("", "claude-reader-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "cwd")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, "", err
	}
	renamed, err := json.Marshal(map[string]any{"mcpServers": map[string]json.RawMessage{serverName: server}})
	if err != nil {
		return nil, "", err
	}
	sessionConfig := filepath.Join(tmp, "mcp.json")
	if err := os.WriteFile(sessionConfig, renamed, 0o600); err != nil {
		return nil, "", err
	}

	system := systemPrompt(source, now.Format("Monday, January 2, 2006"))
	cmd := exec.CommandContext(ctx, claude, claudeArgs(model, sessionConfig, system, string(schemaJSON))...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(question)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if strings.Contains(stderr.String(), "MCP server blocked") {
		return nil, "", fmt.Errorf("%w.\n%s", errToolsBlocked, blockedHelp(server))
	}

	result, parseErr := parseResult(stdout.Bytes())
	switch {
	case parseErr != nil && runErr != nil:
		return nil, "", fmt.Errorf("running %s: %w", claude, runErr)
	case parseErr != nil:
		return nil, "", parseErr
	}
	report = fmt.Sprintf("%s, %d turns, %.1fs, $%.4f", result.Subtype, result.Turns, float64(result.DurationMS)/1000, result.CostUSD)
	if result.notLoggedIn() {
		return nil, report, errNotLoggedIn
	}
	if result.IsError || result.Output == nil {
		return nil, report, errors.New("the session ended without a structured answer")
	}
	line, err := json.Marshal(map[string]json.RawMessage{"answer": result.Output.Answer})
	return line, report, err
}

var errToolsBlocked = errors.New("Claude Code did not load the reader's tools, because its settings list the MCP servers that are allowed")

// blockedHelp says what to do when Claude Code's settings keep the session
// from loading the source's tools. It names the server's exact command,
// which is the same for every question.
func blockedHelp(server json.RawMessage) string {
	var s struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	json.Unmarshal(server, &s) // a server cleanroom wrote; an empty entry is still a usable hint
	argv, _ := json.Marshal(append([]string{s.Command}, s.Args...))
	return "To allow them, add this entry to \"allowedMcpServers\" in Claude Code's settings.json:\n\n" +
		`    { "serverCommand": ` + string(argv) + " }"
}

// sourceServer returns the one server in the config file cleanroom wrote:
// the source's name, and how to start its server.
func sourceServer(configPath string) (source string, server json.RawMessage, err error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", nil, err
	}
	var config struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", nil, fmt.Errorf("%s: %w", configPath, err)
	}
	names := slices.Collect(maps.Keys(config.MCPServers))
	if len(names) != 1 {
		return "", nil, fmt.Errorf("%s names %d MCP servers, want one", configPath, len(names))
	}
	return names[0], config.MCPServers[names[0]], nil
}

// result is the part of Claude Code's --output-format json that is used.
type result struct {
	Subtype        string  `json:"subtype"`
	IsError        bool    `json:"is_error"`
	TerminalReason string  `json:"terminal_reason"`
	Result         string  `json:"result"`
	Turns          int     `json:"num_turns"`
	DurationMS     int     `json:"duration_ms"`
	CostUSD        float64 `json:"total_cost_usd"`
	Output         *struct {
		Answer json.RawMessage `json:"answer"`
	} `json:"structured_output"`
}

var errNotLoggedIn = errors.New("Claude Code is not logged in, or its login has expired")

// notLoggedIn reports whether the session ended because Anthropic refused
// the login. Claude Code 2.1 has no field for this. It ends such a session
// with an API error whose text is one of the two matched here: the first
// for a config directory that was never logged in, the second for a login
// that expired.
func (r result) notLoggedIn() bool {
	return r.IsError && r.TerminalReason == "api_error" &&
		(strings.HasPrefix(r.Result, "Not logged in") || strings.HasPrefix(r.Result, "Failed to authenticate"))
}

func parseResult(stdout []byte) (result, error) {
	var r result
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &r); err != nil {
		return result{}, errors.New("Claude Code did not print a JSON result")
	}
	if r.Output != nil && r.Output.Answer == nil {
		r.Output = nil
	}
	return r, nil
}
