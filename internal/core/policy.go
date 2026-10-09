package core

import (
	"errors"
	"fmt"
	"regexp"
)

// Policy is the trusted configuration: which upstream servers exist, what
// their content carries, and what each tool can do. The user writes it. No
// model and no upstream server can change it.
type Policy struct {
	// SessionsStartPrivate marks every session private from its first
	// call. Set it when the agent already holds private data that
	// cleanroom cannot see, such as the user's notes.
	SessionsStartPrivate bool `json:"sessions_start_private,omitempty"`

	// Upstreams maps a name to an MCP server that cleanroom proxies.
	Upstreams map[string]Upstream `json:"upstreams"`
}

// Upstream is one MCP server behind the gateway. An upstream whose Labels
// are not empty is a source: its content is untrusted, private, or both.
type Upstream struct {
	// Command starts the server, which speaks MCP on stdin and stdout.
	Command []string `json:"command"`
	// Env adds variables to the server's environment.
	Env map[string]string `json:"env,omitempty"`

	// Labels is what content read from this upstream carries.
	Labels Labels `json:"labels"`

	// AskOnly hides the upstream's read tools from agents. Its content
	// is then reachable only through ask, by a reader.
	AskOnly bool `json:"ask_only,omitempty"`

	// Reader is the command that answers questions about this upstream.
	// Without one, ask refuses the source. See package reader for the
	// placeholders the command may use.
	Reader []string `json:"reader,omitempty"`

	// Tools gives the class of each tool, overriding the server's own
	// annotations. A tool with no entry here and no usable annotations
	// is refused. On an upstream that has labels, annotations are not
	// used at all: every tool of a source must be classed here.
	Tools map[string]Tool `json:"tools,omitempty"`
}

// Tool is the policy for one upstream tool.
type Tool struct {
	// Class says what the tool can do. Empty means: use the server's
	// annotations.
	Class Class `json:"class,omitempty"`
	// AlwaysHold holds every call for approval, whatever the session's
	// state.
	AlwaysHold bool `json:"always_hold,omitempty"`
	// DiscardResult makes cleanroom drop what the tool returns. The
	// caller is told only whether the call worked, so it takes on no
	// labels from it. Use it for tools called for their effect, such as
	// sending a message: without it, calling a tool of an untrusted
	// source contaminates the caller, whatever the tool returns.
	DiscardResult bool `json:"discard_result,omitempty"`
}

// Class is what a tool can do, as far as the decision is concerned.
type Class string

const (
	// Read returns content and changes nothing. Never held.
	Read Class = "read"
	// Write changes state that stays inside the system. Never held, but
	// the writer's labels are left on the upstream it wrote to.
	Write Class = "write"
	// Egress can carry data out but does nothing in the user's name, such
	// as fetching a URL. Held when the session is contaminated and private.
	Egress Class = "egress"
	// Act does something in the user's name, such as sending mail or
	// creating a calendar event. Held when the session is contaminated.
	Act Class = "act"
)

func (c Class) valid() bool {
	return c == Read || c == Write || c == Egress || c == Act
}

// ClassFromHints guesses a class from the tool annotations in the MCP spec.
// The pointers are nil when the server left a hint out, in which case the
// spec's defaults apply: destructive and open-world.
//
// The hints come from the upstream server, so this is only a first guess. A
// Tool entry in the policy overrides it.
func ClassFromHints(readOnly bool, destructive, openWorld *bool) Class {
	open := openWorld == nil || *openWorld
	switch {
	case readOnly && !open:
		return Read
	case readOnly:
		return Egress // reads, but its arguments reach the outside world
	case open:
		return Act
	case destructive == nil || *destructive:
		return Act
	default:
		return Write
	}
}

var (
	upstreamName = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	// toolName is what a tool may be called. The MCP spec recommends
	// about this much, and it keeps a name printable wherever it is shown.
	toolName    = regexp.MustCompile(`^[A-Za-z0-9_.\-/]{1,128}$`)
	sessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// ValidSession reports whether s can name a client session. The pattern
// leaves out the colon, which is reserved for reader sessions.
func ValidSession(s string) bool { return sessionName.MatchString(s) }

// Validate reports the first problem in a policy.
func (p Policy) Validate() error {
	if len(p.Upstreams) == 0 {
		return errors.New("policy has no upstreams")
	}
	for name, up := range p.Upstreams {
		if !upstreamName.MatchString(name) {
			return fmt.Errorf("upstream name %q must match %s", name, upstreamName)
		}
		if len(up.Command) == 0 {
			return fmt.Errorf("upstream %q has no command", name)
		}
		for tool, t := range up.Tools {
			if !toolName.MatchString(tool) {
				return fmt.Errorf("upstream %q: tool name %q must match %s", name, tool, toolName)
			}
			if t.Class != "" && !t.Class.valid() {
				return fmt.Errorf("upstream %q tool %q: unknown class %q", name, tool, t.Class)
			}
		}
		if up.AskOnly && len(up.Reader) == 0 {
			return fmt.Errorf("upstream %q is ask_only but has no reader, so nothing could read it", name)
		}
	}
	return nil
}
