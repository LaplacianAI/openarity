package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// countingModel answers with fixed usage and records how it was called.
type countingModel struct {
	completeUsage Usage
	streamEvents  []StreamEvent
	streamErr     error
	closed        bool
}

func (m *countingModel) Complete(context.Context, Request) (Response, error) {
	return Response{Message: Message{Role: RoleAssistant}, Usage: m.completeUsage}, nil
}

func (m *countingModel) Stream(context.Context, Request) (Stream, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	return &recordingStream{events: m.streamEvents, onClose: func() { m.closed = true }}, nil
}

type recordingStream struct {
	events  []StreamEvent
	cur     StreamEvent
	err     error
	onClose func()
}

func (s *recordingStream) Next() bool {
	if len(s.events) == 0 {
		return false
	}
	s.cur, s.events = s.events[0], s.events[1:]
	return true
}

func (s *recordingStream) Event() StreamEvent { return s.cur }
func (s *recordingStream) Err() error         { return s.err }
func (s *recordingStream) Close() error       { s.onClose(); return s.err }

// spender is a pattern that calls the model and reports no usage at all — the
// mistake a pattern written outside this module makes by omission, and the one
// counting at the client exists to absorb.
type spender struct {
	complete int
	stream   int
	fail     error
}

func (spender) Name() PatternName { return PatternCustom }

func (p spender) Run(ctx context.Context, in Input) (Result, error) {
	for range p.complete {
		if _, err := in.Model.Complete(ctx, Request{}); err != nil {
			return Result{}, err
		}
	}
	for range p.stream {
		s, err := in.Model.Stream(ctx, Request{})
		if err != nil {
			return Result{}, err
		}
		for s.Next() {
			_ = s.Event()
		}
		_ = s.Err()
		_ = s.Close()
	}
	return Result{Output: "done"}, p.fail
}

func run(t *testing.T, p Pattern, m ModelClient) (Result, error) {
	t.Helper()
	r, err := New(func(Endpoint) (ModelClient, error) { return m, nil }, p)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r.Run(t.Context(), Spec{Pattern: p.Name(), MaxSteps: 5}, nil,
		Endpoint{BaseURL: "http://gateway"}, nil)
}

func TestUsageAddTotalsEveryField(t *testing.T) {
	t.Parallel()

	got := Usage{InputTokens: 1, OutputTokens: 2, CachedInputTokens: 3}
	got.Add(Usage{InputTokens: 10, OutputTokens: 20, CachedInputTokens: 30})

	want := Usage{InputTokens: 11, OutputTokens: 22, CachedInputTokens: 33}
	if got != want {
		t.Errorf("Add() = %+v, want %+v", got, want)
	}
}

// The whole reason the count lives at the client: a pattern that never touches
// Result.Usage still reports what it spent.
func TestTheRunnerCountsForAPatternThatDoesNot(t *testing.T) {
	t.Parallel()

	final := Response{Usage: Usage{InputTokens: 20, OutputTokens: 6, CachedInputTokens: 2}}
	model := &countingModel{
		completeUsage: Usage{InputTokens: 10, OutputTokens: 4, CachedInputTokens: 1},
		streamEvents:  []StreamEvent{{Text: "hi"}, {Final: &final}},
	}

	got, err := run(t, spender{complete: 1, stream: 1}, model)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := Usage{InputTokens: 30, OutputTokens: 10, CachedInputTokens: 3}
	if got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
}

// A gateway repeating its final event on every chunk is a real mistake — the
// examples' own stub made it — and it must not double the bill.
func TestARepeatedFinalEventIsCountedOnce(t *testing.T) {
	t.Parallel()

	final := Response{Usage: Usage{InputTokens: 20, OutputTokens: 6}}
	model := &countingModel{streamEvents: []StreamEvent{
		{Final: &final}, {Final: &final}, {Final: &final},
	}}

	got, err := run(t, spender{stream: 1}, model)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (Usage{InputTokens: 20, OutputTokens: 6}); got.Usage != want {
		t.Errorf("Usage = %+v, want %+v — the final event counted three times", got.Usage, want)
	}
}

// A run that failed half way was still billed for what it managed.
func TestWhatAFailedRunSpentIsStillReported(t *testing.T) {
	t.Parallel()

	boom := errors.New("the gateway gave up")
	model := &countingModel{completeUsage: Usage{InputTokens: 10, OutputTokens: 4}}

	got, err := run(t, spender{complete: 2, fail: boom}, model)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the pattern's error", err)
	}
	if want := (Usage{InputTokens: 20, OutputTokens: 8}); got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
}

// A stream that never carries a final event spends nothing this can see, and
// must not be a failure of its own.
func TestAStreamWithNoFinalEventCountsNothing(t *testing.T) {
	t.Parallel()

	model := &countingModel{streamEvents: []StreamEvent{{Text: "a"}, {Text: "b"}}}
	got, err := run(t, spender{stream: 1}, model)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Usage != (Usage{}) {
		t.Errorf("Usage = %+v, want nothing", got.Usage)
	}
}

// The wrapper is transparent: a failure opening the stream reaches the pattern
// unchanged, and Event, Err and Close all reach the stream underneath.
func TestTheWrapperIsTransparent(t *testing.T) {
	t.Parallel()

	refused := errors.New("the gateway refused the stream")
	if _, err := run(t, spender{stream: 1}, &countingModel{streamErr: refused}); !errors.Is(err, refused) {
		t.Errorf("err = %v, want the gateway's error", err)
	}

	final := Response{Usage: Usage{InputTokens: 5}}
	model := &countingModel{streamEvents: []StreamEvent{{Text: "delta"}, {Final: &final}}}

	var seen []StreamEvent
	watcher := patternFunc(func(ctx context.Context, in Input) (Result, error) {
		s, err := in.Model.Stream(ctx, Request{})
		if err != nil {
			return Result{}, err
		}
		for s.Next() {
			seen = append(seen, s.Event())
		}
		if err := s.Err(); err != nil {
			return Result{}, err
		}
		return Result{}, s.Close()
	})

	if _, err := run(t, watcher, model); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 || seen[0].Text != "delta" || seen[1].Final == nil {
		t.Errorf("the pattern saw %+v, want both events unchanged", seen)
	}
	if !model.closed {
		t.Error("Close did not reach the stream underneath")
	}
}

// One counter serves a pattern free to call the model from several goroutines.
// Under -race this catches the total losing its lock.
func TestCountingIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	model := &countingModel{completeUsage: Usage{InputTokens: 1, OutputTokens: 1}}

	const callers = 50
	concurrent := patternFunc(func(ctx context.Context, in Input) (Result, error) {
		var wg sync.WaitGroup
		for range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = in.Model.Complete(ctx, Request{})
			}()
		}
		wg.Wait()
		return Result{}, nil
	})

	got, err := run(t, concurrent, model)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (Usage{InputTokens: callers, OutputTokens: callers}); got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
}

// patternFunc adapts a function to Pattern, so a test can describe one run
// without declaring a type for it.
type patternFunc func(context.Context, Input) (Result, error)

func (patternFunc) Name() PatternName { return PatternCustom }

func (f patternFunc) Run(ctx context.Context, in Input) (Result, error) { return f(ctx, in) }
