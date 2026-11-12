package runtime

import (
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func TestARunCompactsAtATenthOfItsBudgetCappedAtTheCeiling(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		budget int64
		want   int
	}{
		{1_000_000, 100_000},
		{5_000_000, 100_000},
		{200_000, 20_000},
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
