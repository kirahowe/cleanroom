// Package gateway is cleanroom's MCP server: the imperative shell around
// the decision core.
//
// Every tool call follows the same three steps. First, under the log's
// lock, ask core.Decide what to do and record the call with the decision.
// Second, and only if the decision was "run", perform the effect. Third,
// record the result. Nothing that depends on a call's arguments happens
// before the first step: no upstream is contacted and no name is resolved.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/eventlog"
	"cleanroom/internal/reader"
	"cleanroom/internal/upstream"
	"cleanroom/internal/version"
)

// Gateway serves one session. All of its fields are fixed once it is
// built. What changes over time lives in the event log.
type Gateway struct {
	Policy core.Policy
	// Log is the event log shared with every other cleanroom process.
	Log *eventlog.Log
	// Session identifies the conversation this gateway serves. Whoever
	// launches the server chooses it, and a conversation that is resumed
	// under the same id gets its history back from the log.
	Session   string
	Upstreams *upstream.Set
	// Readers answers questions about sources, by source name.
	Readers map[string]reader.Reader
	// ReaderTimeout bounds one reader's run. Zero means three minutes.
	ReaderTimeout time.Duration
	// Notify is called whenever something needs the person: a call was
	// held, or an answer is waiting for them. It is the one place to
	// attach a notifier. If nil, the notice is logged.
	Notify func(Notice)
	// Logf logs operational messages. If nil, they are discarded.
	Logf func(format string, args ...any)
}

func (g *Gateway) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

// Notice says that something needs the person's attention.
type Notice struct {
	Kind NoticeKind
	// Seq identifies it on the command line: cleanroom show <Seq>.
	Seq int
}

// NoticeKind says what is waiting for the person.
type NoticeKind string

const (
	HeldCall      NoticeKind = "proposal" // a call is held for approval
	AnswerWaiting NoticeKind = "answer"   // a free-text answer is waiting to be read
)

// needsPerson is the single place where "this needs a person" is raised.
func (g *Gateway) needsPerson(n Notice) {
	if g.Notify != nil {
		g.Notify(n)
		return
	}
	g.logf("%s %d needs you: run `cleanroom show %d`", n.Kind, n.Seq, n.Seq)
}

const instructions = `Cleanroom keeps you away from untrusted content such as email and web pages, so that nothing you read can give you instructions.

To learn something from a source, call "ask" with a question and the type of answer you want (a date, a number, a sum of money, yes or no, or one of a list of choices). A separate reader does the reading and you receive only the typed value.

If you ask for free text, the answer goes to the user, not to you. You can choose to read it with accept_contamination: true. That marks this session contaminated for the rest of its life, and from then on calls that act for the user are held until the user approves them.

Call "status" to see what this session carries and where held calls stand.`

// Server builds the MCP server for an agent's session.
func (g *Gateway) Server(ctx context.Context) (*mcp.Server, error) {
	if !core.ValidSession(g.Session) {
		return nil, fmt.Errorf("session id %q is not valid: use letters, digits, dot, dash and underscore", g.Session)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "cleanroom", Version: version.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	g.addStatus(s)
	g.addAsk(s)
	g.addReadAnswer(s)
	offered, err := g.addUpstreamTools(ctx, s)
	if err != nil {
		return nil, err
	}
	s.AddReceivingMiddleware(g.recordUnoffered(offered))
	return s, nil
}

// recordUnoffered catches a call to a tool this server does not offer and
// sends it through Decide like any other call, which refuses it and records
// the attempt. Left to the MCP layer, such a call is rejected as unknown
// and leaves nothing in the log, and an agent reaching for a tool it was
// not given is something the person may want to see.
func (g *Gateway) recordUnoffered(offered map[string]string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if method != "tools/call" || !ok || offered[p.Name] != "" {
				return next(ctx, method, req)
			}
			// The public name is <upstream>_<tool>. If it starts with
			// an upstream's name, record the call against that
			// upstream. Otherwise the name is recorded whole.
			upstream, tool := "", p.Name
			for name := range g.Policy.Upstreams {
				if rest, ok := strings.CutPrefix(p.Name, name+"_"); ok && len(name) > len(upstream) {
					upstream, tool = name, rest
				}
			}
			return g.callTool(ctx, core.Call{
				Session: g.Session, Op: core.OpTool,
				Upstream: upstream, Tool: tool, Args: p.Arguments,
			}), nil
		}
	}
}

// textResult is a tool result that says the same thing twice: as text for
// the model, and as structured content for clients that use it.
func textResult(text string, structured any) *mcp.CallToolResult {
	res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	if structured != nil {
		res.StructuredContent = structured
	}
	return res
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// jsonResult renders v as indented JSON text, with v as structured content.
func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult("cleanroom could not encode its own result: " + err.Error())
	}
	return textResult(string(b), v)
}

// emptyObject is the input schema of a tool that takes no arguments.
var emptyObject = json.RawMessage(`{"type":"object","additionalProperties":false}`)
