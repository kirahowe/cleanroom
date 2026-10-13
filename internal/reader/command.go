//go:build unix

package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/typed"
	"cleanroom/internal/version"
)

// Command is a Reader that runs an external program: a script, or a
// headless agent with a model behind it.
//
// The program gets its question on the command line and reaches the source
// through an MCP server that cleanroom describes in a config file. It
// prints its answer to stdout as JSON: {"answer": <value>}, or
// {"answer": null} when it has none.
//
// That MCP server is this process. For the length of one job it listens on
// a unix socket in a private directory and serves the job's Tools there.
// The config file names a command that only joins the reader to the socket,
// so the reader's calls travel over connections this process already holds
// and nothing is started or logged in to per question. When the job ends
// the socket is gone, and with it the reader's access.
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
	Dir  string
	// Pipe is the command that joins its stdin and stdout to a unix
	// socket: the cleanroom binary and "reader-tools". The socket's path
	// is given to it in SocketEnv, for each job.
	//
	// The path is kept out of the arguments so that the command is the
	// same for every job. An MCP client that only starts servers on an
	// allowlist of exact commands can then list this one.
	Pipe []string
	// Logf receives what the command writes to stderr.
	Logf func(format string, args ...any)
}

// SocketEnv names the environment variable that tells the Pipe command
// which socket to join.
const SocketEnv = "CLEANROOM_READER_SOCKET"

// ExitToolsBlocked is the status a reader exits with when its MCP client
// would not start the Pipe command, which leaves it nothing to read with.
// The status is how a reader reports this, because cleanroom shows nobody
// what a reader writes. A reader that exits with it falsely gets the person
// a setup hint that cleanroom wrote, and nothing else.
const ExitToolsBlocked = 78

// ErrToolsBlocked is what Answer returns for a reader that exited with
// ExitToolsBlocked.
var ErrToolsBlocked = errors.New("the reader could not start its tools")

// ExitNotLoggedIn is the status a reader exits with when the model it asks
// would not accept its login, so that it never got to read. Like
// ExitToolsBlocked, it earns the person a hint that cleanroom wrote.
const ExitNotLoggedIn = 77

// ErrNotLoggedIn is what Answer returns for a reader that exited with
// ExitNotLoggedIn.
var ErrNotLoggedIn = errors.New("the reader is not logged in to its model")

// Answer runs the command and returns the answer it printed.
func (c *Command) Answer(ctx context.Context, job Job) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "cleanroom-reader-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	server, err := toolServer(ctx, job.Tools)
	if err != nil {
		return nil, fmt.Errorf("listing the tools of %q: %w", job.Source, err)
	}
	// MkdirTemp made the directory for this user only, so only this user
	// can connect.
	socket := filepath.Join(tmp, "tools.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	serving, stop := context.WithCancel(context.Background())
	defer stop() // ends every connection a reader left open
	defer listener.Close()
	go serve(serving, listener, server)

	configPath := filepath.Join(tmp, "mcp.json")
	if err := os.WriteFile(configPath, c.mcpConfig(job, socket), 0o600); err != nil {
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
	err = cmd.Run()
	// Whatever the reader left running goes too, even after a clean exit.
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("the reader ran out of time")
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case ExitToolsBlocked:
				return nil, ErrToolsBlocked
			case ExitNotLoggedIn:
				return nil, ErrNotLoggedIn
			}
		}
		return nil, fmt.Errorf("the reader failed: %w", err)
	}
	return ParseOutput(out.buf.Bytes())
}

// mcpConfig is the file a command reader is pointed at. It uses the
// "mcpServers" layout that MCP clients share.
func (c *Command) mcpConfig(job Job, socket string) []byte {
	type server struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	b, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]server{job.Source: {
			Command: c.Pipe[0], Args: c.Pipe[1:], Env: map[string]string{SocketEnv: socket},
		}},
	}, "", "  ")
	return b
}

// toolServer builds the MCP server that a command reader connects to. It
// offers the reader's tools under their own names and passes every call to
// tools.Call, which is where the call is decided.
//
// A call to a tool that is not offered goes to tools.Call as well. It will
// be refused there, and that is how the attempt gets recorded: a reader
// reaching for a tool it was not given is the first sign that what it read
// has taken it over.
func toolServer(ctx context.Context, tools Tools) (*mcp.Server, error) {
	s := mcp.NewServer(&mcp.Implementation{Name: "cleanroom-reader-tools", Version: version.Version}, nil)
	if tools == nil {
		return s, nil
	}
	list, err := tools.List(ctx)
	if err != nil {
		return nil, err
	}
	offered := map[string]bool{}
	for _, tool := range list {
		offered[tool.Name] = true
	}
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if method != "tools/call" || !ok || offered[p.Name] {
				return next(ctx, method, req)
			}
			return tools.Call(ctx, p.Name, p.Arguments)
		}
	})
	for _, tool := range list {
		s.AddTool(&mcp.Tool{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Annotations: tool.Annotations,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return tools.Call(ctx, tool.Name, req.Params.Arguments)
		})
	}
	return s, nil
}

// serve answers each connection on l with s, until l is closed. Cancelling
// ctx ends the connections that are open.
func serve(ctx context.Context, l net.Listener, s *mcp.Server) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			s.Run(ctx, &mcp.IOTransport{Reader: conn, Writer: conn})
		}()
	}
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
	for _, line := range slices.Backward(lines) {
		if raw, ok := envelope(line); ok {
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
