package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestInterruptible(t *testing.T) {
	t.Run("normal exit", func(t *testing.T) {
		if got := interruptible(context.Background(), func() int { return 7 }); got != 7 {
			t.Fatalf("exit %d, want 7", got)
		}
	})
	t.Run("cancelled before start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := interruptible(ctx, func() int { t.Error("started after cancellation"); return 0 }); got != 130 {
			t.Fatalf("exit %d, want 130", got)
		}
	})
	t.Run("waits for cleanup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cleaned := make(chan struct{})
		code := interruptible(ctx, func() int {
			defer close(cleaned)
			cancel()
			return 0
		})
		if code != 130 {
			t.Fatalf("exit %d, want 130", code)
		}
		select {
		case <-cleaned:
		default:
			t.Fatal("exited before cleanup")
		}
	})
}

// A real SIGINT must stop even a command that never observes its context.
func TestInterruptStopsBlockedCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(), "CLEANROOM_TEST_AS=blocked-command")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "waiting\n" {
		t.Fatalf("command did not start: %q, %v", line, err)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 130 {
		t.Fatalf("interrupt: %v, context: %v; want exit 130", err, ctx.Err())
	}
}
