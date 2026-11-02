package purge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	purgeCron     = "0 0 * * * *"
	purgeWorkflow = "openarity.teams.purge"
	purgeSchedule = "team-purge"

	// batchSize is how many teams one statement purges. A team's cascade
	// reaches every row it holds, so a statement stays small enough to keep
	// its locks short.
	batchSize = 10

	// fallbackLimit is how many teams a tick tries one by one once a batch has
	// failed. Anything past it waits for the next tick.
	fallbackLimit = 1000
)

type Store interface {
	PurgeTeams(ctx context.Context, arg db.PurgeTeamsParams) ([]uuid.UUID, error)
	ListPurgeableTeams(ctx context.Context, arg db.ListPurgeableTeamsParams) ([]uuid.UUID, error)
	PurgeTeam(ctx context.Context, arg db.PurgeTeamParams) (int64, error)
}

// Teams purges every team deleted before now minus after, batchSize to a
// statement, and returns how many went. A batch that fails is retried one
// team per statement, so a team whose cascade fails holds back only itself:
// its error is returned and logged with its id, and the next tick tries it
// again. after is read on every call, so a restart with a new
// TEAM_PURGE_AFTER moves every pending purge at once.
func Teams(ctx context.Context, s Store, now time.Time, after time.Duration, logger *slog.Logger) (int, error) {
	before := now.Add(-after)

	purged := 0
	for {
		ids, err := s.PurgeTeams(ctx, db.PurgeTeamsParams{Before: before, Limit: batchSize})
		if err != nil {
			logger.WarnContext(ctx, "A purge batch failed; purging one team at a time", "error", err)
			n, err := oneByOne(ctx, s, before, logger)
			return purged + n, err
		}
		purged += len(ids)
		if len(ids) < batchSize {
			return purged, nil
		}
	}
}

func oneByOne(ctx context.Context, s Store, before time.Time, logger *slog.Logger) (int, error) {
	ids, err := s.ListPurgeableTeams(ctx, db.ListPurgeableTeamsParams{Before: before, Limit: fallbackLimit})
	if err != nil {
		return 0, fmt.Errorf("list teams to purge: %w", err)
	}

	purged := 0
	var errs []error
	for _, id := range ids {
		n, err := s.PurgeTeam(ctx, db.PurgeTeamParams{ID: id, Before: before})
		if err != nil {
			logger.ErrorContext(ctx, "Purging a team failed", "team_id", id, "error", err)
			errs = append(errs, fmt.Errorf("purge team %s: %w", id, err))
			continue
		}
		purged += int(n)
	}
	return purged, errors.Join(errs...)
}

type job struct {
	store  Store
	after  time.Duration
	logger *slog.Logger
}

func Job(s Store, after time.Duration, logger *slog.Logger) job {
	return job{store: s, after: after, logger: logger}
}

func (job) Name() string { return "teams.purge" }

func (j job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	dbos.RegisterWorkflow(d, j.purge, dbos.WithWorkflowName(purgeWorkflow))
	return j.schedules(), nil
}

// No backfill: a missed tick has no work of its own. The next one purges
// everything that expired in between.
func (job) schedules() []dbos.ScheduleSpec {
	return []dbos.ScheduleSpec{{
		ScheduleName:      purgeSchedule,
		Schedule:          purgeCron,
		WorkflowName:      purgeWorkflow,
		AutomaticBackfill: false,
	}}
}

func (j job) purge(ctx dbos.Context, _ dbos.ScheduledWorkflowInput) (int, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (int, error) {
		n, err := Teams(sctx, j.store, time.Now(), j.after, j.logger)
		j.logger.InfoContext(sctx, "Purged deleted teams", "purged", n)
		return n, err
	})
}
