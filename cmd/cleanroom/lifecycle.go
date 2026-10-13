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

// shutdownGrace is how long a command has to finish after a signal. It is
// longer than the two seconds a reader's processes get to die, so that an
// interrupted ask can still record what happened.
const shutdownGrace = 3 * time.Second

// exitInterrupted is the code run returns for a command that a signal cut
// short. exitStatus replaces it with the status for the signal that
// arrived.
const exitInterrupted = 130

type signalAction int

const (
	ignoreSignal signalAction = iota
	cancelCommand
	exitNow
)

// onSignal decides what a signal does to a running command. The first one
// cancels the command's context, and one that arrives while the command is
// already stopping ends the process at once.
//
// A foreground command hands the terminal to a child, and Ctrl-C there
// belongs to the child. Cleanroom takes that SIGINT and does nothing, so
// that it outlives it.
func onSignal(foreground, stopping bool, sig syscall.Signal) signalAction {
	switch {
	case foreground && sig == syscall.SIGINT:
		return ignoreSignal
	case stopping:
		return exitNow
	}
	return cancelCommand
}

// exitStatus is the process's exit status and whether to call the command
// interrupted. A command that did not finish in the grace period was
// interrupted, and so was one that says so itself. Any other command that
// finished keeps its own code, even after a signal: one that completed its
// work before noticing the signal did complete.
func exitStatus(finished bool, code int, sig syscall.Signal) (status int, interrupted bool) {
	if finished && code != exitInterrupted {
		return code, false
	}
	return signalStatus(sig), true
}

// signalStatus is the shell's convention for a process a signal ended: 130
// for SIGINT, 143 for SIGTERM.
func signalStatus(sig syscall.Signal) int {
	return 128 + int(sig)
}

// withSignals is the process boundary, shared by every CLI command. It
// carries out what onSignal and exitStatus decide, and returns before
// os.Exit so that the terminal is restored on every path.
func withSignals(foreground bool, command func(context.Context) int) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A blocked password read may never reach its own restoration defer.
	if saved, err := term.GetState(int(os.Stdin.Fd())); err == nil {
		defer term.Restore(int(os.Stdin.Fd()), saved)
	}
	report := func(status int, interrupted bool) int {
		if interrupted {
			fmt.Fprintln(os.Stderr, "\ncleanroom: interrupted")
		}
		return status
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan int, 1)
	go func() { done <- command(ctx) }()

	var stopping syscall.Signal
	var graceOver <-chan time.Time
	for {
		select {
		case code := <-done:
			return report(exitStatus(true, code, stopping))
		case <-graceOver:
			return report(exitStatus(false, 0, stopping))
		case s := <-signals:
			sig := s.(syscall.Signal)
			switch onSignal(foreground, stopping != 0, sig) {
			case cancelCommand:
				stopping = sig
				cancel()
				graceOver = time.After(shutdownGrace)
			case exitNow:
				return report(signalStatus(sig), true)
			}
		}
	}
}
