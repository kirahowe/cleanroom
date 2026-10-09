// Package upstream connects cleanroom to the MCP servers it proxies. Here
// cleanroom is the MCP client.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/version"
)

// Dialer connects to one upstream server.
type Dialer func(ctx context.Context, name string, up core.Upstream) (*mcp.ClientSession, error)

// Set is the upstream servers of a policy, connected on first use.
//
// The connections and the tool lists are the only mutable state here. They
// are guarded by mu and carry no decisions: what a session may do is never
// cached.
type Set struct {
	policy core.Policy
	dial   Dialer

	mu    sync.Mutex
	conns map[string]*mcp.ClientSession
	tools map[string][]*mcp.Tool
}

// New returns the upstreams of a policy. Nothing is started until an
// upstream is first used.
func New(p core.Policy, dial Dialer) *Set {
	return &Set{policy: p, dial: dial, conns: map[string]*mcp.ClientSession{}, tools: map[string][]*mcp.Tool{}}
}

// CommandDialer starts each upstream as a child process, with dir as its
// working directory, and speaks MCP over its stdin and stdout.
func CommandDialer(dir string) Dialer {
	return func(ctx context.Context, name string, up core.Upstream) (*mcp.ClientSession, error) {
		cmd := exec.Command(up.Command[0], up.Command[1:]...)
		cmd.Dir = dir
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		for k, v := range up.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "cleanroom", Version: version.Version}, nil)
		return client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	}
}

func (s *Set) conn(ctx context.Context, name string) (*mcp.ClientSession, error) {
	up, ok := s.policy.Upstreams[name]
	if !ok {
		return nil, fmt.Errorf("no upstream named %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.conns[name]; ok {
		return cs, nil
	}
	cs, err := s.dial(ctx, name, up)
	if err != nil {
		return nil, fmt.Errorf("connecting to upstream %q: %w", name, err)
	}
	s.conns[name] = cs
	return cs, nil
}

// Tools lists the tools an upstream offers. The list is fetched once and
// kept, so a server cannot change a tool's annotations partway through a
// session to get a different class.
func (s *Set) Tools(ctx context.Context, name string) ([]*mcp.Tool, error) {
	cs, err := s.conn(ctx, name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tools, ok := s.tools[name]; ok {
		return tools, nil
	}
	var tools []*mcp.Tool
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("listing tools of %q: %w", name, err)
		}
		tools = append(tools, tool)
	}
	s.tools[name] = tools
	return tools, nil
}

// Call calls a tool on an upstream. The gateway calls this only after
// core.Decide has allowed the call.
func (s *Set) Call(ctx context.Context, name, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	cs, err := s.conn(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
}

// Close disconnects from every upstream and stops the ones it started.
func (s *Set) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, cs := range s.conns {
		cs.Close()
		delete(s.conns, name)
	}
}

// Hint guesses a tool's class from the annotations its server gave. It
// returns the empty class when the server gave none. The guess is only
// used for tools the policy does not mention.
func Hint(t *mcp.Tool) core.Class {
	if t.Annotations == nil {
		return ""
	}
	a := t.Annotations
	return core.ClassFromHints(a.ReadOnlyHint, a.DestructiveHint, a.OpenWorldHint)
}
