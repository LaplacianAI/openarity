package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// saying holds a tree's count and its next said event together, so delegates
// calling reply at once cannot both pass at nine. A tree runs in one process
// (a delegate on its parent's tool call, a resumed one adopted by its root),
// so a lock in the process is the tree's; trees share a stripe only by chance
// of their id, and wait for each other's few milliseconds.
var saying [64]sync.Mutex

func sayingFor(root uuid.UUID) *sync.Mutex { return &saying[root[15]%byte(len(saying))] }

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
	if r.store == nil {
		return "", errors.New("nothing was sent: this run has no conversation to tell")
	}
	text, err := replyText(a.Text, r.prefix())
	if err != nil {
		return "", err
	}
	call, err := callOf(ctx)
	if err != nil {
		return "", fmt.Errorf("nothing was sent: %w", err)
	}
	seq, err := r.say(ctx, call, text)
	if err != nil {
		return "", err
	}
	if r.toChannel {
		if err := r.post(ctx, seq, text); err != nil {
			return "", err
		}
	}
	return "Sent. Carry on; your answer still comes at the end, without repeating this.", nil
}

// say writes the said event when the tree has room for one: the count and
// the write held together, and nothing else, so a post's latency is no other
// tree's wait.
func (r *replying) say(ctx context.Context, call, text string) (int32, error) {
	mu := sayingFor(r.run.RootRunID)
	mu.Lock()
	defer mu.Unlock()
	said, err := r.store.CountSaidInTree(ctx, r.run.RootRunID)
	if err != nil {
		return 0, fmt.Errorf("nothing was sent: %w", err)
	}
	if said >= repliesPerTree {
		return 0, fmt.Errorf("the person has been told %d things while this task went on, the most it tells them; "+
			"keep the rest for your answer", repliesPerTree)
	}
	seq, err := r.note(ctx, "said", stream.Said{ToolCallID: call, Agent: r.run.AgentName, Text: text})
	if err != nil {
		return 0, fmt.Errorf("nothing was sent: %w", err)
	}
	return seq, nil
}

// prefix is what a delegate's words carry on a channel: its agent's name, as
// they are its own. A root's carry nothing.
func (r *replying) prefix() string {
	if r.run.ParentRunID == nil || !r.toChannel {
		return ""
	}
	return r.run.AgentName + ": "
}

// post queues what was said for the tree's channel and has it sent now. The
// said event is already written, so a post that fails leaves it in the run's
// trace and counted, and the model is told it did not reach the person, not
// to try again: the event stands as the record of what it meant to say.
func (r *replying) post(ctx context.Context, seq int32, text string) error {
	n, err := r.store.InsertSaidReply(ctx, db.InsertSaidReplyParams{RunID: r.run.ID, EventSeq: seq, Text: r.prefix() + text})
	if err != nil {
		return fmt.Errorf("it did not reach the person; do not send it again, put it in your answer: %w", err)
	}
	if n == 0 {
		return errors.New("it did not reach the person: this conversation can no longer be posted to; say so in your answer")
	}
	if r.kick != nil {
		r.kick(seq)
	}
	return nil
}

// replyText is what a person is sent: line breaks kept, a tab a space, no
// other control character and no invisible format mark (one that reorders
// the text around it among them), and one message long with prefix. Joiners
// and tags stay, as emoji and flags are made of them.
func replyText(s, prefix string) (string, error) {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r), r == '\u2028', r == '\u2029':
			return -1
		case unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d' && (r < 0xE0020 || r > 0xE007F):
			return -1
		}
		return r
	}, s))
	if s == "" {
		return "", errors.New("there is nothing to send; say what to tell them")
	}
	if most := maxReply - utf8.RuneCountInString(prefix); utf8.RuneCountInString(s) > most {
		return "", fmt.Errorf("that is %d characters; one message is %d at most, so keep the rest for your answer",
			utf8.RuneCountInString(s), most)
	}
	return s, nil
}

// WithReplies gives a run whose tree answers a person's message reply, on
// the dashboard or a channel it can post to, through s. A schedule's or a
// watcher's never has it: nobody is waiting, and its answer is its one
// message. Without it no run is given reply.
func (b *Builder) WithReplies(s ReplyStore) *Builder {
	b.replies = s
	return b
}

// replyFor is the tool for a run, when its tree answers a person: a
// delegate's as its root's. A replay holds a stand-in wherever a run might,
// as it has no conversation to ask; called, it says so and sends nothing.
func (b *Builder) replyFor(ctx context.Context, run db.Run, l Lineage) (agent.Tool, bool, error) {
	if b.replay {
		return (&replying{run: run}).tool(), true, nil
	}
	if b.replies == nil || b.noteSeq == nil {
		return agent.Tool{}, false, nil
	}
	reach, err := b.replies.GetReplyReach(ctx, run.ID)
	if err != nil {
		return agent.Tool{}, false, fmt.Errorf("reading whether the run answers a person: %w", err)
	}
	if !reach.Answers {
		return agent.Tool{}, false, nil
	}
	r := &replying{
		store: b.replies, run: run, toChannel: !reach.Streamed,
		note: func(ctx context.Context, kind string, p any) (int32, error) {
			return b.noteSeq(ctx, run.ID, l.Worker, kind, p)
		},
	}
	if b.kickEvent != nil {
		r.kick = func(seq int32) { b.kickEvent(run.ID, seq) }
	}
	return r.tool(), true, nil
}
