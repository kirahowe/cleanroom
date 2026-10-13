package gateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/upstream"
)

// addUpstreamTools offers each upstream tool to the agent under the name
// <upstream>_<tool>. Two kinds of tool are left out: tools with no class,
// which Decide would refuse anyway, and the read tools of an ask-only
// source. It returns every tool name the server now offers, with where each
// comes from.
func (g *Gateway) addUpstreamTools(ctx context.Context, s *mcp.Server) (map[string]string, error) {
	taken := map[string]string{"status": "cleanroom", "ask": "cleanroom", "read_answer": "cleanroom"}
	for _, name := range sortedKeys(g.Policy.Upstreams) {
		up := g.Policy.Upstreams[name]
		tools, err := g.Upstreams.Tools(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, tool := range tools {
			hint := upstream.Hint(tool)
			class, ok := core.ClassOf(up, tool.Name, hint)
			if !ok {
				g.logf("not offering %s.%s: no class in the policy and no usable annotations", name, tool.Name)
				continue
			}
			if class == core.Read && up.AskOnly {
				continue
			}
			public := name + "_" + tool.Name
			if other, clash := taken[public]; clash {
				return nil, fmt.Errorf("tool name %q is produced by both %s and %s.%s", public, other, name, tool.Name)
			}
			taken[public] = name + "." + tool.Name

			call := core.Call{Session: g.Session, Op: core.OpTool, Upstream: name, Tool: tool.Name, Hint: hint}
			s.AddTool(&mcp.Tool{
				Name:        public,
				Title:       tool.Title,
				Description: describeTool(tool.Description, class, up.Tools[tool.Name]),
				InputSchema: tool.InputSchema,
				Annotations: tool.Annotations,
				// No OutputSchema: a held or refused call returns
				// cleanroom's own shape, not the upstream's.
			}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				c := call
				c.Args = req.Params.Arguments
				return g.callTool(ctx, c), nil
			})
		}
	}
	return taken, nil
}

func describeTool(upstream string, class core.Class, t core.Tool) string {
	note := classNote(class, t)
	if t.DiscardResult {
		note += discardNote
	}
	return strings.TrimSpace(upstream + "\n\n" + note)
}

// classNote tells the agent how cleanroom treats a tool.
func classNote(class core.Class, t core.Tool) string {
	switch {
	case t.AlwaysHold:
		return "[cleanroom] Every call to this tool is held until the user approves it."
	case class == core.Act:
		return "[cleanroom] This tool acts for the user. Calls are held for the user's approval once this session is contaminated."
	case class == core.Egress:
		return "[cleanroom] This tool can send data out. Calls are held for the user's approval once this session is contaminated and private."
	case class == core.Read:
		return "[cleanroom] The session takes on the labels of whatever this tool returns."
	}
	return ""
}

// discardNote is added to the description of a tool whose result cleanroom
// drops.
const discardNote = " Cleanroom discards this tool's output: you are told only whether it ran."

// callTool is the path every upstream call takes: decide and record, then
// act, then record the result.
func (g *Gateway) callTool(ctx context.Context, c core.Call) *mcp.CallToolResult {
	rec, err := g.decide(c)
	if err != nil {
		// A call that cannot be recorded is not made.
		return errorResult("cleanroom could not record this call, so it was not made: " + err.Error())
	}
	c, d := rec.Call.Call, rec.Call.Decision
	switch d.Verdict {
	case core.Run:
	case core.Hold:
		g.needsPerson(Notice{Kind: HeldCall, Seq: rec.Seq})
		return g.heldResult(rec.Seq, d)
	default:
		return refusedResult(d)
	}

	// From here on, use only the call as it was recorded.
	res, err := g.Upstreams.Call(ctx, c.Upstream, c.Tool, c.Args)
	g.record(core.Event{Kind: core.KindResult, Result: &core.Result{Ref: rec.Seq, OK: err == nil && !res.IsError}})
	// The session took on the upstream's labels when the call was
	// decided. Check that they still cover what the upstream may have
	// returned: a contaminated session may have written to it since.
	if events, logErr := g.Log.Read(); logErr != nil || !core.MayReturn(g.Policy, events, *rec.Call) {
		return errorResult(fmt.Sprintf("%s.%s ran, but %s was written to while it did, so its result is withheld. Call it again.", c.Upstream, c.Tool, c.Upstream))
	}
	if g.Policy.Upstreams[c.Upstream].Tools[c.Tool].DiscardResult {
		// The caller took on no labels for this call, so it gets none
		// of the upstream's words either, not even an error message.
		if err != nil || res.IsError {
			return errorResult(fmt.Sprintf("%s.%s failed. Cleanroom discards this tool's output, so there are no details here.", c.Upstream, c.Tool))
		}
		return textResult(fmt.Sprintf("%s.%s ran.", c.Upstream, c.Tool), nil)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("%s.%s failed: %v", c.Upstream, c.Tool, err))
	}
	return res
}

// decide sanitizes a call, asks the core what to do with it, and records
// the call with the decision, as one atomic step on the shared log. It
// returns the recorded event, which holds the call as stored, the decision
// and the event's Seq. The stored call is the one to carry out.
func (g *Gateway) decide(c core.Call) (core.Event, error) {
	c = core.Sanitize(c)
	added, err := g.Log.Update(func(events []core.Event) ([]core.Event, error) {
		if core.ReaderSpent(events, c.Session) {
			return nil, errReaderSpent
		}
		return []core.Event{core.Record(g.Policy, events, c, core.Decide(g.Policy, events, c))}, nil
	})
	if err != nil {
		return core.Event{}, err
	}
	return added[0], nil
}

// errReaderSpent stops a reader that has used its calls. Nothing more is
// recorded for it.
var errReaderSpent = errors.New("this reader has used all its calls")

// record appends an event that needs no decision. A failure is reported
// but does not undo an effect that has already happened.
func (g *Gateway) record(e core.Event) {
	if _, err := g.Log.Update(func([]core.Event) ([]core.Event, error) {
		return []core.Event{e}, nil
	}); err != nil {
		g.logf("could not record a %s event: %v", e.Kind, err)
	}
}

// Held is the structured result of a call that was held.
type Held struct {
	Status   string   `json:"status"` // always "held"
	Proposal int      `json:"proposal"`
	Reasons  []string `json:"reasons"`
}

func (g *Gateway) heldResult(seq int, d core.Decision) *mcp.CallToolResult {
	reasons := g.explain(d)
	text := fmt.Sprintf(`HELD: this call has not run. It is waiting for the user as proposal %d.
Why: %s
The user decides outside this conversation, with: cleanroom approve %d (or deny %d).
Call status to see the outcome. Do not retry: a retry would only be held again.`,
		seq, strings.Join(reasons, " "), seq, seq)
	return textResult(text, Held{Status: "held", Proposal: seq, Reasons: reasons})
}

func refusedResult(d core.Decision) *mcp.CallToolResult {
	var reasons []string
	for _, r := range d.Reasons {
		reasons = append(reasons, r.Text)
	}
	return errorResult("REFUSED: " + strings.Join(reasons, " "))
}

// explain renders a decision's reasons, naming the events they cite.
func (g *Gateway) explain(d core.Decision) []string {
	events, err := g.Log.Read()
	if err != nil {
		events = nil // the reasons still stand without their citations
	}
	var out []string
	for _, r := range d.Reasons {
		text := r.Text
		if cited := describe(events, r.Events); len(cited) > 0 {
			text += " (" + strings.Join(cited, "; ") + ")"
		}
		out = append(out, text+".")
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
