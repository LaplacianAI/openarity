package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

// unspoken is the run's own bookkeeping: finding its tools, planning, reading
// what it already holds. In a chat it is noise.
var unspoken = map[string]bool{
	GraphToolName: true, PlanToolName: true, AttachmentsToolName: true, ReadAttachmentToolName: true,
	agent.ReadOutputToolName: true, agent.DescribeToolsName: true,
	agent.SkillToolName: true, agent.SkillResourceToolName: true,
	patterns.PlanToolName: true, patterns.CritiqueToolName: true,
}

// spoken is what a tool call says in the conversation, or false when it says
// nothing. Its result never does: it may hold what was never meant for the
// chat. A tool found by search is named as itself, not as call_tool.
func spoken(call agent.ToolCallEvent) (string, bool) {
	name := call.Name
	switch name {
	case DelegateToolName:
		var args struct {
			Agent string `json:"agent"`
		}
		if json.Unmarshal(call.Arguments, &args) != nil || args.Agent == "" {
			return "", false
		}
		return "Asking " + code(args.Agent) + "…", true
	case agent.CallToolName:
		var args struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(call.Arguments, &args) != nil || args.Name == "" {
			return "", false
		}
		name = args.Name
	}
	if unspoken[name] {
		return "", false
	}
	return "Using " + code(name) + "…", true
}

// code sets a name as code, so a provider rendering Markdown shows it as it
// is: read_logs, not "read" in italics.
func code(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "") + "`"
}

// teller writes a run's steps to its conversation's outbox, each named by the
// run event it came from. Best effort: a step not told is logged, and the run
// goes on to its answer.
func (e *Executor) teller(ctx context.Context, row db.Run) func(seq int32, text string) {
	// Only a top-level run answers a channel, so a delegated one tells nothing.
	if !row.ReplyToChannel {
		return nil
	}
	shows, err := e.store.RunShowsEvents(ctx, row.ID)
	if err != nil {
		e.logger.WarnContext(ctx, "steps not told; reading the channel's reply mode failed", "run", row.ID, "err", err)
		return nil
	}
	if !shows {
		return nil
	}
	return func(seq int32, text string) {
		n, err := e.store.InsertEventReply(ctx, db.InsertEventReplyParams{RunID: row.ID, EventSeq: seq, Text: text})
		if err != nil {
			e.logger.WarnContext(ctx, "step not told", "run", row.ID, "seq", seq, "err", err)
			return
		}
		if n == 0 || e.kickEvent == nil {
			return
		}
		if err := e.kickEvent(row.ID, seq); err != nil {
			e.logger.WarnContext(ctx, "step not kicked; the sweep will send it", "run", row.ID, "seq", seq, "err", err)
		}
	}
}
