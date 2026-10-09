//go:build unix

package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cleanroom/internal/typed"
)

// Command is a Reader that runs an external program: a script, or a
// headless agent with a model behind it.
//
// The program gets its question on the command line and reaches the source
// through an MCP server that cleanroom describes in a config file. It
// prints its answer to stdout as JSON: {"answer": <value>}, or
// {"answer": null} when it has none.
type Command struct {
	// Argv is the command, with placeholders that are replaced for each
	// job:
	//
	//	{mcp_config}  path of a JSON file with an "mcpServers" block
	//	              naming one server, the source's read-only tools
	//	{question}    the caller's question
	//	{returns}     the wanted type, as JSON
	//	{source}      the name of the source
	//
	// A placeholder is replaced inside one argument, once, and never
	// passes through a shell. The question is the caller's text, so give
	// it its own flag ("--question", "{question}") or put it after "--".
	// A bare "{question}" would let a question that starts with a dash
	// be read as an option of the reader program.
	Argv []string
	// Dir is the working directory for the command.
	Dir string
	// ToolServer is the command that serves a reader's tools over
	// stdio: the cleanroom binary and its reader-tools arguments. The
	// ask number is appended for each job.
	ToolServer []string
	// Logf receives what the command writes to stderr.
	Logf func(format string, args ...any)
}

// Answer runs the command and returns the answer it printed.
func (c *Command) Answer(ctx context.Context, job Job) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "cleanroom-reader-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	configPath := filepath.Join(tmp, "mcp.json")
	if err := os.WriteFile(configPath, c.mcpConfig(job), 0o600); err != nil {
		return nil, err
	}

	replace := strings.NewReplacer(
		"{mcp_config}", configPath,
		"{question}", job.Question,
		"{returns}", string(job.Returns),
		"{source}", job.Source,
	)
	argv := make([]string, len(c.Argv))
	for i, arg := range c.Argv {
		argv[i] = replace.Replace(arg)
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = c.Dir
	// Run the reader in its own process group and stop the whole group
	// when time runs out, so that anything the reader started goes too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var out capped
	cmd.Stdout = &out
	cmd.Stderr = &lineLogger{logf: c.Logf}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("the reader ran out of time")
		}
		return nil, fmt.Errorf("the reader failed: %w", err)
	}
	return ParseOutput(out.buf.Bytes())
}

// mcpConfig is the file a command reader is pointed at. It uses the
// "mcpServers" layout that MCP clients share.
func (c *Command) mcpConfig(job Job) []byte {
	type server struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	args := append(append([]string(nil), c.ToolServer[1:]...), "--ask", strconv.Itoa(job.Ask))
	b, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]server{job.Source: {Command: c.ToolServer[0], Args: args}},
	}, "", "  ")
	return b
}

// capped keeps the first bytes written to it and drops the rest, so a
// reader cannot make cleanroom hold an unbounded answer in memory.
type capped struct{ buf bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := typed.MaxAnswerLen + 1 - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// maxLogLine bounds one line of a reader's stderr.
const maxLogLine = 8 << 10

// lineLogger passes a reader's stderr to logf a line at a time. A line
// longer than maxLogLine is logged in pieces, so a reader that never prints
// a newline cannot make cleanroom hold its output.
type lineLogger struct {
	logf func(format string, args ...any)
	rest []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	if l.logf == nil {
		return len(p), nil
	}
	l.rest = append(l.rest, p...)
	for {
		line, rest, found := bytes.Cut(l.rest, []byte("\n"))
		if !found && len(l.rest) < maxLogLine {
			break
		}
		if !found {
			line, rest = l.rest[:maxLogLine], l.rest[maxLogLine:]
		}
		l.logf("reader: %s", string(line))
		l.rest = rest
	}
	return len(p), nil
}

// ParseOutput finds the answer in what a reader printed. It accepts the
// whole output as {"answer": ...}, or failing that the last line that has
// that form, so that a reader may print progress before its answer.
//
// The result is still untrusted. It is only the bytes to be type-checked.
func ParseOutput(out []byte) ([]byte, error) {
	if len(out) > typed.MaxAnswerLen {
		return nil, errors.New("the reader's output was too long")
	}
	if raw, ok := envelope(out); ok {
		return raw, nil
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if raw, ok := envelope(lines[i]); ok {
			return raw, nil
		}
	}
	return nil, errors.New(`the reader did not print {"answer": ...}`)
}

func envelope(b []byte) ([]byte, bool) {
	var env map[string]json.RawMessage
	if json.Unmarshal(b, &env) != nil {
		return nil, false
	}
	raw, ok := env["answer"]
	return raw, ok
}
