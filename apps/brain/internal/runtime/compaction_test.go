package runtime

import (
	"fmt"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// A run compacts at a tenth of its budget, never past the ceiling, and never
// below the floor: under it, what a compaction keeps is already over the line.
func TestARunCompactsAtATenthOfItsBudgetBetweenTheFloorAndTheCeiling(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		budget int64
		want   int
	}{
		{5_000_000, 100_000},
		{1_000_000, 100_000},
		{700_000, 70_000},
		{500_000, 50_000},
		{300_000, 50_000},
		{150_000, 50_000},
		{100_000, 50_000},
		{1, 50_000},
	} {
		got := compactionFor(42, tc.budget)
		if got.AfterInputTokens != tc.want || got.AfterMessages != 42 {
			t.Errorf("compactionFor(42, %d) = %+v, want %d tokens and the team's 42 messages", tc.budget, got, tc.want)
		}
	}
}

type fakeRun struct {
	compacted int
	wrapped   []string
	wrapErr   error
}

func (f *fakeRun) CompactNow() { f.compacted++ }

func (f *fakeRun) WrapUp(text string) error {
	f.wrapped = append(f.wrapped, text)
	return f.wrapErr
}

func used(input int) agent.UsageEvent {
	return agent.UsageEvent{Usage: agent.Usage{InputTokens: input}}
}

func TestTheSoftLinesCompactFirstThenWrapUpEachOnce(t *testing.T) {
	t.Parallel()
	run := &fakeRun{}
	l := newSoftLines(1_000_000, 4_000)
	l.hold(run)

	// Plenty left: nothing.
	if w := l.spent(500_000, used(20_000)); w != nil || run.compacted != 0 {
		t.Fatalf("at half the budget: wrap %+v, compacted %d; want nothing", w, run.compacted)
	}
	// A tenth left, and calls still small: compact, and work on.
	if w := l.spent(910_000, used(20_000)); w != nil || run.compacted != 1 {
		t.Fatalf("at a tenth left: wrap %+v, compacted %d; want one compaction and no wrap-up", w, run.compacted)
	}
	if l.spent(920_000, used(20_000)); run.compacted != 1 {
		t.Fatalf("compacted %d times; the line is crossed once", run.compacted)
	}
	// One more call would not fit: 2 × 20k + 4k > 40k left.
	w := l.spent(960_000, used(20_000))
	if w == nil || w.Reason != "budget" || w.Left != 40_000 || len(run.wrapped) != 1 || run.wrapped[0] != budgetWrapUp {
		t.Fatalf("one call short: wrap %+v, asked %v; want the budget's wrap-up", w, run.wrapped)
	}
	if w := l.spent(980_000, used(5_000)); w != nil || len(run.wrapped) != 1 {
		t.Fatalf("asked to wrap up %d times; once is the line", len(run.wrapped))
	}
}

func wrote(input, output int) agent.UsageEvent {
	return agent.UsageEvent{Usage: agent.Usage{InputTokens: input, OutputTokens: output}}
}

// The wrap-up keeps room for the answer the run writes, not for the model's
// max_tokens: an agent allowed 200k of output that writes 3k answers is not
// told to stop with a fifth of its budget unspent.
func TestTheWrapUpKeepsRoomForWhatTheRunWrites(t *testing.T) {
	t.Parallel()
	run := &fakeRun{}
	l := newSoftLines(1_000_000, 200_000)
	l.hold(run)

	// 300k left, 50k a call, 3k answers: room for many more calls.
	if w := l.spent(700_000, wrote(50_000, 3_000)); w != nil {
		t.Fatalf("wrapped up with 300k left and 3k answers: %+v", w)
	}
	// 115k left: one call is 2 × 50k + the 16k floor; it fits no longer.
	if w := l.spent(885_000, wrote(50_000, 3_000)); w == nil || w.Left != 115_000 {
		t.Fatalf("wrap %+v, want the wrap-up with 115k left", w)
	}
}

func TestTheReserveFollowsTheLargestAnswer(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		maxOut int64
		writes []int
		want   int64
	}{
		"nothing written yet":      {200_000, nil, 16_000},
		"small answers":            {200_000, []int{1_000, 3_000, 2_000}, 16_000},
		"a long answer":            {200_000, []int{2_000, 40_000, 5_000}, 80_000},
		"never past max_tokens":    {200_000, []int{150_000}, 200_000},
		"a small max_tokens rules": {4_000, []int{1_000}, 4_000},
	} {
		t.Run(name, func(t *testing.T) {
			l := newSoftLines(1_000_000, c.maxOut)
			l.hold(&fakeRun{})
			for _, w := range c.writes {
				l.spent(0, wrote(1_000, w))
			}
			if got := l.reserve(); got != c.want {
				t.Errorf("reserve = %d, want %d", got, c.want)
			}
		})
	}
}

// A summary's output is a summary, not an answer: it does not grow the reserve.
func TestASummaryDoesNotGrowTheReserve(t *testing.T) {
	t.Parallel()
	l := newSoftLines(1_000_000, 200_000)
	l.hold(&fakeRun{})
	summary := wrote(300_000, 60_000)
	summary.Summary = true
	l.spent(400_000, summary)
	if got := l.reserve(); got != minReserve {
		t.Errorf("reserve = %d after a summary, want %d", got, minReserve)
	}
}

func TestASummaryCallsSizeDoesNotWrapUpTheRun(t *testing.T) {
	t.Parallel()
	run := &fakeRun{}
	l := newSoftLines(1_000_000, 4_000)
	l.hold(run)

	summary := used(300_000)
	summary.Summary = true
	if w := l.spent(400_000, summary); w != nil || len(run.wrapped) != 0 {
		t.Fatalf("a summary that read 300k wrapped the run up: %+v", w)
	}
}

func TestNoLineIsAskedBeforeTheRunIsHeld(t *testing.T) {
	t.Parallel()
	l := newSoftLines(100, 10)

	if w := l.spent(99, used(50)); w != nil {
		t.Fatalf("wrap %+v before the run was held", w)
	}
}

func TestTheClockAndTheBudgetWrapUpOnceBetweenThem(t *testing.T) {
	t.Parallel()
	run := &fakeRun{}
	l := newSoftLines(1_000_000, 4_000)
	l.hold(run)

	if !l.wrap(timeWrapUp) {
		t.Fatal("the clock's wrap-up was refused")
	}
	if w := l.spent(999_000, used(20_000)); w != nil {
		t.Fatalf("the budget wrapped up a run the clock had: %+v", w)
	}
	if len(run.wrapped) != 1 || run.wrapped[0] != timeWrapUp {
		t.Fatalf("asked %v; want the clock's alone", run.wrapped)
	}
}

func TestAWrapUpTheRunRefusesIsNotRecorded(t *testing.T) {
	t.Parallel()
	run := &fakeRun{wrapErr: agent.ErrRunFinished}
	l := newSoftLines(1_000_000, 4_000)
	l.hold(run)

	if w := l.spent(999_000, used(20_000)); w != nil {
		t.Fatalf("recorded a wrap-up the run refused: %+v", w)
	}
}

func TestOnlyALimitKeepsWhatARunHad(t *testing.T) {
	t.Parallel()

	for cause, want := range map[error]bool{
		ErrBudget:    true,
		errWallClock: true,
		ErrCancelled: false,
		ErrLostRun:   false,
		nil:          false,
	} {
		if got := stopped(cause); got != want {
			t.Errorf("stopped(%v) = %v, want %v", cause, got, want)
		}
	}
}

// A run is asked to finish stepsLeft before its last step, once; a run too
// short to spare the steps is never asked.
func TestARunNearItsLastStepIsAskedToWrapUpOnce(t *testing.T) {
	t.Parallel()
	run := &fakeRun{}
	l := newSoftLines(1_000_000, 4_000)
	l.steps = 30
	l.hold(run)

	if w := l.stepped(27); w != nil {
		t.Fatalf("at step 27 of 30: %+v, want nothing", w)
	}
	w := l.stepped(28)
	if w == nil || w.Reason != "steps" || len(run.wrapped) != 1 || run.wrapped[0] != fmt.Sprintf(stepsWrapUp, 28, 30) {
		t.Fatalf("at step 28 of 30: %+v, asked %v; want the steps' wrap-up", w, run.wrapped)
	}
	if w := l.stepped(29); w != nil || len(run.wrapped) != 1 {
		t.Fatalf("asked %d times; once is the line", len(run.wrapped))
	}

	short := &fakeRun{}
	s := newSoftLines(1_000_000, 4_000)
	s.steps = 3
	s.hold(short)
	for step := 1; step <= 3; step++ {
		if w := s.stepped(step); w != nil {
			t.Fatalf("a run of 3 steps was asked at step %d", step)
		}
	}
	four := newSoftLines(1_000_000, 4_000)
	four.steps = 4
	four.hold(&fakeRun{})
	if four.stepped(1) != nil || four.stepped(2) == nil {
		t.Fatal("a run of 4 steps should be asked at step 2, not before")
	}
}
