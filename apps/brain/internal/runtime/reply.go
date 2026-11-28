package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/stream"
)

const (
	// ReplyToolName tells the person something now, while the run carries on.
	ReplyToolName = "reply"

	// repliesPerTree is how often a run and its delegates together tell the
	// person something before the answer: past it they are narrating.
	repliesPerTree = 10
	// maxReply is one message, short of every platform's limit.
	maxReply = 4000
)

// ReplyStore is what telling a person something mid-run takes: where the
// run's tree answers, the reply that posts it to a channel, and how often the
// tree has already spoken.
type ReplyStore interface {
	GetReplyReach(ctx context.Context, runID uuid.UUID) (db.GetReplyReachRow, error)
	InsertSaidReply(ctx context.Context, arg db.InsertSaidReplyParams) (int64, error)
	CountSaidInTree(ctx context.Context, rootID uuid.UUID) (int64, error)
}

// replying is the tool. toChannel is a tree answering a channel, whose words
// are also a reply; a conversation read on its stream has the event alone.
// With no store, as in a replay, it is a stand-in that is never made.
type replying struct {
	store     ReplyStore
	run       db.Run
	toChannel bool
	note      func(ctx context.Context, kind string, payload any) (int32, error)
	kick      func(seq int32)
}

func (r *replying) tool() agent.Tool {
	return agent.Tool{
		Name: ReplyToolName,
		Description: fmt.Sprintf("Tell the person something now, without ending your work: what you have done "+
			"so far, or an answer to something they wrote while you worked, which reaches you labelled "+
			"\"The user sent this while you were working\". Then carry on. Your answer still comes at the "+
			"end; do not repeat in it what you sent here. At most %d for the whole task, your delegates' included.",
			repliesPerTree),
		Schema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{`+
			`"text":{"type":"string","maxLength":%d,"description":"what to tell them, in a line or a few"}},`+
			`"required":["text"],"additionalProperties":false}`, maxReply)),
		Invoke: r.invoke,
	}
}

func (r *replying) invoke(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("the arguments are not what %s takes, a text: %w", ReplyToolName, err)
	}
	text, err := replyText(a.Text)
	if err != nil {
		return "", err
	}
	call := agent.ToolCallID(ctx)
	if call == "" || len(call) > maxCallID {
		return "", errors.New("this call has no id the reply can be kept by, so nothing was sent")
	}
	said, err := r.store.CountSaidInTree(ctx, r.run.RootRunID)
	if err != nil {
		return "", fmt.Errorf("nothing was sent: %w", err)
	}
	if said >= repliesPerTree {
		return "", fmt.Errorf("the person has been told %d things while this task went on, the most it tells them; "+
			"keep the rest for your answer", repliesPerTree)
	}

	seq, err := r.note(ctx, "said", stream.Said{ToolCallID: call, Agent: r.run.AgentName, Text: text})
	if err != nil {
		return "", fmt.Errorf("nothing was sent: %w", err)
	}
	if r.toChannel {
		if err := r.post(ctx, seq, text); err != nil {
			return "", err
		}
	}
	return "Sent. Carry on; your answer still comes at the end, without repeating this.", nil
}

// post queues what was said for the tree's channel and has it sent now. A
// delegate's words are its own, so they carry its agent's name. None written
// is a conversation that can no longer be posted to, which the model is told
// rather than told it went.
func (r *replying) post(ctx context.Context, seq int32, text string) error {
	if r.run.ParentRunID != nil {
		text = r.run.AgentName + ": " + text
	}
	n, err := r.store.InsertSaidReply(ctx, db.InsertSaidReplyParams{RunID: r.run.ID, EventSeq: seq, Text: text})
	if err != nil {
		return fmt.Errorf("nothing was sent: %w", err)
	}
	if n == 0 {
		return errors.New("nothing was sent: this conversation can no longer be posted to; say so in your answer")
	}
	if r.kick != nil {
		r.kick(seq)
	}
	return nil
}

// replyText is what a person is sent: line breaks kept, no other control
// characters, and one message long.
func replyText(s string) (string, error) {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || !unicode.IsControl(r) && r != ' ' && r != ' ' {
			return r
		}
		return -1
	}, s))
	if s == "" {
		return "", errors.New("there is nothing to send; say what to tell them")
	}
	if n := utf8.RuneCountInString(s); n > maxReply {
		return "", fmt.Errorf("that is %d characters; one message is %d at most, so keep the rest for your answer", n, maxReply)
	}
	return s, nil
}

// Replies is what a run tells its person through: the store, and the
// recorder writing what was said in turn with the run's own events. Without
// it no run is given reply.
type Replies struct {
	Store ReplyStore
}

// WithReplies gives a run whose tree answers a person's message reply, on
// the dashboard or a channel it can post to. A schedule's or a watcher's
// never has it: nobody is waiting, and its answer is its one message.
func (b *Builder) WithReplies(r Replies) *Builder {
	b.replies = &r
	return b
}

// replyFor is the tool for a run, when its tree answers a person: a
// delegate's as its root's. A replay holds a stand-in wherever a run might,
// as it has no conversation to ask.
func (b *Builder) replyFor(ctx context.Context, run db.Run, l Lineage) (agent.Tool, bool, error) {
	if b.replay {
		return (&replying{run: run}).tool(), true, nil
	}
	if b.replies == nil || b.replies.Store == nil || b.noteSeq == nil {
		return agent.Tool{}, false, nil
	}
	reach, err := b.replies.Store.GetReplyReach(ctx, run.ID)
	if err != nil {
		return agent.Tool{}, false, fmt.Errorf("reading whether the run answers a person: %w", err)
	}
	if !reach.Answers {
		return agent.Tool{}, false, nil
	}
	r := &replying{
		store: b.replies.Store, run: run, toChannel: !reach.Streamed,
		note: func(ctx context.Context, kind string, p any) (int32, error) {
			return b.noteSeq(ctx, run.ID, l.Worker, kind, p)
		},
	}
	if b.kickEvent != nil {
		r.kick = func(seq int32) { b.kickEvent(run.ID, seq) }
	}
	return r.tool(), true, nil
}
