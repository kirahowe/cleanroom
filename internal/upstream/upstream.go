// Package upstream connects cleanroom to the MCP servers it proxies. Here
// cleanroom is the MCP client.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/version"
)

// Dialer connects to one upstream server.
type Dialer func(ctx context.Context, name string, up core.Upstream) (*mcp.ClientSession, error)

// Set is the upstream servers of a policy. Each is connected on first use
// and stays connected until Close, so a server that has to log in does so
// once for the life of the process that holds the Set, however many calls
// and questions pass through it.
//
// The connections and tool lists are the only mutable state here, and
// they carry no decisions: what a session may do is never cached.
type Set struct {
	dial Dialer
	// servers has one entry per upstream in the policy. The map is
	// fixed once New returns. Each entry guards its own state, so a
	// slow upstream holds up nobody else.
	servers map[string]*server
}

// server is one upstream and its lazily made connection.
type server struct {
	name string
	up   core.Upstream

	// connect dials the upstream and lists its tools, once. The list is
	// kept for good, so a server cannot change a tool's annotations
	// partway through a session to get a different class.
	connect func() (*connection, error)
	// dialed is set once connect has been asked to dial, so that Close
	// can tell an upstream that was never used from one that was.
	dialed atomic.Bool
}

type connection struct {
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

// New returns the upstreams of a policy. Nothing is started until an
// upstream is first used.
func New(p core.Policy, dial Dialer) *Set {
	s := &Set{dial: dial, servers: map[string]*server{}}
	for name, up := range p.Upstreams {
		srv := &server{name: name, up: up}
		// The connection outlives whichever call happened to make it,
		// so it is not tied to that call's context.
		srv.connect = sync.OnceValues(func() (*connection, error) {
			srv.dialed.Store(true)
			return s.open(context.Background(), srv)
		})
		s.servers[name] = srv
	}
	return s
}

// CommandDialer starts each upstream as a child process, with dir as its
// working directory, and speaks MCP over its stdin and stdout. What the
// process writes to stderr goes to stderr, which may be nil to discard it.
func CommandDialer(dir string, stderr io.Writer) Dialer {
	return func(ctx context.Context, name string, up core.Upstream) (*mcp.ClientSession, error) {
		// Not CommandContext: the process must outlive the call that
		// started it. Close stops it.
		cmd := exec.Command(up.Command[0], up.Command[1:]...)
		cmd.Dir = dir
		cmd.Stderr = stderr
		cmd.Env = os.Environ()
		for k, v := range up.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "cleanroom", Version: version.Version}, nil)
		return client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	}
}

func (s *Set) open(ctx context.Context, srv *server) (*connection, error) {
	cs, err := s.dial(ctx, srv.name, srv.up)
	if err != nil {
		return nil, fmt.Errorf("connecting to upstream %q: %w", srv.name, err)
	}
	c := &connection{session: cs}
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			cs.Close()
			return nil, fmt.Errorf("listing tools of %q: %w", srv.name, err)
		}
		c.tools = append(c.tools, tool)
	}
	return c, nil
}

func (s *Set) conn(name string) (*connection, error) {
	srv, ok := s.servers[name]
	if !ok {
		return nil, fmt.Errorf("no upstream named %q", name)
	}
	return srv.connect()
}

// Tools lists the tools an upstream offers, as they were when it was
// first connected.
func (s *Set) Tools(ctx context.Context, name string) ([]*mcp.Tool, error) {
	c, err := s.conn(name)
	if err != nil {
		return nil, err
	}
	return c.tools, nil
}

// Call calls a tool on an upstream. The gateway calls this only after
// core.Decide has allowed the call.
func (s *Set) Call(ctx context.Context, name, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	c, err := s.conn(name)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return c.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
}

// Close disconnects from every upstream that was connected and stops the
// ones it started. An upstream that was never used is left alone: closing
// must not be what starts it. The Set must not be used afterwards.
func (s *Set) Close() {
	for _, srv := range s.servers {
		if !srv.dialed.Load() {
			continue
		}
		// connect waits for a dial that is still in progress.
		if c, err := srv.connect(); err == nil {
			c.session.Close()
		}
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
