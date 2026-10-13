package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

const shutdownGrace = time.Second

// withSignals is the process boundary, shared by every CLI command.
// It must return before os.Exit so terminal restoration is not skipped.
func withSignals(command func(context.Context) int) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A blocked password read may never reach its own restoration defer.
	if saved, err := term.GetState(int(os.Stdin.Fd())); err == nil {
		defer term.Restore(int(os.Stdin.Fd()), saved)
	}
	code := interruptible(ctx, func() int { return command(ctx) })
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\ncleanroom: interrupted")
	}
	return code
}

// interruptible owns shutdown for every command, including commands blocked
// in terminal input or dependencies that do not support context cancellation.
// Give cooperative cleanup a bounded chance to finish, then return to main
// to restore the terminal and exit the process.
func interruptible(ctx context.Context, command func() int) int {
	if ctx.Err() != nil {
		return 130
	}
	done := make(chan int, 1)
	go func() { done <- command() }()
	select {
	case code := <-done:
		if ctx.Err() != nil {
			return 130
		}
		return code
	case <-ctx.Done():
	}
	select {
	case <-done:
	case <-time.After(shutdownGrace):
	}
	return 130
}
