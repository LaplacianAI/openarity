// Package replay tries a proposed change to an agent on a few of its past
// runs before anyone decides on it, and keeps what came of each: the I6
// slice of the self-improving agents design.
//
// A try runs the past run again on the harness the proposal would leave
// (candidate.go), through the runtime's Replayer, which acts on nothing and
// writes nothing; the I2 judge decides it as it decided the run. This
// package chooses the runs and keeps the rows.
package replay

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const (
	// goodWithin is how recent a good run must be to be tried on.
	goodWithin = 14 * 24 * time.Hour
	// eventsPage is how many of a past run's events are read at once.
	eventsPage = 1000
	// answerBytes and traceBytes are what a try's row keeps; errorBytes is
	// what it keeps of why a try failed.
	answerBytes = 16 << 10
	traceBytes  = 256 << 10
	errorBytes  = 2000
	// storeTries is how many times a read or a write of a try is made
	// before the try is given up on; the model is never run again for it.
	storeTries = 3
)

// Why a run was chosen.
const (
	chosenCited = "cited"
	chosenGood  = "good"
)

// A try's status.
const (
	statusQueued  = "queued"
	statusSkipped = "skipped"
)

// A proposal's status, as a try reads it.
const (
	proposalWaiting = "waiting"
	proposalApplied = "applied"
)

// A run's outcome, and a try's.
const (
	outcomeGood     = "good"
	outcomeBad      = "bad"
	outcomeCantTell = "cant_tell"
)

// Why the tries still to run of a proposal are skipped.
const (
	whyDecided  = "the proposal was decided before it was tried"
	whyOff      = "trying changes on past runs was turned off"
	whyDailyCap = "the team's tries for today are used"
)

// Store is what a replay reads and writes.
type Store interface {
	runtime.BuildStore
	CandidateReads
	GetReplaySettings(ctx context.Context, teamID uuid.UUID) (db.GetReplaySettingsRow, error)
	GetImprovementProposal(ctx context.Context, arg db.GetImprovementProposalParams) (db.ImprovementProposal, error)
	GetRunStartedSeq(ctx context.Context, runID uuid.UUID) (int64, error)
	ListRunEvents(ctx context.Context, arg db.ListRunEventsParams) ([]db.RunEvent, error)
	ListReplayRuns(ctx context.Context, arg db.ListReplayRunsParams) ([]db.ListReplayRunsRow, error)
	ListReplayGoodRunIDs(ctx context.Context, arg db.ListReplayGoodRunIDsParams) ([]uuid.UUID, error)
	InsertReplay(ctx context.Context, arg db.InsertReplayParams) error
	MarkReplayChosen(ctx context.Context, arg db.MarkReplayChosenParams) error
	ListQueuedReplays(ctx context.Context, proposalID uuid.UUID) ([]uuid.UUID, error)
	// ClaimReplay begins a try under the team's lock, unless the day's tries
	// are used: store.Store's.
	ClaimReplay(ctx context.Context, arg db.StartReplayParams) (int64, error)
	FinishReplay(ctx context.Context, arg db.FinishReplayParams) error
	FailReplay(ctx context.Context, arg db.FailReplayParams) error
	SkipQueuedReplays(ctx context.Context, arg db.SkipQueuedReplaysParams) error
}

// Runner replays a run: the runtime's Replayer.
type Runner interface {
	Replay(ctx context.Context, in runtime.ReplayInput) (runtime.Replayed, error)
}

// Judge decides a replay: the improve Checker.
type Judge interface {
	JudgeReplay(ctx context.Context, runID uuid.UUID, r improve.Replay) (improve.ReplayJudgement, error)
}

// Replays tries proposals on past runs.
type Replays struct {
	store    Store
	objects  runtime.Objects
	sessions agent.Store
	runner   Runner
	judge    Judge
	now      func() time.Time
	// minute is how long a minute of a team's replay minutes is.
	minute time.Duration
	// pause is how long a read or write of a try waits before it is made
	// again.
	pause time.Duration
}

// New tries proposals with store, reading skill files from objects and past
// runs' transcripts from sessions (never writing either).
func New(s Store, objects runtime.Objects, sessions agent.Store, r Runner, j Judge) *Replays {
	return &Replays{
		store: s, objects: objects, sessions: sessions, runner: r, judge: j,
		now: time.Now, minute: time.Minute, pause: time.Second,
	}
}

// errStop ends a proposal's tries: what is left was skipped, saying why.
var errStop = errors.New("the proposal's tries are stopped")

// Stopped is whether err says a proposal's tries are over.
func Stopped(err error) bool { return errors.Is(err, errStop) }
