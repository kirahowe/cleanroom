package main

import (
	"syscall"
	"testing"
)

func TestOnSignal(t *testing.T) {
	const background, running = false, false
	const foreground, stopping = true, true
	for _, tc := range []struct {
		name                 string
		foreground, stopping bool
		sig                  syscall.Signal
		want                 signalAction
	}{
		{"the first signal cancels the command", background, running, syscall.SIGINT, cancelCommand},
		{"SIGTERM cancels like Ctrl-C", background, running, syscall.SIGTERM, cancelCommand},
		{"a second signal ends the process", background, stopping, syscall.SIGINT, exitNow},
		{"Ctrl-C belongs to a foreground command's child", foreground, running, syscall.SIGINT, ignoreSignal},
		{"and still does while the command is stopping", foreground, stopping, syscall.SIGINT, ignoreSignal},
		{"SIGTERM cancels a foreground command", foreground, running, syscall.SIGTERM, cancelCommand},
		{"a second SIGTERM ends a foreground command", foreground, stopping, syscall.SIGTERM, exitNow},
	} {
		if got := onSignal(tc.foreground, tc.stopping, tc.sig); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestExitStatus(t *testing.T) {
	const finished, abandoned = true, false
	for _, tc := range []struct {
		name            string
		finished        bool
		code            int
		sig             syscall.Signal
		want            int
		wantInterrupted bool
	}{
		{"no signal", finished, 1, 0, 1, false},
		{"completed despite the signal", finished, 0, syscall.SIGINT, 0, false},
		{"failed for its own reasons after the signal", finished, 2, syscall.SIGINT, 2, false},
		{"cut short by Ctrl-C", finished, exitInterrupted, syscall.SIGINT, 130, true},
		{"cut short by SIGTERM", finished, exitInterrupted, syscall.SIGTERM, 143, true},
		{"ignored the signal for the whole grace period", abandoned, 0, syscall.SIGTERM, 143, true},
	} {
		status, interrupted := exitStatus(tc.finished, tc.code, tc.sig)
		if status != tc.want || interrupted != tc.wantInterrupted {
			t.Errorf("%s: got %d, %v; want %d, %v", tc.name, status, interrupted, tc.want, tc.wantInterrupted)
		}
	}
}

func TestForeground(t *testing.T) {
	if !foreground([]string{"claude", "--resume", "x"}) || foreground([]string{"ask", "claude"}) || foreground(nil) {
		t.Error("claude, and only claude, is a foreground command")
	}
}
