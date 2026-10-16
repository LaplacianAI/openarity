package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

type EventStore interface {
	AppendRunEvent(ctx context.Context, arg db.AppendRunEventParams) (int32, error)
	AddRunUsage(ctx context.Context, arg db.AddRunUsageParams) (int64, error)
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

func record(ctx context.Context, s EventStore, runID uuid.UUID, worker string,
	events <-chan agent.Event, stop context.CancelCauseFunc,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		lost := false
		for e := range events {
			if lost {
				continue
			}
			err := recordOne(ctx, s, runID, worker, e)
			if err == nil {
				continue
			}
			lost = errors.Is(err, ErrLostRun)
			stop(err)
		}
	}()
	return done
}

func recordOne(ctx context.Context, s EventStore, runID uuid.UUID, worker string, e agent.Event) error {
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
		return recordUsage(ctx, s, runID, worker, e)
	}
	return nil
}

func recordUsage(ctx context.Context, s EventStore, runID uuid.UUID, worker string, e agent.UsageEvent) error {
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

	if spent > maxTokens {
		return ErrBudget
	}
	return nil
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
