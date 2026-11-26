package runtime

import (
	"context"
	"fmt"
	"slices"
	"time"

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
	// What an earlier run left open is closed: its requests are its own.
	stored := len(msgs)
	msgs = closeCalls(msgs)
	// The earlier runs' tool results are theirs too: sent as handles, which
	// read_output reads back whole, rather than whole on every request. The
	// mark is written before the run's place, so a run that stops before its
	// first save still finds its first message there.
	if mark, ok := agent.ClearingFor(msgs); ok {
		if err := e.saveMark(ctx, transcriptOf(row), stored, msgs, mark); err != nil {
			return false, nil, fmt.Errorf("clearing the earlier runs' results from run %s's view: %w", row.ID, err)
		}
		msgs = append(msgs, mark)
	}
	if err := e.store.SetTranscriptFrom(ctx, db.SetTranscriptFromParams{ID: row.ID, Position: int32(len(msgs))}); err != nil { //nolint:gosec // a transcript is never 2^31 messages
		return false, nil, fmt.Errorf("placing run %s in its transcript: %w", row.ID, err)
	}
	return false, append(msgs, first), nil
}

// saveMark writes msgs and mark after them. Of the stored messages, all but
// the last are kept as they are: closeCalls adds only before a trailing
// files message.
func (e *Executor) saveMark(ctx context.Context, session string, stored int, msgs []agent.Message, mark agent.Message) error {
	all := append(slices.Clone(msgs), mark)
	if s, ok := e.sessions.(agent.SavingFrom); ok {
		from := stored
		if len(msgs) != stored {
			from = max(stored-1, 0)
		}
		return s.SaveFrom(ctx, session, from, all)
	}
	return e.sessions.Save(ctx, session, all)
}

// firstMessage is what a run adds to its transcript: the date, its input,
// and for a channel's run what was said since the last one.
//
// The date is here, not in the system prompt, so every system block stays
// cacheable: a block that changes daily would end the cached prefix there,
// and everything after it, the conversation included, would miss. Each turn
// keeps the date it was asked on, which is also the truth.
func (e *Executor) firstMessage(ctx context.Context, row db.Run) (agent.Message, error) {
	input := row.Input
	if row.ReplyToChannel {
		var err error
		if input, err = e.conversation(ctx, row); err != nil {
			return agent.Message{}, err
		}
	}
	// Pinned, so a compaction that folds this turn still shows when it was asked.
	msg := agent.Message{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: today(e.now()) + "\n\n" + input}},
		Pinned:  true,
	}
	if row.MessageID != nil {
		files, err := e.store.ListAttachmentsByMessage(ctx, *row.MessageID)
		if err != nil {
			return agent.Message{}, fmt.Errorf("reading the files sent with run %s: %w", row.ID, err)
		}
		msg.Content = append(msg.Content, filesOf(files)...)
	}
	return msg, nil
}

// closeCalls gives a result to every call of the transcript's last turn that
// has none: a run stopped while a tool ran leaves its call open, and the next
// request would be refused for it. A turn's files message stays last.
func closeCalls(msgs []agent.Message) []agent.Message {
	end := len(msgs)
	if end > 0 && msgs[end-1].Role == agent.RoleUser && msgs[end-1].Attached {
		end--
	}
	last := -1
	for i := end - 1; i >= 0; i-- {
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
	for _, m := range msgs[last+1 : end] {
		answered[m.ToolCallID] = true
	}
	var closing []agent.Message
	for _, call := range msgs[last].ToolCalls {
		if !answered[call.ID] {
			closing = append(closing, agent.Message{
				Role: agent.RoleTool, ToolCallID: call.ID,
				Content: []agent.Content{{Type: agent.ContentText, Text: endedMidCall}},
			})
		}
	}
	if len(closing) == 0 {
		return msgs
	}
	// msgs[end:] is the files message, when there is one.
	return slices.Concat(msgs[:end], closing, msgs[end:])
}

func today(now time.Time) string {
	return "Now: " + now.UTC().Format("Monday, 2 January 2006, 15:04") + " UTC."
}
