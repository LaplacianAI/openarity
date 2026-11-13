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

// textPayload is a piece of the model's text, in the order it was written.
type textPayload struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// record writes a run's events in order, as its only writer. When streams is
// set, the model's text is written too, gathered into text events; it is
// flushed before any other event, so text and the call it led to stay in order.
func record(ctx context.Context, s EventStore, runID uuid.UUID, worker string, lines *softLines, streams bool,
	events <-chan agent.Event, notes <-chan note, stop context.CancelCauseFunc,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := &recorder{ctx: ctx, s: s, runID: runID, worker: worker, stop: stop}
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
				if t, isText := e.(agent.TextEvent); isText {
					if streams {
						r.text.WriteString(t.Delta)
						if r.text.Len() >= textFlushBytes {
							r.flush()
						}
					}
					continue
				}
				r.flush()
				if !r.lost {
					r.failed(recordOne(ctx, s, runID, worker, lines, e))
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
	lost   bool
}

// flush writes the text gathered so far, if any.
func (r *recorder) flush() {
	if r.text.Len() == 0 {
		return
	}
	text, cut := clip(r.text.String())
	r.text.Reset()
	if r.lost {
		return
	}
	r.failed(appendEvent(r.ctx, r.s, r.runID, r.worker, "text", textPayload{Text: text, Truncated: cut}))
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

func recordOne(ctx context.Context, s EventStore, runID uuid.UUID, worker string, lines *softLines, e agent.Event) error {
	switch e := e.(type) {
	case agent.StepEvent:
		return appendEvent(ctx, s, runID, worker, "step", stepPayload{Step: e.Step})
	case agent.ToolCallEvent:
		args, cut := clip(string(e.Arguments))
		return appendEvent(ctx, s, runID, worker, "tool_call", toolCallPayload{
			ID: e.ID, Name: e.Name, Arguments: args, Truncated: cut,
		})
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
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding the %s event: %w", kind, err)
	}

	_, err = s.AppendRunEvent(ctx, db.AppendRunEventParams{RunID: runID, Kind: kind, Payload: raw, Worker: worker})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLostRun
	}
	if err != nil {
		return fmt.Errorf("recording the %s event: %w", kind, err)
	}
	return nil
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
