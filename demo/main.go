// Command demo plays the scenes from the README against real processes: a
// real Mailpit, the real mail and calendar servers, and the real cleanroom
// server, with a scripted agent making the tool calls a model would make.
// It needs no API key. Every scene checks its result and the command exits
// non-zero if any check fails.
//
// Run it with "make demo".
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/config"
	"cleanroom/internal/core"
	"cleanroom/internal/eventlog"
)

const (
	mailpitHTTP = "127.0.0.1:18025"
	mailpitSMTP = "127.0.0.1:11025"
	session     = "chief-of-staff"
)

func main() {
	root := flag.String("root", ".", "the repository root")
	mailpit := flag.String("mailpit", "", "path to the mailpit binary")
	flag.Parse()

	d, err := newDemo(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
	stop, err := d.startMailpit(*mailpit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
	d.run()
	stop()
	if d.failed > 0 {
		fmt.Printf("\n%d checks FAILED\n", d.failed)
		os.Exit(1)
	}
	fmt.Printf("\nAll %d checks passed.\n", d.passed)
	fmt.Printf("The calendar the agent wrote: %s\n", filepath.Join(d.state, "calendar", "calendar.ics"))
	fmt.Printf("The event log behind every decision: %s\n", filepath.Join(d.state, eventlog.FileName))
}

type demo struct {
	root   string // repository root, absolute
	policy string // path of the demo policy
	state  string // state directory, wiped at the start of each run
	cfg    *config.Config
	log    *eventlog.Log
	// ownInbox is true when the demo started Mailpit and seeded it, so
	// that every message in the inbox is one of the demo's.
	ownInbox bool

	passed, failed int
}

func newDemo(root string) (*demo, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	d := &demo{
		root:   root,
		policy: filepath.Join(root, "demo", "policy.jsonc"),
		state:  filepath.Join(root, "demo", ".cleanroom"),
	}
	for _, bin := range []string{"cleanroom", "mail-source", "calendar-source", "demo-reader", "seed"} {
		if _, err := os.Stat(filepath.Join(root, "bin", bin)); err != nil {
			return nil, fmt.Errorf("bin/%s is missing: run `make build` first", bin)
		}
	}
	// Each run starts from nothing, so that the sessions are new.
	if err := os.RemoveAll(d.state); err != nil {
		return nil, err
	}
	if d.cfg, err = config.Load(d.policy); err != nil {
		return nil, err
	}
	if d.log, err = eventlog.Open(d.state); err != nil {
		return nil, err
	}
	return d, nil
}

// startMailpit makes sure a Mailpit is listening on the demo ports. If one
// already is (from `make inbox`), the demo uses its inbox as it stands,
// including any message a reviewer added. Otherwise it starts one, seeds
// it, and stops it at the end.
func (d *demo) startMailpit(bin string) (stop func(), err error) {
	if mailpitUp() {
		fmt.Printf("Using the Mailpit already running at http://%s, with its inbox as it is.\n", mailpitHTTP)
		return func() {}, nil
	}
	if bin == "" {
		bin = filepath.Join(d.root, ".tools", "mailpit")
		if path, err := exec.LookPath("mailpit"); err == nil {
			bin = path
		}
	}
	cmd := exec.Command(bin, "--listen", mailpitHTTP, "--smtp", mailpitSMTP, "--quiet")
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting Mailpit (%s): %w; run `make mailpit` to download it", bin, err)
	}
	stop = func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
	for deadline := time.Now().Add(10 * time.Second); !mailpitUp(); {
		if time.Now().After(deadline) {
			stop()
			return nil, fmt.Errorf("no Mailpit answered on %s within 10 seconds", mailpitHTTP)
		}
		time.Sleep(100 * time.Millisecond)
	}
	seed := exec.Command(filepath.Join(d.root, "bin", "seed"),
		"--api", "http://"+mailpitHTTP, "--smtp", mailpitSMTP,
		"--dir", filepath.Join(d.root, "demo", "inbox"), "--reset")
	if out, err := seed.CombinedOutput(); err != nil {
		stop()
		return nil, fmt.Errorf("seeding the inbox: %v\n%s", err, out)
	}
	d.ownInbox = true
	fmt.Printf("Started Mailpit at http://%s and seeded it from demo/inbox.\n", mailpitHTTP)
	return stop, nil
}

func mailpitUp() bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + mailpitHTTP + "/api/v1/info")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func sentTo(addr string) (int, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + mailpitHTTP + "/api/v1/search?query=to:" + addr)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// "total" is the size of the whole mailbox. The matches are counted
	// in "messages_count".
	var found struct {
		Matches int `json:"messages_count"`
	}
	err = json.NewDecoder(resp.Body).Decode(&found)
	return found.Matches, err
}

// agent is the scripted agent: an MCP client connected to a cleanroom
// server it started, as any MCP client would.
type agent struct {
	d      *demo
	cs     *mcp.ClientSession
	stderr *lockedBuffer // what the server and its readers logged
}

// startAgent launches a cleanroom server for the demo session and connects
// to it. Extra environment variables reach the server and its readers.
func (d *demo) startAgent(env ...string) *agent {
	cmd := exec.Command(filepath.Join(d.root, "bin", "cleanroom"), "serve",
		"--policy", d.policy, "--state", d.state, "--session", session)
	cmd.Env = append(os.Environ(), env...)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "scripted-agent", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: starting cleanroom: %v\n%s", err, stderr.String())
		os.Exit(1)
	}
	return &agent{d: d, cs: cs, stderr: stderr}
}

func (a *agent) close() { a.cs.Close() }

// call makes a tool call, prints it, and decodes the structured result
// into out.
func (a *agent) call(tool string, args map[string]any, out any) string {
	argText := []byte("{}")
	if args != nil {
		argText, _ = json.Marshal(args)
	}
	fmt.Printf("  agent   %s %s\n", tool, argText)
	res, err := a.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		a.d.check(false, "%s: %v", tool, err)
		return ""
	}
	var text strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			text.WriteString(t.Text)
		}
	}
	if out != nil && res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		json.Unmarshal(b, out)
	}
	return text.String()
}

// askResult mirrors what the ask tool returns.
type askResult struct {
	Status string          `json:"status"`
	ID     int             `json:"id"`
	Value  json.RawMessage `json:"value"`
	Text   string          `json:"text"`
}

// ask puts a question to the mail source and prints what came of it.
func (a *agent) ask(question string, returns any, acceptContamination bool) askResult {
	args := map[string]any{"source": "mail", "question": question, "returns": returns}
	if acceptContamination {
		args["accept_contamination"] = true
	}
	var res askResult
	a.call("ask", args, &res)
	a.d.showReader(res.ID)
	switch res.Status {
	case "answered":
		if res.Value != nil {
			fmt.Printf("  agent   received the value %s\n", res.Value)
		} else {
			fmt.Printf("  agent   received the text %q\n", res.Text)
		}
	default:
		fmt.Printf("  agent   received status %q and nothing else\n", res.Status)
	}
	return res
}

// labels reads the session's labels from the log, as the status tool does.
func (d *demo) labels() core.Labels {
	log, err := d.log.Read()
	if err != nil {
		d.check(false, "reading the log: %v", err)
	}
	return core.StateOf(d.cfg.Policy, log, session).Labels
}

// showReader prints what the reader of an ask did, taken from the log.
func (d *demo) showReader(askSeq int) {
	log, err := d.log.Read()
	if err != nil || askSeq == 0 {
		return
	}
	counts := map[string]int{}
	var order []string
	for _, e := range log {
		if e.Kind != core.KindCall || e.Call.Call.Session != core.ReaderSession(askSeq) {
			continue
		}
		name := fmt.Sprintf("%s.%s (%s)", e.Call.Call.Upstream, e.Call.Call.Tool, e.Call.Decision.Verdict)
		if e.Call.Decision.Verdict == core.Refuse {
			// A refused call can name any tool the mail told the
			// reader to try, so the name is not printed here.
			name = "a tool it was not given (refused and recorded)"
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}
	for _, name := range order {
		fmt.Printf("  reader  %d x %s\n", counts[name], name)
	}
	if ans, ok := core.AnswerTo(log, askSeq); ok && ans.Outcome == core.OutcomeNone {
		fmt.Printf("  gateway recorded no answer: %s\n", ans.Detail)
	}
}

func (d *demo) check(ok bool, format string, args ...any) {
	mark := "ok   "
	if ok {
		d.passed++
	} else {
		d.failed++
		mark = "FAIL "
	}
	fmt.Printf("  %s   %s\n", mark, fmt.Sprintf(format, args...))
}

func scene(n int, title string) {
	fmt.Printf("\nScene %d. %s\n", n, title)
}

// lockedBuffer collects a child's stderr while the demo reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
