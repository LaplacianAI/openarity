package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

// A run compacts at a tenth of its budget, held between the team's least and
// most tokens: under the least, what a compaction keeps is already over the line.
func TestARunCompactsAtATenthOfItsBudgetBetweenTheTeamsLines(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		least, most int32
		budget      int64
		want        int
	}{
		{50_000, 60_000, 5_000_000, 60_000},
		{50_000, 60_000, 700_000, 60_000},
		{50_000, 60_000, 550_000, 55_000},
		{50_000, 60_000, 500_000, 50_000},
		{50_000, 60_000, 1, 50_000},
		{40_000, 40_000, 2_000_000, 40_000},
		{40_000, 40_000, 100_000, 40_000},
		{30_000, 1_000_000, 2_000_000, 200_000},
		{100_000, 150_000, 500_000, 100_000},
	} {
		s := defaultTeamSettings()
		s.CompactionMessages, s.CompactionTokensMin, s.CompactionTokensMax = 42, tc.least, tc.most
		got := compactionFor(s, tc.budget)
		if got.AfterInputTokens != tc.want || got.AfterMessages != 42 {
			t.Errorf("compactionFor(%d to %d, budget %d) = %+v, want %d tokens and the team's 42 messages",
				tc.least, tc.most, tc.budget, got, tc.want)
		}
	}
}

// A team that never saved its settings compacts where the brain always did.
func TestATeamWithoutSettingsCompactsBetween50And60Thousand(t *testing.T) {
	t.Parallel()
	s := defaultTeamSettings()
	if s.CompactionTokensMin != 50_000 || s.CompactionTokensMax != 60_000 || s.CompactionMessages != defaultCompactionMessages {
		t.Errorf("defaults = %d to %d tokens, %d messages; want 50,000 to 60,000 and %d",
			s.CompactionTokensMin, s.CompactionTokensMax, s.CompactionMessages, defaultCompactionMessages)
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

// The next call is priced as the budget counts it. A 60k call that is 50k
// cached costs 15k, so 100k left of 200k is room for it; the same call with
// nothing cached costs 60k, and 100k is not room for two of it and an answer.
// A provider counting more cached than it read prices as all cached, 6k,
// never less: with 20k left that is too little room, not endless room.
func TestTheWrapUpPricesTheNextCallAsTheBudgetCountsIt(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		cached int
		left   int64
		wraps  bool
	}{
		"mostly cached":                     {50_000, 100_000, false},
		"nothing cached":                    {0, 100_000, true},
		"more cached than read, room":       {90_000, 100_000, false},
		"more cached than read, near spent": {90_000, 20_000, true},
	} {
		t.Run(name, func(t *testing.T) {
			l := newSoftLines(200_000, 200_000)
			l.hold(&fakeRun{})
			e := agent.UsageEvent{Usage: agent.Usage{InputTokens: 60_000, CachedInputTokens: c.cached, OutputTokens: 1_000}}
			if w := l.spent(200_000-c.left, e); (w != nil) != c.wraps {
				t.Errorf("wrap-up = %+v with %d left, want wrapped %v", w, c.left, c.wraps)
			}
		})
	}
}

// The default least is under the default most, or a run with a small budget
// would compact past a line it can never be under.
func TestTheDefaultCompactionLinesAreInOrder(t *testing.T) {
	t.Parallel()
	if defaultCompactionTokensMin >= defaultCompactionTokensMax {
		t.Errorf("defaultCompactionTokensMin %d is not under defaultCompactionTokensMax %d",
			defaultCompactionTokensMin, defaultCompactionTokensMax)
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

// textTool is a tool whose result is text: each line numbered, so a read of the
// middle names which line it got.
func textTool(name, text string) agent.Tool {
	return agent.Tool{
		Name: name, Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) { return text, nil },
	}
}

// numbered is n numbered lines of about 40 bytes.
func numbered(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line %05d of a file a review read whole\n", i+1)
	}
	return b.String()
}

// sentAfter runs one call of tool through a run as the brain builds one, then
// read_output with args when set, and is what the model was sent of each
// result.
func sentAfter(t *testing.T, tool agent.Tool, readArgs string) []agent.Message {
	t.Helper()
	turns := []turn{callTool("c1", tool.Name, `{}`)}
	if readArgs != "" {
		turns = append(turns, callTool("r1", agent.ReadOutputToolName, readArgs))
	}
	model := newScripted(append(turns, answer("done", 10, 5))...)
	runner, err := agent.New(func(agent.Endpoint) (agent.ModelClient, error) { return model, nil }, patterns.ReAct())
	if err != nil {
		t.Fatal(err)
	}
	spec := agent.Spec{
		Model: agent.ModelRef{Name: "m", MaxTokens: 100}, Pattern: agent.PatternReAct, MaxSteps: 4,
		Tools:      []agent.Tool{tool},
		Compaction: compactionFor(defaultTeamSettings(), maxTokens),
	}
	task := []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "review it"}}}}
	if _, err := runner.Run(t.Context(), spec, task, agent.Endpoint{}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := model.requests()
	var out []agent.Message
	for _, m := range reqs[len(reqs)-1].Messages {
		if m.Role == agent.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

// A result over 8 KiB reaches the model as its two ends and a handle, from
// the first request after it; one under is sent whole.
func TestAResultOver8KiBIsShownAsItsEnds(t *testing.T) {
	t.Parallel()

	big := numbered(500) // about 20 KiB
	got := sentAfter(t, textTool("Bash", big), "")
	if len(got) != 1 || strings.Contains(got[0].Text(), "line 00250") || !strings.Contains(got[0].Text(), "line 00001") ||
		!strings.Contains(got[0].Text(), "line 00500") || !strings.Contains(got[0].Text(), agent.ReadOutputToolName) {
		t.Fatalf("a 20 KiB result was sent as %d bytes; want its ends and a read_output handle", len(got[0].Text()))
	}
	if len(got[0].Text()) > 7<<10 {
		t.Errorf("the preview is %d bytes, want about 6 KiB", len(got[0].Text()))
	}

	small := numbered(170) // about 7 KiB
	if got := sentAfter(t, textTool("Bash", small), ""); len(got) != 1 || got[0].Text() != small {
		t.Errorf("a 7 KiB result was not sent whole")
	}
}

// What a preview leaves out, read_output reads back: the middle by its line.
func TestTheMiddleOfAPreviewedResultIsReadBack(t *testing.T) {
	t.Parallel()

	got := sentAfter(t, textTool("Bash", numbered(500)), `{"call":"c1","from_line":250,"lines":2}`)
	if len(got) != 2 || !strings.Contains(got[1].Text(), "line 00250") {
		t.Fatalf("read_output of line 250 gave %.200q", got[len(got)-1].Text())
	}
}

// A skill is the model's instructions, never cut.
func TestASkillOver8KiBIsSentWhole(t *testing.T) {
	t.Parallel()

	skill := numbered(300) // about 12 KiB
	if got := sentAfter(t, textTool(agent.SkillToolName, skill), ""); len(got) != 1 || got[0].Text() != skill {
		t.Errorf("a 12 KiB skill was cut")
	}
}
