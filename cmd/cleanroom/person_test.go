package main

import (
	"strings"
	"testing"
	"time"

	"cleanroom/internal/core"
)

// The log says why an ask came to nothing, on the row of its answer.
func TestLogTableGivesTheReasonForNoAnswer(t *testing.T) {
	events := []core.Event{
		{Seq: 1, Kind: core.KindCall, Call: &core.Decided{
			Call:     core.Call{Session: "chief", Op: core.OpAsk, Upstream: "mail"},
			Decision: core.Decision{Verdict: core.Run},
		}},
		{Seq: 2, Kind: core.KindAnswer, Answer: &core.Answer{Ref: 1, Outcome: core.OutcomeNone, Detail: "the reader did not find one, or was not sure"}},
		{Seq: 3, Kind: core.KindAnswer, Answer: &core.Answer{Ref: 1, Outcome: core.OutcomeTyped}},
	}
	table := logTable(events, time.UTC)
	for _, want := range []string{
		"asked mail",
		"no answer to ask 1: the reader did not find one, or was not sure",
		"answer to ask 1 (typed)",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("want %q in:\n%s", want, table)
		}
	}
}
