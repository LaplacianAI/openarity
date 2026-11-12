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
	// single call spends more than that.
	compactCeiling = 100_000
	// defaultCompactionMessages is team_settings.compaction_messages' default,
	// for a team that never saved its settings.
	defaultCompactionMessages = 100

	budgetWrapUp = "Your token budget is nearly spent. Stop calling tools and answer now with what you have."
	timeWrapUp   = "Your time is nearly up. Stop calling tools and answer now with what you have."
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
		return db.TeamSetting{SearchProvider: noSearchProvider, CompactionMessages: defaultCompactionMessages}, nil
	}
	if err != nil {
		return db.TeamSetting{}, fmt.Errorf("reading the team's settings: %w", err)
	}
	return s, nil
}

// compactionFor is how a run compacts: past the team's message count, or past
// the smaller of the ceiling and a tenth of the run's budget.
func compactionFor(messages int32, budget int64) *agent.Compaction {
	return &agent.Compaction{
		AfterMessages:    int(messages),
		AfterInputTokens: int(min(compactCeiling, budget/10)),
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
	// next call's.
	if e.Summary || left >= 2*int64(e.Usage.InputTokens)+l.maxOut {
		return nil
	}
	if !l.wrap(budgetWrapUp) {
		return nil
	}
	return &wrapUpPayload{Reason: "budget", Left: left}
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
