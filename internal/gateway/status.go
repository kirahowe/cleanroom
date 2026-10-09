package gateway

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/core"
)

// Status is what the status tool reports. It describes the session using
// only facts from the log and words written by cleanroom. It never includes
// content from a source, so reading it cannot contaminate anyone.
type Status struct {
	Session string `json:"session"`
	// Labels is what the session carries: "contaminated", "private",
	// both, or nothing.
	Labels core.Labels `json:"labels"`
	// ContaminatedBy and PrivateBy say which of the session's own calls
	// gave it each label.
	ContaminatedBy []string `json:"contaminated_by,omitempty"`
	PrivateBy      []string `json:"private_by,omitempty"`
	// Held lists the session's calls that needed the person's approval.
	Held []HeldCall `json:"held,omitempty"`
}

// HeldCall is one held call of the session and where it stands.
type HeldCall struct {
	Proposal int         `json:"proposal"`
	Tool     string      `json:"tool"`
	Status   core.Status `json:"status"`
}

func (g *Gateway) addStatus(s *mcp.Server) {
	s.AddTool(&mcp.Tool{
		Name:        "status",
		Title:       "Session status",
		Description: "Report what this session carries (contaminated, private) and why, and where its held calls stand. Reads only cleanroom's own log, so it is always safe to call.",
		InputSchema: emptyObject,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(bool)},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		st, err := g.status()
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(st), nil
	})
}

func (g *Gateway) status() (Status, error) {
	log, err := g.Log.Read()
	if err != nil {
		return Status{}, err
	}
	state := core.StateOf(g.Policy, log, g.Session)
	st := Status{
		Session:        g.Session,
		Labels:         state.Labels,
		ContaminatedBy: describe(log, state.ContaminatedBy),
		PrivateBy:      describe(log, state.PrivateBy),
	}
	if state.Labels.Private && len(state.PrivateBy) == 0 {
		st.PrivateBy = []string{"the policy marks every session private from the start"}
	}
	for _, pr := range core.Proposals(log) {
		if pr.Call.Call.Session == g.Session {
			st.Held = append(st.Held, HeldCall{
				Proposal: pr.Seq,
				Tool:     pr.Call.Call.Upstream + "." + pr.Call.Call.Tool,
				Status:   pr.Status,
			})
		}
	}
	return st, nil
}

// describe names each event in seqs in a few words written by cleanroom.
func describe(log []core.Event, seqs []int) []string {
	var out []string
	for _, seq := range seqs {
		out = append(out, fmt.Sprintf("event %d: %s", seq, core.Summarize(log, seq)))
	}
	return out
}
