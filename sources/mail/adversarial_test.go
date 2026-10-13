package main

import (
	"context"
	"strings"
	"testing"
)

func TestReadIDCannotLeaveTheMessagePath(t *testing.T) {
	m := newTestMailpit(t, "http://127.0.0.1:1")
	for _, id := range []string{"../search?query=x", "a/b", "a?b", "a#b", "..", "a%2f..", "a b", "", "a\nb", strings.Repeat("a", 500), "é"} {
		if _, err := m.Read(context.Background(), id); err == nil || !strings.Contains(err.Error(), "invalid message id") {
			t.Errorf("Read(%q): %v, want it refused before any request", id, err)
		}
	}
}
