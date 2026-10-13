package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type cancelInput struct {
	io.Reader
	cancel context.CancelFunc
}

func (in cancelInput) Read(p []byte) (int, error) {
	in.cancel()
	return in.Reader.Read(p)
}

// Cancel as soon as the command waits for input, leaving stdin open and
// blocked. The command must exit without needing Enter or an EOF.
func runCancelledPrompt(t *testing.T, prefix string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, args, stdio{
			stdin:  io.MultiReader(strings.NewReader(prefix), cancelInput{in, cancel}),
			stdout: &out, stderr: &out, terminal: true,
			readPassword: func() (string, error) {
				return "", errors.New("unexpected password prompt")
			},
		})
	}()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("exit %d, want 130:\n%s", code, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled prompt is still waiting for input")
	}
	return out.String()
}
