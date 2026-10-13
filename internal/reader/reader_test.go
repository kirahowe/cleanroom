//go:build unix

package reader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string // empty means an error
	}{
		{"just the answer", `{"answer": "2027-07-17"}`, `"2027-07-17"`},
		{"null answer", `{"answer": null}`, `null`},
		{"object answer", `{"answer": {"amount": 1, "currency": "CAD"}}`, `{"amount": 1, "currency": "CAD"}`},
		{"progress, then the answer", "searching...\nreading 4 messages\n{\"answer\": 3}\n", `3`},
		{"last answer wins", "{\"answer\": 1}\n{\"answer\": 2}", `2`},
		{"no answer key", `{"result": 3}`, ""},
		{"prose", `The date is July 17.`, ""},
		{"empty", ``, ""},
		{"too long", `{"answer": "` + strings.Repeat("x", 70<<10) + `"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOutput([]byte(tt.out))
			if (err == nil) != (tt.want != "") || string(got) != tt.want {
				t.Errorf("ParseOutput = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// sh is a reader's command line that runs a shell script. The script is
// passed inline: a script file would be a new executable each time, and
// starting one of those is slow.
func sh(body string, args ...string) []string {
	return append([]string{"/bin/sh", "-c", body, "reader"}, args...)
}

func TestCommandSubstitutesAndReadsTheAnswer(t *testing.T) {
	// The script prints its arguments and the config file to stderr, and
	// an answer to stdout.
	c := &Command{
		Argv: sh(`
echo "source=$1 question=$2 returns=$3" >&2
tr -d ' \n' < "$4" >&2; echo >&2
echo '{"answer": true}'
`, "{source}", "{question}", "{returns}", "{mcp_config}"),
		Pipe: []string{"/bin/cleanroom", "reader-tools"},
	}
	var logged []string
	c.Logf = func(format string, args ...any) { logged = append(logged, args[0].(string)) }

	raw, err := c.Answer(context.Background(), Job{
		Ask: 7, Source: "mail", Question: "Is it $HOME; rm -rf?", Returns: json.RawMessage(`"boolean"`),
	})
	if err != nil || string(raw) != "true" {
		t.Fatalf("Answer = %q, %v", raw, err)
	}
	stderr := strings.Join(logged, "\n")
	// The question arrives as one argument, never through a shell.
	if !strings.Contains(stderr, `source=mail question=Is it $HOME; rm -rf? returns="boolean"`) {
		t.Errorf("arguments were not substituted as expected:\n%s", stderr)
	}
	// The config names the pipe command and a socket, and nothing about
	// the policy, the state or the ask.
	config := regexp.MustCompile(`\{"mcpServers":\{"mail":\{"command":"/bin/cleanroom","args":\["reader-tools"\],"env":\{"CLEANROOM_READER_SOCKET":"(/[^"]+/tools\.sock)"\}\}\}\}`)
	m := config.FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("mcp config:\n%s\nwant it to match %s", stderr, config)
	}
	if _, err := os.Stat(m[1]); err == nil {
		t.Errorf("the socket %s is still there after the reader finished", m[1])
	}
}

// fakeTools is the tools of a source, as the gateway would hand them to a
// reader. It records the calls that reach it.
type fakeTools struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeTools) List(context.Context) ([]*mcp.Tool, error) {
	return []*mcp.Tool{{Name: "search", Description: "Search.", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}

func (f *fakeTools) Call(_ context.Context, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tool+" "+string(args))
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found"}}}, nil
}

// TestToolsAreServedOnTheSocket connects the way a command reader does, as
// an MCP client on the socket, and checks that the calls arrive at the
// job's Tools. Nothing else is started to serve them.
func TestToolsAreServedOnTheSocket(t *testing.T) {
	ctx := context.Background()
	tools := &fakeTools{}
	server, err := toolServer(ctx, tools)
	if err != nil {
		t.Fatal(err)
	}
	// Not t.TempDir: its path can be too long for a socket.
	dir, err := os.MkdirTemp("", "cleanroom-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "tools.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	serving, stop := context.WithCancel(ctx)
	defer stop()
	go serve(serving, listener, server)

	// Two readers' worth of connections, one after the other and both
	// served by the same process.
	for i := range 2 {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "reader", Version: "0"}, nil).
			Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := cs.ListTools(ctx, nil)
		if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "search" {
			t.Fatalf("connection %d: tools = %+v, %v", i, listed, err)
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"query": "x"}})
		if err != nil || res.IsError {
			t.Fatalf("connection %d: search = %+v, %v", i, res, err)
		}
		if i == 0 {
			// A tool that was not offered still reaches Tools.Call,
			// where the gateway refuses and records it.
			if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "send", Arguments: map[string]any{"to": "eve@evil.example"}}); err != nil {
				t.Fatalf("calling a tool that was not offered: %v", err)
			}
			cs.Close()
			continue
		}
		// The job ends with this connection still open. It must stop
		// working.
		stop()
		listener.Close()
		if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "search"}); err == nil {
			t.Error("a call on a connection left open after the job ended still worked")
		}
	}
	want := []string{`search {"query":"x"}`, `send {"to":"eve@evil.example"}`, `search {"query":"x"}`}
	if !slices.Equal(tools.calls, want) {
		t.Errorf("calls that reached the tools: %q, want %q", tools.calls, want)
	}
}

// The reader's exit status reaches failure, which names the cause.
func TestCommandReportsHowTheReaderExited(t *testing.T) {
	c := &Command{Argv: sh(fmt.Sprintf(`echo '{"answer": 1}'; exit %d`, ExitToolsBlocked)), Pipe: []string{"x"}}
	if raw, err := c.Answer(context.Background(), Job{Ask: 1, Source: "mail"}); !errors.Is(err, ErrToolsBlocked) {
		t.Errorf("Answer = %q, %v; want ErrToolsBlocked", raw, err)
	}
}

func TestFailure(t *testing.T) {
	crashed := errors.New("exit status 3")
	for _, tc := range []struct {
		name   string
		ctxErr error
		exit   int
		want   string
	}{
		{"an ordinary failure", nil, 3, "the reader failed: exit status 3"},
		{"a reader that never started", nil, -1, "the reader failed: exit status 3"},
		{"blocked tools", nil, ExitToolsBlocked, ErrToolsBlocked.Error()},
		{"no login", nil, ExitNotLoggedIn, ErrNotLoggedIn.Error()},
		{"the deadline passed", context.DeadlineExceeded, -1, "the reader ran out of time"},
		{"the caller stopped the job", context.Canceled, -1, "the reader was interrupted"},
		// A stopped job's reader may leave any status behind.
		{"stopped, with a status that has a meaning", context.Canceled, ExitToolsBlocked, "the reader was interrupted"},
	} {
		if got := failure(tc.ctxErr, tc.exit, crashed).Error(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if err := failure(nil, ExitToolsBlocked, crashed); !errors.Is(err, ErrToolsBlocked) {
		t.Errorf("blocked tools: %v is not ErrToolsBlocked", err)
	}
}

// FuzzParseOutput checks that no reader output can crash the parser, and
// that whatever it returns is valid JSON taken from the output.
func FuzzParseOutput(f *testing.F) {
	f.Add([]byte(`{"answer": 1}`))
	f.Add([]byte("noise\n{\"answer\": {\"a\": [1,2]}}\n"))
	f.Add([]byte(`{"answer": `))
	f.Fuzz(func(t *testing.T, out []byte) {
		raw, err := ParseOutput(out)
		if err != nil {
			return
		}
		if !json.Valid(raw) {
			t.Fatalf("ParseOutput returned invalid JSON %q", raw)
		}
	})
}
