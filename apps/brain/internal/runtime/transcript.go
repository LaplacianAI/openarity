package runtime

import (
	"context"
	"fmt"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// endedMidCall is the result a tool call gets when its run ended before the
// call returned: a provider refuses a transcript holding a call with none.
const endedMidCall = "The run ended before this call returned; nothing is known of its result."

// transcriptOf is the SDK session a run's messages and steers are kept under:
// its session's for a top-level run, so every run in a conversation continues
// one transcript; its own id for a delegate, and for a run from before shared
// transcripts.
func transcriptOf(row db.Run) string {
	if row.Transcript != nil {
		return *row.Transcript
	}
	return row.ID.String()
}

// begin is where a run starts in its transcript, and whether it is resuming.
// A run that has a place, transcript_from, resumes: its first message is
// written again when it was lost before the first save. A new one takes the
// transcript's length as its place, written before its message is, then adds
// its message. A run from before shared transcripts resumes when its own has
// anything in it, as it always did.
func (e *Executor) begin(ctx context.Context, row db.Run, msgs []agent.Message) (bool, []agent.Message, error) {
	if row.Transcript == nil { // a delegate, or a run from before shared transcripts
		if len(msgs) > 0 {
			return true, closeCalls(msgs), nil
		}
		first, err := e.firstMessage(ctx, row)
		return false, []agent.Message{first}, err
	}

	if row.TranscriptFrom != nil {
		from := int(*row.TranscriptFrom)
		if len(msgs) > from {
			return true, closeCalls(msgs), nil
		}
		first, err := e.firstMessage(ctx, row)
		return true, append(closeCalls(msgs), first), err
	}

	first, err := e.firstMessage(ctx, row)
	if err != nil {
		return false, nil, err
	}
	msgs = closeCalls(msgs)
	if err := e.store.SetTranscriptFrom(ctx, db.SetTranscriptFromParams{ID: row.ID, Position: int32(len(msgs))}); err != nil { //nolint:gosec // a transcript is never 2^31 messages
		return false, nil, fmt.Errorf("placing run %s in its transcript: %w", row.ID, err)
	}
	return false, append(msgs, first), nil
}

// firstMessage is what a run adds to its transcript: its input, and for a
// channel's run what was said since the last one.
func (e *Executor) firstMessage(ctx context.Context, row db.Run) (agent.Message, error) {
	input := row.Input
	if row.ReplyToChannel {
		var err error
		if input, err = e.conversation(ctx, row); err != nil {
			return agent.Message{}, err
		}
	}
	return agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: input}}}, nil
}

// closeCalls gives a result to every call of the transcript's last turn that
// has none: a run stopped while a tool ran leaves its call open, and the next
// request would be refused for it.
func closeCalls(msgs []agent.Message) []agent.Message {
	last := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == agent.RoleAssistant && len(msgs[i].ToolCalls) > 0 {
			last = i
			break
		}
		if msgs[i].Role != agent.RoleTool {
			return msgs
		}
	}
	if last < 0 {
		return msgs
	}
	answered := map[string]bool{}
	for _, m := range msgs[last+1:] {
		answered[m.ToolCallID] = true
	}
	out := msgs
	for _, call := range msgs[last].ToolCalls {
		if !answered[call.ID] {
			out = append(out, agent.Message{
				Role: agent.RoleTool, ToolCallID: call.ID,
				Content: []agent.Content{{Type: agent.ContentText, Text: endedMidCall}},
			})
		}
	}
	return out
}
