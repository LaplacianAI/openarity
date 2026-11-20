package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const (
	// compactCeiling is the most a run's next request may read before it
	// compacts. A run's own budget lowers it to a tenth of the budget, so no
	// single call reads more than that, but never below compactFloor: a
	// compaction keeps the latest ~15,000 tokens, the prompt and the tools,
	// so a lower line is crossed again at once and the run spends its budget
	// summarising. A small budget is kept by its soft lines instead.
	//
	// 60,000, not more: a review delegate at 100,000 spent its last ten or
	// twenty steps rereading tool output it was done with, and a compaction
	// on staging left about 26,000, so this still leaves room for several
	// steps between compactions.
	compactCeiling = 60_000
	compactFloor   = 50_000
	// defaultCompactionMessages is team_settings.compaction_messages' default,
	// for a team that never saved its settings.
	defaultCompactionMessages = 100

	budgetWrapUp = "Your token budget is nearly spent. Stop calling tools and answer now with what you have."
	timeWrapUp   = "Your time is nearly up. Stop calling tools and answer now with what you have."
	stepsWrapUp  = "You are at step %d of %d. Stop calling tools and answer now with what you have."
	// stepsLeft is how many steps before MaxSteps a run is asked to finish:
	// the recorder hears of a step as it starts, so the ask can land a step
	// late, and the model still has a step to answer in.
	stepsLeft = 2
	// minReserve is the least the wrap-up keeps for the run's answer, before
	// it has written anything to measure.
	minReserve = 16_000
	// timeWrapBefore is how long before a run's wall clock it is asked to finish.
	timeWrapBefore = 2 * time.Minute
)

// compactedPayload is a compaction as the trace shows it. A resume needs none
// of it: the summary is in the transcript.
type compactedPayload struct {
	Reason  string `json:"reason"`
	Cleared int    `json:"cleared"`
	Folded  int    `json:"folded"`
	Before  int    `json:"before"`
	After   int    `json:"after"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

type wrapUpPayload struct {
	Reason string `json:"reason"`
	Left   int64  `json:"left,omitempty"`
}

// teamSettings reads a team's settings once for a build: compaction and the
// web tools both need them. A team that never saved any has the defaults.
func (b *Builder) teamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error) {
	s, err := b.store.GetTeamSettings(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.TeamSetting{
			SearchProvider: noSearchProvider, BrowserProvider: noBrowserProvider, CompactionMessages: defaultCompactionMessages,
		}, nil
	}
	if err != nil {
		return db.TeamSetting{}, fmt.Errorf("reading the team's settings: %w", err)
	}
	return s, nil
}

// compactionFor is how a run compacts: past the team's message count, or past
// a tenth of the run's budget, held between the floor and the ceiling.
func compactionFor(messages int32, budget int64) *agent.Compaction {
	return &agent.Compaction{
		AfterMessages:    int(messages),
		AfterInputTokens: int(min(compactCeiling, max(budget/10, compactFloor))),
	}
}

// handle is what the recorder asks of a run while it is going.
type handle interface {
	CompactNow()
	WrapUp(text string) error
}

// softLines asks a run to compact, and then to finish, before its budget is
// gone. Each line is crossed once. The run is set after it starts, so events
// that arrive before it is set are counted without asking anything.
type softLines struct {
	budget int64
	maxOut int64
	// steps is the run's MaxSteps, set before it starts; zero asks nothing.
	steps int
	// wrote is the most one of the run's calls has written: what an answer
	// is measured by, rather than the model's max_tokens, which is often far
	// more than any answer and would wrap a run up with much of its budget
	// unspent.
	wrote int64

	mu  sync.Mutex
	run handle

	compacted bool // touched only by the recorder
	wrapped   atomic.Bool
}

func newSoftLines(budget, maxOut int64) *softLines {
	return &softLines{budget: budget, maxOut: maxOut}
}

func (l *softLines) hold(run handle) {
	l.mu.Lock()
	l.run = run
	l.mu.Unlock()
}

func (l *softLines) held() handle {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.run
}

// spent is called with each usage the run records. It returns the wrap-up to
// record when this call crossed that line.
func (l *softLines) spent(spent int64, e agent.UsageEvent) *wrapUpPayload {
	run := l.held()
	if run == nil {
		return nil
	}
	left := l.budget - spent

	if !l.compacted && left < l.budget/10 {
		l.compacted = true
		run.CompactNow()
	}
	// A summary call read the whole context, so its size says nothing of the
	// next call's, and what it wrote is no answer.
	if e.Summary {
		return nil
	}
	l.wrote = max(l.wrote, int64(e.Usage.OutputTokens))
	if left >= 2*costOf(e.Usage)+l.reserve() {
		return nil
	}
	if !l.wrap(budgetWrapUp) {
		return nil
	}
	return &wrapUpPayload{Reason: "budget", Left: left}
}

// stepped is called as each step starts. It returns the wrap-up to record
// when this step is stepsLeft from the run's last. A run of so few steps that
// the ask would refuse its tools almost at once is never asked.
func (l *softLines) stepped(step int) *wrapUpPayload {
	if l.steps <= 2*stepsLeft-1 || step < l.steps-stepsLeft {
		return nil
	}
	if !l.wrap(fmt.Sprintf(stepsWrapUp, step, l.steps)) {
		return nil
	}
	return &wrapUpPayload{Reason: "steps"}
}

// cacheShare is the part of a fresh token a cached one counts for: a tenth,
// as providers bill it. run_spent in the store counts a run's budget the same
// way; the two must agree.
const cacheShare = 10

// costOf is what a call's input cost the budget: a cached token a tenth.
func costOf(u agent.Usage) int64 {
	in := int64(u.InputTokens)
	cached := min(max(int64(u.CachedInputTokens), 0), in)
	return in - cached + cached/cacheShare
}

// reserve is what one more call is kept for its answer: twice the most the
// run has written, at least minReserve, never more than max_tokens.
func (l *softLines) reserve() int64 {
	return min(l.maxOut, max(2*l.wrote, minReserve))
}

// wrap asks the run to finish, once, whichever line asks first.
func (l *softLines) wrap(text string) bool {
	run := l.held()
	if run == nil || !l.wrapped.CompareAndSwap(false, true) {
		return false
	}
	return run.WrapUp(text) == nil
}

// stopped says whether a run ended on one of its limits rather than on its
// own, so what it had is kept as its output.
func stopped(cause error) bool {
	return errors.Is(cause, ErrBudget) || errors.Is(cause, errWallClock)
}
