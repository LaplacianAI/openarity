package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

type EventStore interface {
	AppendRunEvent(ctx context.Context, arg db.AppendRunEventParams) (int32, error)
	AddRunUsage(ctx context.Context, arg db.AddRunUsageParams) (int64, error)
	// ConsumeSteer records the run taking the message behind a steer it read.
	ConsumeSteer(ctx context.Context, runID uuid.UUID) error
}

const (
	maxEventText = 64 << 10
	maxTokens    = 1_000_000
)

var (
	ErrLostRun = errors.New("another worker holds this run now")
	ErrBudget  = errors.New("the run has spent its token budget")
)

type stepPayload struct {
	Step int `json:"step"`
}

type toolCallPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Truncated bool   `json:"truncated"`
}

type toolResultPayload struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`
}

type usagePayload struct {
	Model             string `json:"model"`
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens"`
	Spent             int64  `json:"spent"`
}

// note is one of the brain's own events, written in turn with the agent's;
// done gets what the write returned.
type note struct {
	kind    string
	payload any
	done    chan error
}

const (
	// textFlushBytes and textFlushEvery are how much of a streamed reply is
	// held before it is written: often enough to read as it is typed, rarely
	// enough that a reply is tens of rows, not thousands.
	textFlushBytes = 512
	textFlushEvery = 250 * time.Millisecond
)

// textPayload is a piece of the model's text or reasoning, in the order it
// was written, and the step whose model call wrote it.
type textPayload struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Step      int    `json:"step,omitempty"`
}

// The kinds a run's gathered writing is recorded as.
const (
	kindText      = "text"
	kindReasoning = "reasoning"
)

// record writes a run's events in order, as its only writer. When streams is
// set, the model's text and reasoning are written too, each gathered into
// events of its own kind; what is gathered is flushed before any other event,
// and before the other kind, so the two never share a row and each stays in
// order with the call it led to. When tell is set, the steps a person would
// follow are told to the run's conversation, each after the event it comes
// from is written.
func record(ctx context.Context, s EventStore, runID uuid.UUID, worker string, lines *softLines, streams bool,
	tell func(seq int32, text string), events <-chan agent.Event, notes <-chan note, stop context.CancelCauseFunc,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := &recorder{ctx: ctx, s: s, runID: runID, worker: worker, stop: stop, tell: tell}
		var tick <-chan time.Time
		if streams {
			t := time.NewTicker(textFlushEvery)
			defer t.Stop()
			tick = t.C
		}
		for {
			select {
			case e, ok := <-events:
				if !ok {
					r.flush()
					return
				}
				if kind, delta, writing := writingOf(e); writing {
					if streams {
						r.gather(kind, delta)
					}
					continue
				}
				r.flush()
				if s, isStep := e.(agent.StepEvent); isStep {
					r.step = s.Step
					r.said.Reset()
				}
				if !r.lost {
					seq, err := recordOne(ctx, s, runID, worker, lines, e)
					r.failed(err)
					if call, isCall := e.(agent.ToolCallEvent); isCall && err == nil {
						r.told(call, seq)
					}
				}
			case n := <-notes:
				r.flush()
				// A run this worker lost refuses the write itself, with ErrLostRun.
				n.done <- appendEvent(ctx, s, runID, worker, n.kind, n.payload)
			case <-tick:
				r.flush()
			}
		}
	}()
	return done
}

type recorder struct {
	ctx    context.Context
	s      EventStore
	runID  uuid.UUID
	worker string
	stop   context.CancelCauseFunc
	text   strings.Builder
	kind   string // what text holds: kindText or kindReasoning
	step   int    // the step the model call writing is in
	lost   bool

	// tell, when set, sends a step to the conversation; said is the text the
	// current step has written, and textSeq the event that last held it.
	tell    func(seq int32, text string)
	said    strings.Builder
	textSeq int32
}

// told tells the conversation what a step said before it called a tool, once,
// and then the call, if it is one a person would follow. The step's text with
// no call after it is the answer, which goes as the answer.
func (r *recorder) told(call agent.ToolCallEvent, seq int32) {
	if r.tell == nil {
		return
	}
	if text := strings.TrimSpace(r.said.String()); text != "" && r.textSeq > 0 {
		r.tell(r.textSeq, text)
	}
	r.said.Reset()
	if line, ok := spoken(call); ok {
		r.tell(seq, line)
	}
}

// writingOf is the kind and piece of an event that is the model writing.
func writingOf(e agent.Event) (kind, delta string, ok bool) {
	switch e := e.(type) {
	case agent.TextEvent:
		return kindText, e.Delta, true
	case agent.ReasoningEvent:
		return kindReasoning, e.Delta, true
	}
	return "", "", false
}

func (r *recorder) gather(kind, delta string) {
	if kind != r.kind {
		r.flush()
		r.kind = kind
	}
	r.text.WriteString(delta)
	if kind == kindText && r.tell != nil {
		r.said.WriteString(delta)
	}
	if r.text.Len() >= textFlushBytes {
		r.flush()
	}
}

// flush writes what is gathered so far, if anything.
func (r *recorder) flush() {
	if r.text.Len() == 0 {
		return
	}
	text, cut := clip(r.text.String())
	r.text.Reset()
	if r.lost {
		return
	}
	seq, err := appendEventSeq(r.ctx, r.s, r.runID, r.worker, r.kind, textPayload{Text: text, Truncated: cut, Step: r.step})
	r.failed(err)
	if err == nil && r.kind == kindText {
		r.textSeq = seq
	}
}

// failed stops the run on a write that failed; a run another worker holds
// now is written to no more.
func (r *recorder) failed(err error) {
	if err == nil {
		return
	}
	r.lost = errors.Is(err, ErrLostRun)
	r.stop(err)
}

// recordOne writes one event and returns its seq: a tool call's is what a
// step told of it is named by. It is 0 for an event written as none.
func recordOne(ctx context.Context, s EventStore, runID uuid.UUID, worker string, lines *softLines, e agent.Event) (int32, error) {
	if call, ok := e.(agent.ToolCallEvent); ok {
		args, cut := clip(string(call.Arguments))
		return appendEventSeq(ctx, s, runID, worker, "tool_call", toolCallPayload{
			ID: call.ID, Name: call.Name, Arguments: args, Truncated: cut,
		})
	}
	return 0, recordOther(ctx, s, runID, worker, lines, e)
}

func recordOther(ctx context.Context, s EventStore, runID uuid.UUID, worker string, lines *softLines, e agent.Event) error {
	switch e := e.(type) {
	case agent.StepEvent:
		return appendEvent(ctx, s, runID, worker, "step", stepPayload{Step: e.Step})
	case agent.ToolResultEvent:
		output, cutOutput := clip(e.Output)
		failure, cutError := clip(e.Err)
		return appendEvent(ctx, s, runID, worker, "tool_result", toolResultPayload{
			ID: e.ID, Name: e.Name, Output: output, Error: failure,
			Truncated: cutOutput || cutError, DurationMS: e.Duration.Milliseconds(),
		})
	case agent.UsageEvent:
		return recordUsage(ctx, s, runID, worker, lines, e)
	case agent.SteerEvent:
		if err := s.ConsumeSteer(ctx, runID); err != nil {
			return fmt.Errorf("recording the message behind a steer: %w", err)
		}
		return nil
	case agent.CompactedEvent:
		return appendEvent(ctx, s, runID, worker, "compacted", compactedPayload{
			Reason: e.Reason, Cleared: e.Cleared, Folded: e.Folded, Before: e.Before, After: e.After,
			Summary: e.Summary, Error: e.Err,
		})
	}
	return nil
}

func recordUsage(ctx context.Context, s EventStore, runID uuid.UUID, worker string, lines *softLines, e agent.UsageEvent) error {
	spent, err := s.AddRunUsage(ctx, db.AddRunUsageParams{
		InputTokens: int64(e.Usage.InputTokens), OutputTokens: int64(e.Usage.OutputTokens),
		ID: runID, Worker: worker,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLostRun
	}
	if err != nil {
		return fmt.Errorf("counting the run's tokens: %w", err)
	}

	if err := appendEvent(ctx, s, runID, worker, "usage", usagePayload{
		Model: e.Model, InputTokens: e.Usage.InputTokens, OutputTokens: e.Usage.OutputTokens,
		CachedInputTokens: e.Usage.CachedInputTokens, Spent: spent,
	}); err != nil {
		return err
	}

	if spent > lines.budget {
		return ErrBudget
	}
	if wrap := lines.spent(spent, e); wrap != nil {
		return appendEvent(ctx, s, runID, worker, "wrap_up", wrap)
	}
	return nil
}

func budgetOf(set *int64) int64 {
	if set == nil {
		return maxTokens
	}
	return *set
}

func appendEvent(ctx context.Context, s EventStore, runID uuid.UUID, worker, kind string, payload any) error {
	_, err := appendEventSeq(ctx, s, runID, worker, kind, payload)
	return err
}

// appendEventSeq is appendEvent, returning the seq the event was written at.
func appendEventSeq(ctx context.Context, s EventStore, runID uuid.UUID, worker, kind string, payload any) (int32, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encoding the %s event: %w", kind, err)
	}

	seq, err := s.AppendRunEvent(ctx, db.AppendRunEventParams{RunID: runID, Kind: kind, Payload: raw, Worker: worker})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrLostRun
	}
	if err != nil {
		return 0, fmt.Errorf("recording the %s event: %w", kind, err)
	}
	return seq, nil
}

func clip(s string) (string, bool) {
	s = strings.ReplaceAll(s, "\x00", "\uFFFD")
	if len(s) <= maxEventText {
		return s, false
	}

	cut := maxEventText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
