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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cleanroom/internal/typed"
)

// argDump is a reader that writes each of its arguments to a file, one per
// file, so a test can see exactly what argv it was given.
func argDump(t *testing.T) (argv0, dir string) {
	dir = t.TempDir()
	return script(t, `
i=0
for a in "$@"; do i=$((i+1)); printf '%s' "$a" > "`+dir+`/arg$i"; done
echo $i > "`+dir+`/count"
echo '{"answer": true}'
`), dir
}

func readArg(t *testing.T, dir string, i int) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "arg"+strconv.Itoa(i)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPlaceholdersAreSubstitutedOnce asks questions that are themselves
// placeholders. Substitution must not look at its own output, the question
// must stay one argument, and the config path must not leak into another.
func TestPlaceholdersAreSubstitutedOnce(t *testing.T) {
	questions := []string{
		"{mcp_config}", "{returns}", "{source}", "{question}", "{{mcp_config}}", "{mcp_config}{returns}{source}",
		"a {mcp_config} b", "-- {mcp_config}", "--mcp-config=/etc/passwd", "-x", "", " ", "a b\tc\nd", "'; rm -rf / #",
		"$(id)", "`id`", "$HOME", "a\"b", `a\`, "{", "}", "{mcp_config", "é\u202e\u0000"[:5],
	}
	returns := []string{`"date"`, `{"type":"choice","choices":["{mcp_config}","{question}","{source}"]}`}
	argv0, dir := argDump(t)
	for _, q := range questions {
		for _, r := range returns {
			for i := 1; i <= 6; i++ {
				os.Remove(filepath.Join(dir, "arg"+strconv.Itoa(i)))
			}
			c := &Command{
				Argv: []string{argv0, "{question}", "{returns}", "{source}", "{mcp_config}", "q={question};r={returns};s={source}", "fixed"},
				Pipe: []string{"/bin/cleanroom", "reader-tools"},
			}
			if _, err := c.Answer(context.Background(), Job{Ask: 3, Source: "inbox", Question: q, Returns: json.RawMessage(r)}); err != nil {
				t.Fatalf("question %q: %v", q, err)
			}
			if n := strings.TrimSpace(mustRead(t, filepath.Join(dir, "count"))); n != "6" {
				t.Fatalf("question %q: the reader got %s arguments, want 6", q, n)
			}
			config := readArg(t, dir, 4)
			if !strings.HasSuffix(config, "mcp.json") || !filepath.IsAbs(config) {
				t.Fatalf("question %q: config argument is %q", q, config)
			}
			if got := readArg(t, dir, 1); got != q {
				t.Errorf("question %q arrived as %q", q, got)
			}
			if got := readArg(t, dir, 2); got != r {
				t.Errorf("question %q: returns %q arrived as %q", q, r, got)
			}
			if got := readArg(t, dir, 3); got != "inbox" {
				t.Errorf("question %q: source arrived as %q", q, got)
			}
			if got, want := readArg(t, dir, 5), "q="+q+";r="+r+";s=inbox"; got != want {
				t.Errorf("question %q: combined argument %q, want %q", q, got, want)
			}
			if readArg(t, dir, 6) != "fixed" {
				t.Errorf("question %q moved a later argument", q)
			}
			// The config path appears only where the policy put it.
			for _, i := range []int{1, 2, 3, 5} {
				if strings.Contains(readArg(t, dir, i), config) {
					t.Errorf("question %q: the config path leaked into argument %d", q, i)
				}
			}
		}
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
		Argv: []string{script(t, `stat -f '%Lp %N' "$1" "$(dirname "$1")" > "`+out+`" 2>/dev/null || stat -c '%a %n' "$1" "$(dirname "$1")" > "`+out+`"; echo '{"answer": 1}'`), "{mcp_config}"},
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
// Answer must fail, quickly, without holding the flood in memory.
func TestOutputFloodIsBounded(t *testing.T) {
	c := &Command{Argv: []string{script(t, `head -c 100000000 /dev/zero | tr '\0' 'x'; echo; echo '{"answer": true}'`)}, Pipe: []string{"x"}}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	raw, err := c.Answer(context.Background(), Job{Ask: 1, Source: "mail"})
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Errorf("a 100 MB output gave the answer %q", raw)
	}
	if grew := int64(after.TotalAlloc) - int64(before.TotalAlloc); grew > 64<<20 {
		t.Errorf("Answer allocated %d MB while reading the flood", grew>>20)
	}
}

func TestCappedWriterKeepsOnlyTheLimit(t *testing.T) {
	var c capped
	for i := 0; i < 1000; i++ {
		c.Write(bytes.Repeat([]byte("x"), 1000))
	}
	if c.buf.Len() != typed.MaxAnswerLen+1 {
		t.Errorf("capped kept %d bytes", c.buf.Len())
	}
}

// TestTimeoutKillsTheWholeProcessGroup starts a reader that forks children
// which ignore hangup and hold the pipes. When time runs out all of them
// must be gone.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	c := &Command{Argv: []string{script(t, `
trap '' HUP TERM INT
(trap '' HUP TERM INT; sleep 60) &
echo $! >> "`+pids+`"
(trap '' HUP TERM INT; sleep 60) &
echo $! >> "`+pids+`"
echo $$ >> "`+pids+`"
sleep 60
`)}, Pipe: []string{"x"}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := c.Answer(ctx, Job{Ask: 1, Source: "mail"}); err == nil || !strings.Contains(err.Error(), "out of time") {
		t.Errorf("err = %v, want a timeout", err)
	}
	if took := time.Since(start); took > 9*time.Second {
		t.Errorf("Answer took %v", took)
	}
	time.Sleep(200 * time.Millisecond)
	for _, field := range strings.Fields(mustRead(t, pids)) {
		pid, _ := strconv.Atoi(field)
		if pid > 1 && syscall.Kill(pid, 0) == nil {
			t.Errorf("process %d of the reader's group is still running", pid)
			syscall.Kill(pid, syscall.SIGKILL) // a pid this test started
		}
	}
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
