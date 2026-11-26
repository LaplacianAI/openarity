// Package triggers is what starts a run when nobody asked just then: a
// schedule, on the clock. Its rules are written once here, for the routes, an
// agent's tools and the jobs alike: who may make one, the team's limits, the
// minimum interval, where it answers, and confirming one an agent proposed.
//
// A schedule runs in a conversation of its own, as the person who made it,
// with their permissions when it fires. It is never deleted: a delete marks
// it so and closes its conversation, which stays with its runs for review.
package triggers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// runWrite is what a fire needs its maker to still hold.
const runWrite = "run:write"

const (
	// proposalLife is how long an agent's proposal waits to be confirmed.
	proposalLife = time.Hour
	// keepFires is how many fires each schedule keeps.
	keepFires = 200
)

// The team's limits when it never saved its own (D7).
const (
	defaultTimezone          = "UTC"
	defaultMinInterval       = 60
	defaultPerMember         = 5
	defaultPerTeam           = 20
	defaultFiresPerHour      = 20
	defaultAgentsMaySchedule = true
)

// What a refusal is, as the routes answer it.
type Kind int

const (
	// Invalid is a request that cannot be saved as asked.
	Invalid Kind = iota + 1
	// NotFound is none the caller may see.
	NotFound
	// Conflict is one whose state does not allow it: a name taken, one already
	// paused, a run still working.
	Conflict
	// Limit is a team limit reached.
	Limit
	// Forbidden is one the caller may see but not change.
	Forbidden
)

// An Error is a refusal said for a person: the routes pass it on as it is,
// and so will an agent's tools.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func refuse(k Kind, format string, args ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}

// KindOf is the refusal err is, or zero for a failure.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// Who is the caller: a member of a team, and whether they hold
// schedule:manage, which reaches everyone's.
type Who struct {
	Team, User uuid.UUID
	Manage     bool
}

// Origin is the run whose tool call this is. Zero is a person on the
// dashboard or with oa.
type Origin struct {
	Run, Session uuid.UUID
}

func (o Origin) viaTool() bool { return o.Run != uuid.Nil }

// Enqueuer hands a created run to the workers. A lost enqueue is started by
// the runtime's sweep.
type Enqueuer interface {
	Enqueue(ctx context.Context, runID uuid.UUID) error
}

// Store is what the rules read and write. Everything that decides is done in
// one transaction.
type Store interface {
	InTx(ctx context.Context, fn func(*db.Queries) error) error
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	GetScheduleView(ctx context.Context, arg db.GetScheduleViewParams) (db.GetScheduleViewRow, error)
	ListScheduleViews(ctx context.Context, arg db.ListScheduleViewsParams) ([]db.ListScheduleViewsRow, error)
	ListScheduleFires(ctx context.Context, arg db.ListScheduleFiresParams) ([]db.ScheduleFire, error)
	ListScheduleNews(ctx context.Context, arg db.ListScheduleNewsParams) ([]db.ListScheduleNewsRow, error)
	MarkScheduleSeen(ctx context.Context, arg db.MarkScheduleSeenParams) (int64, error)
	ScheduleIDByName(ctx context.Context, arg db.ScheduleIDByNameParams) (uuid.UUID, error)
}

// Service applies the rules. now is the clock; tests set it.
type Service struct {
	store  Store
	runs   Enqueuer
	logger *slog.Logger
	now    func() time.Time
}

// NewService is the rules over a store, handing the runs it starts to runs.
func NewService(s Store, runs Enqueuer, logger *slog.Logger) *Service {
	return &Service{store: s, runs: runs, logger: logger, now: time.Now}
}

// mayRun refuses a maker who may not start runs in the team: a schedule runs
// as them, so it would only pause itself at its first fire.
func mayRun(ctx context.Context, q *db.Queries, team, user uuid.UUID, k Kind, msg string) error {
	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: team, UserID: user, Action: runWrite})
	if err != nil {
		return fmt.Errorf("check the maker may start runs: %w", err)
	}
	if !may {
		return refuse(k, "%s", msg)
	}
	return nil
}

// Limits are a team's settings for schedules and watchers.
type Limits struct {
	AgentsMaySchedule bool
	Timezone          string
	MinInterval       time.Duration
	PerMember         int
	PerTeam           int
	FiresPerHour      int
}

// LimitsOf is a team's limits from its settings row; a team that never saved
// has the defaults.
func LimitsOf(row db.TeamSetting, err error) (Limits, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return Limits{
			AgentsMaySchedule: defaultAgentsMaySchedule, Timezone: defaultTimezone,
			MinInterval: defaultMinInterval * time.Minute, PerMember: defaultPerMember,
			PerTeam: defaultPerTeam, FiresPerHour: defaultFiresPerHour,
		}, nil
	}
	if err != nil {
		return Limits{}, fmt.Errorf("read the team's settings: %w", err)
	}
	return Limits{
		AgentsMaySchedule: row.AgentsMaySchedule, Timezone: row.Timezone,
		MinInterval: time.Duration(row.ScheduleMinIntervalMinutes) * time.Minute,
		PerMember:   int(row.SchedulesPerMember), PerTeam: int(row.SchedulesPerTeam),
		FiresPerHour: int(row.FiresPerHour),
	}, nil
}

func (s *Service) limits(ctx context.Context, team uuid.UUID) (Limits, error) {
	row, err := s.store.GetTeamSettings(ctx, team)
	return LimitsOf(row, err)
}

// underLimits counts the team's and the maker's live schedules and watchers,
// holding the team's lock so a second create waits for this one's count.
func underLimits(ctx context.Context, q *db.Queries, team, maker uuid.UUID, l Limits) error {
	if err := q.LockTeamTriggers(ctx, team); err != nil {
		return fmt.Errorf("lock the team's schedules: %w", err)
	}
	n, err := q.CountLiveTriggers(ctx, db.CountLiveTriggersParams{TeamID: team, Maker: maker})
	if err != nil {
		return fmt.Errorf("count the team's schedules: %w", err)
	}
	if int(n.Maker) >= l.PerMember {
		return refuse(Limit, "you already have %d schedules and watchers, which is this team's limit per person; delete one first", n.Maker)
	}
	if int(n.Team) >= l.PerTeam {
		return refuse(Limit, "this team already has %d schedules and watchers, which is its limit; an admin can raise it in Team settings", n.Team)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
