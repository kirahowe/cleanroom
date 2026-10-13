//go:build unix

package reader

// Adversarial tests for the command reader that pass. What got through is

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cleanroom/internal/typed"
)

// TestPlaceholdersAreSubstitutedOnce asks questions that are themselves
// placeholders. Substitution must not look at its own output, each argument
// must stay one argument, and the config path must not leak into another.
func TestPlaceholdersAreSubstitutedOnce(t *testing.T) {
	questions := []string{
		"{mcp_config}", "{returns}", "{source}", "{question}", "{{mcp_config}}", "{mcp_config}{returns}{source}",
		"a {mcp_config} b", "-- {mcp_config}", "--mcp-config=/etc/passwd", "-x", "", " ", "a b\tc\nd", "'; rm -rf / #",
		"$(id)", "`id`", "$HOME", "a\"b", `a\`, "{", "}", "{mcp_config", "é\u202e\u0000"[:5],
	}
	returns := []string{`"date"`, `{"type":"choice","choices":["{mcp_config}","{question}","{source}"]}`}
	const config = "/tmp/cleanroom-reader-1/mcp.json"
	argv := []string{"reader", "{question}", "{returns}", "{source}", "{mcp_config}", "q={question};r={returns};s={source}", "fixed"}
	for _, q := range questions {
		for _, r := range returns {
			got := substitute(argv, Job{Ask: 3, Source: "inbox", Question: q, Returns: json.RawMessage(r)}, config)
			want := []string{"reader", q, r, "inbox", config, "q=" + q + ";r=" + r + ";s=inbox", "fixed"}
			if !slices.Equal(got, want) {
				t.Errorf("question %q, returns %q:\n got %q\nwant %q", q, r, got, want)
			}
		}
	}
	if argv[1] != "{question}" {
		t.Error("substitute changed the policy's own command line")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestConfigFileIsGoneAndPrivate checks the temporary config: mode 0600 in
// a 0700 directory while the reader runs, removed afterwards.
func TestConfigFileIsGoneAndPrivate(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seen")
	c := &Command{
		Argv: sh(`stat -f '%Lp %N' "$1" "$(dirname "$1")" > "`+out+`" 2>/dev/null || stat -c '%a %n' "$1" "$(dirname "$1")" > "`+out+`"; echo '{"answer": 1}'`, "{mcp_config}"),
		Pipe: []string{"x"},
	}
	if _, err := c.Answer(context.Background(), Job{Ask: 1, Source: `we"ird\name`}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(mustRead(t, out)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "600 ") || !strings.HasPrefix(lines[1], "700 ") {
		t.Errorf("config file and directory modes: %q", lines)
	}
	path := strings.TrimPrefix(lines[0], "600 ")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the config file is still there: %v", err)
	}
}

// TestOutputFloodIsBounded has the reader print far more than the limit.
// Answer must refuse it without holding the flood in memory, which shows
// that the reader's stdout goes through the capped writer. A few megabytes
// are enough to tell: a buffer that kept them would allocate more than
// that, and the capped one stays under a hundred kilobytes.
func TestOutputFloodIsBounded(t *testing.T) {
	const flood = 4 << 20
	c := &Command{Argv: sh(`head -c ` + strconv.Itoa(flood) + ` /dev/zero; echo; echo '{"answer": true}'`), Pipe: []string{"x"}}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	raw, err := c.Answer(context.Background(), Job{Ask: 1, Source: "mail"})
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Errorf("a %d MB output gave the answer %q", flood>>20, raw)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > flood/4 {
		t.Errorf("Answer allocated %d kB while reading a %d MB flood", grew>>10, flood>>20)
	}
}

func TestCappedWriterKeepsOnlyTheLimit(t *testing.T) {
	var c capped
	for range 1000 {
		c.Write(bytes.Repeat([]byte("x"), 1000))
	}
	if c.buf.Len() != typed.MaxAnswerLen+1 {
		t.Errorf("capped kept %d bytes", c.buf.Len())
	}
}

// chainLength is where the reader in TestStoppingKillsAReaderThatKeepsForking
// stops by itself, so that a failing test leaves nothing running for long.
const chainLength = 400

// TestStoppingKillsAReaderThatKeepsForking stops a reader that is a chain
// of processes, each of which ignores hangup, holds the pipes, starts the
// next and waits for it. The chain is still growing when the job is
// stopped, which is the case one kill of the process group gets wrong: it
// misses a process that was being started as it went round, and the chain
// carries on from there.
//
// The reader says when the chain is under way by writing to a named pipe,
// so the test stops it then and waits for nothing.
func TestStoppingKillsAReaderThatKeepsForking(t *testing.T) {
	dir := t.TempDir()
	pids, ready := filepath.Join(dir, "pids"), filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	link := `
trap '' HUP TERM INT
echo $$ >> "` + pids + `"
[ "$1" = 5 ] && echo ready > "` + ready + `"
[ "$1" -lt ` + strconv.Itoa(chainLength) + ` ] && /bin/sh -c "$0" "$0" $(($1+1)) &
wait
`
	started := func() []int {
		var all []int
		for field := range strings.FieldsSeq(mustRead(t, pids)) {
			if pid, _ := strconv.Atoi(field); pid > 1 {
				all = append(all, pid)
			}
		}
		return all
	}
	t.Cleanup(func() {
		for _, pid := range started() {
			syscall.Kill(pid, syscall.SIGKILL) // pids this test started
		}
	})

	c := &Command{Argv: []string{"/bin/sh", "-c", link, link, "0"}, Pipe: []string{"x"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		os.ReadFile(ready) // returns once the reader has written
		cancel()
	}()
	if _, err := c.Answer(ctx, Job{Ask: 1, Source: "mail"}); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("err = %v, want an interruption", err)
	}

	links := started()
	if len(links) >= chainLength {
		t.Errorf("the chain ran to its end, %d links, after the job was stopped", len(links))
	}
	for _, pid := range links {
		if !gone(pid) {
			t.Errorf("process %d of the reader's group is still running", pid)
		}
	}
}

// gone reports whether a process has ended. A killed process whose parent
// died with it stays visible until the system collects it, so gone looks
// again until it has. The deadline only bounds a failing test.
func gone(pid int) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
	}
	return false
}

// FuzzParseOutputBounds adds to FuzzParseOutput: the answer is never
// longer than the limit and is bytes that were in the output.
func FuzzParseOutputBounds(f *testing.F) {
	f.Add([]byte(`{"answer": 1}`))
	f.Add([]byte(`{"answer": 1, "answer": "two"}`))
	f.Add([]byte("{\"answer\": 1}\r\n{\"ANSWER\": 2}\n\n"))
	f.Add([]byte("{\"answer\": \"a\nb\"}"))
	f.Add([]byte("\xef\xbb\xbf{\"answer\": 1}"))
	f.Add([]byte(`[{"answer": 1}]`))
	f.Add([]byte(`{"answer": 1}{"answer": 2}`))
	f.Fuzz(func(t *testing.T, out []byte) {
		raw, err := ParseOutput(out)
		if err != nil {
			if raw != nil {
				t.Fatalf("an error came with bytes %q", raw)
			}
			return
		}
		if len(raw) > typed.MaxAnswerLen || !json.Valid(raw) {
			t.Fatalf("ParseOutput returned %d bytes, valid=%v", len(raw), json.Valid(raw))
		}
		if !bytes.Contains(out, raw) {
			t.Fatalf("ParseOutput returned %q, which is not in the output %q", raw, out)
		}
	})
}
