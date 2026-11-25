package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Silent is the whole answer of a run a schedule started that has nothing to
// say: no reply is sent, and nobody is told.
const Silent = "[silent]"

// pauseAfter is how many of a schedule's fires in a row may fail before it
// pauses itself.
const pauseAfter = 5

// finishFired records how a run a schedule started ended, on its fire, and
// counts it on the schedule: a success clears its failures, a failure adds
// one and the fifth pauses it, a cancelled run is neither. It says whether the
// run was silent, so no reply is written for it. A run no schedule started
// changes nothing.
func finishFired(ctx context.Context, q *db.Queries, run db.Run, finish db.FinishRunParams) (bool, error) {
	if run.ParentRunID != nil {
		return false, nil
	}
	silent := finish.Status == "succeeded" && finish.Output != nil && strings.TrimSpace(*finish.Output) == Silent
	outcome := "failed"
	switch {
	case silent:
		outcome = "silent"
	case finish.Status == "succeeded":
		outcome = "answered"
	}
	schedule, err := q.SetFireOutcome(ctx, db.SetFireOutcomeParams{Outcome: outcome, RunID: &run.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record how run %s's fire went: %w", run.ID, err)
	}
	if _, err := q.CountScheduleRun(ctx, db.CountScheduleRunParams{
		ID: schedule, Failed: finish.Status == "failed", Succeeded: finish.Status == "succeeded", PauseAfter: pauseAfter,
	}); err != nil {
		return false, fmt.Errorf("count run %s on its schedule: %w", run.ID, err)
	}
	return silent, nil
}
