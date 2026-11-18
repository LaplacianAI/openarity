package patterns

import (
	"context"
	"slices"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// An open turn is the end of a transcript a tool suspended: the model's
// calls, the results of those before the one that suspended, and the files
// they attached, if any.
type open struct {
	// results is where the turn's results end and the next goes.
	results int
	// files is the turn's files message, or -1.
	files int
	calls []agent.ToolCall
}

// openTurn is the turn msgs ends in when some of its calls have no result.
// A turn whose every call is answered is not open: the model is next.
func openTurn(msgs []agent.Message) (open, bool) {
	i, files := len(msgs)-1, -1
	if i >= 0 && msgs[i].Role == agent.RoleUser && msgs[i].Attached {
		files = i
		i--
	}
	answered := map[string]bool{}
	for i >= 0 && msgs[i].Role == agent.RoleTool {
		answered[msgs[i].ToolCallID] = true
		i--
	}
	if i < 0 || msgs[i].Role != agent.RoleAssistant || len(msgs[i].ToolCalls) == 0 {
		return open{}, false
	}

	var calls []agent.ToolCall
	for _, call := range msgs[i].ToolCalls {
		if !answered[call.ID] {
			calls = append(calls, call)
		}
	}
	if len(calls) == 0 {
		return open{}, false
	}
	results := len(msgs)
	if files >= 0 {
		results = files
	}
	return open{results: results, files: files, calls: calls}, true
}

// finishTurn makes an open turn's remaining calls, in order, and keeps the
// turn in the shape a provider takes: every result, then one message with
// everything the turn's tools attached.
func finishTurn(ctx context.Context, in agent.Input, byName map[string]agent.Tool,
	msgs []agent.Message, o open,
) ([]agent.Message, error) {
	results, attached, err := dispatch(ctx, in, byName, o.calls)

	out := slices.Concat(msgs[:o.results], results)
	var parts []agent.Content
	if o.files >= 0 {
		parts = slices.Clone(msgs[o.files].Content)
	}
	if ctx.Err() == nil {
		if more, ok := agent.Attached(attached, o.calls); ok {
			parts = append(parts, more.Content...)
		}
	}
	if len(parts) > 0 {
		out = append(out, agent.Message{Role: agent.RoleUser, Content: parts, Attached: true})
	}
	return out, err
}
