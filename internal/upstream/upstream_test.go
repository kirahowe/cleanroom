package upstream_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/mcptest"
	"cleanroom/internal/upstream"
)

// counted wraps a dialer and counts the connections it makes.
func counted(dial upstream.Dialer, n *atomic.Int32) upstream.Dialer {
	return func(ctx context.Context, name string, up core.Upstream) (*mcp.ClientSession, error) {
		n.Add(1)
		return dial(ctx, name, up)
	}
}

func twoUpstreams(t *testing.T) (*upstream.Set, *atomic.Int32) {
	policy := core.Policy{Upstreams: map[string]core.Upstream{"mail": {}, "calendar": {}}}
	fakes := map[string]*mcptest.Upstream{
		"mail":     mcptest.NewUpstream(mcptest.Tool{Name: "search", Reply: "[]"}),
		"calendar": mcptest.NewUpstream(mcptest.Tool{Name: "list_events", Reply: "[]"}),
	}
	var dials atomic.Int32
	return upstream.New(policy, counted(mcptest.Dialer(t, fakes), &dials)), &dials
}

// An upstream may have to log in when it starts, so it is connected once
// and the connection is kept, however many calls use it.
func TestAnUpstreamIsConnectedOnce(t *testing.T) {
	set, dials := twoUpstreams(t)
	ctx := context.Background()
	for range 5 {
		if _, err := set.Tools(ctx, "mail"); err != nil {
			t.Fatal(err)
		}
		if res, err := set.Call(ctx, "mail", "search", nil); err != nil || res.IsError {
			t.Fatalf("search = %+v, %v", res, err)
		}
	}
	set.Close()
	if got := dials.Load(); got != 1 {
		t.Errorf("mail was used ten times and calendar never; %d connections were made, want 1", got)
	}
}

// Close used to connect to every upstream it had not used, only to
// disconnect again.
func TestCloseStartsNothing(t *testing.T) {
	set, dials := twoUpstreams(t)
	set.Close()
	if got := dials.Load(); got != 0 {
		t.Errorf("closing an unused set made %d connections, want none", got)
	}
}
