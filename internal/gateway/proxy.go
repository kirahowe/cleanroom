package gateway

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
	"cleanroom/internal/upstream"
)

// addUpstreamTools offers each upstream tool to the agent under the name
// <upstream>_<tool>. Two kinds of tool are left out: tools with no class,
// which Decide would refuse anyway, and the read tools of an ask-only
// source.
func (g *Gateway) addUpstreamTools(ctx context.Context, s *mcp.Server) error {
	taken := map[string]string{"status": "cleanroom", "ask": "cleanroom", "read_answer": "cleanroom"}
	for _, name := range sortedKeys(g.Policy.Upstreams) {
		up := g.Policy.Upstreams[name]
		tools, err := g.Upstreams.Tools(ctx, name)
		if err != nil {
			return err
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
				return fmt.Errorf("tool name %q is produced by both %s and %s.%s", public, other, name, tool.Name)
			}
			taken[public] = name + "." + tool.Name

			call := core.Call{Session: g.Session, Op: core.OpTool, Upstream: name, Tool: tool.Name, Hint: hint}
			s.AddTool(&mcp.Tool{
				Name:        public,
				Title:       tool.Title,
				Description: strings.TrimSpace(tool.Description + "\n\n" + classNote(class, up.Tools[tool.Name])),
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
	return nil
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

// callTool is the path every upstream call takes: decide and record, then
// act, then record the result.
func (g *Gateway) callTool(ctx context.Context, c core.Call) *mcp.CallToolResult {
	seq, c, d, err := g.decide(c)
	if err != nil {
		// A call that cannot be recorded is not made.
		return errorResult("cleanroom could not record this call, so it was not made: " + err.Error())
	}
	switch d.Verdict {
	case core.Run:
	case core.Hold:
		g.needsPerson(Notice{Kind: "proposal", Seq: seq})
		return g.heldResult(seq, d)
	default:
		return refusedResult(d)
	}

	res, err := g.Upstreams.Call(ctx, c.Upstream, c.Tool, c.Args)
	g.record(core.Event{Kind: core.KindResult, Result: &core.Result{Ref: seq, OK: err == nil && !res.IsError}})
	if err != nil {
		return errorResult(fmt.Sprintf("%s.%s failed: %v", c.Upstream, c.Tool, err))
	}
	return res
}

// decide sanitizes a call, asks the core what to do with it, and records
// the call with the decision, as one atomic step on the shared log. It
// returns the call as recorded, which is the one to carry out.
func (g *Gateway) decide(c core.Call) (seq int, _ core.Call, d core.Decision, err error) {
	c = core.Sanitize(c)
	added, err := g.Log.Update(func(log []core.Event) ([]core.Event, error) {
		d = core.Decide(g.Policy, log, c)
		return []core.Event{core.Record(g.Policy, log, c, d)}, nil
	})
	if err != nil {
		return 0, c, core.Decision{}, err
	}
	return added[0].Seq, c, d, nil
}

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
	log, err := g.Log.Read()
	if err != nil {
		log = nil
	}
	var out []string
	for _, r := range d.Reasons {
		text := r.Text
		if cited := describe(log, r.Events); len(cited) > 0 {
			text += " (" + strings.Join(cited, "; ") + ")"
		}
		out = append(out, text+".")
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return slices.Clip(keys)
}
