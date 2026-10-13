// Command claude-reader is a reader backed by a model. It answers a
// question by running a headless Claude Code session whose only tools are
// those of one source, reached through the MCP config cleanroom supplies.
// It prints {"answer": ...} for cleanroom to check.
//
// The session uses the Claude Code login of the user who runs it. What the
// reader reads from the source is sent to Anthropic.
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
	"slices"
	"strings"
	"time"

	"cleanroom/internal/typed"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("claude-reader: ")
	var (
		configPath = flag.String("mcp-config", "", "MCP config file naming the source's server")
		question   = flag.String("question", "", "the question to answer")
		returns    = flag.String("returns", `"text"`, "the wanted type, as JSON")
		model      = flag.String("model", "sonnet", "Claude Code model alias or name")
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
		log.Fatal(err)
	}
	fmt.Println(string(answer))
}

// run asks the model and returns the line to print. The report is a line
// for the log. It holds counts and no text from the model or the source.
func run(ctx context.Context, claude, model, configPath, question, returns string, now time.Time) (answer []byte, report string, err error) {
	spec, err := typed.ParseSpec(json.RawMessage(returns))
	if err != nil {
		return nil, "", err
	}
	source, err := sourceName(configPath)
	if err != nil {
		return nil, "", err
	}
	schemaJSON, err := json.Marshal(answerSchema(spec))
	if err != nil {
		return nil, "", err
	}

	// Claude Code reads instruction files from its working directory and
	// the directories above it. An empty directory gives it none.
	dir, err := os.MkdirTemp("", "claude-reader-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)

	system := systemPrompt(source, now.Format("Monday, January 2, 2006"))
	cmd := exec.CommandContext(ctx, claude, claudeArgs(model, configPath, source, system, string(schemaJSON))...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(question)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	runErr := cmd.Run()

	result, parseErr := parseResult(stdout.Bytes())
	switch {
	case parseErr != nil && runErr != nil:
		return nil, "", fmt.Errorf("running %s: %w", claude, runErr)
	case parseErr != nil:
		return nil, "", parseErr
	}
	report = fmt.Sprintf("%s, %d turns, %.1fs, $%.4f", result.Subtype, result.Turns, float64(result.DurationMS)/1000, result.CostUSD)
	if result.IsError || result.Output == nil {
		return nil, report, errors.New("the session ended without a structured answer")
	}
	line, err := json.Marshal(map[string]json.RawMessage{"answer": result.Output.Answer})
	return line, report, err
}

// sourceName returns the name of the one server in the config file. Claude
// Code names a server's tools after it.
func sourceName(configPath string) (string, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var config struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", fmt.Errorf("%s: %w", configPath, err)
	}
	names := slices.Collect(maps.Keys(config.MCPServers))
	if len(names) != 1 {
		return "", fmt.Errorf("%s names %d MCP servers, want one", configPath, len(names))
	}
	return names[0], nil
}

// result is the part of Claude Code's --output-format json that is used.
type result struct {
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"is_error"`
	Turns      int     `json:"num_turns"`
	DurationMS int     `json:"duration_ms"`
	CostUSD    float64 `json:"total_cost_usd"`
	Output     *struct {
		Answer json.RawMessage `json:"answer"`
	} `json:"structured_output"`
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
