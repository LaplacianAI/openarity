package improvements

import (
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// side is what one window of runs came to.
type side struct {
	Runs int16 `json:"runs"`
	Bad  int16 `json:"bad"`
	Up   int16 `json:"up"`
	Down int16 `json:"down"`
	// Medians per run; null with no runs.
	Tokens *int64 `json:"tokens"`
	Steps  *int32 `json:"steps"`
	// Runs carrying the targeted signature; null for a removal.
	Signature *int16 `json:"signature"`
}

// watchView is how an applied change went: its judged runs after it
// against the ones before, and the bar the agent's own noise sets.
type watchView struct {
	Status   string  `json:"status"`
	Flag     *string `json:"flag"`
	EndedWhy *string `json:"ended_why"`
	// Runs counted after the change, of the ones that decide it.
	Needed       int         `json:"needed"`
	BaselineRuns int16       `json:"baseline_runs"`
	BaselineBad  int16       `json:"baseline_bad"`
	Bar          *int16      `json:"bar"`
	BaselineP90  *int64      `json:"baseline_p90"`
	Before       side        `json:"before"`
	After        side        `json:"after"`
	Overlapping  []uuid.UUID `json:"overlapping"`
	StartedAt    time.Time   `json:"started_at"`
	FinishedAt   *time.Time  `json:"finished_at"`
	ClearedBy    *uuid.UUID  `json:"cleared_by"`
	ClearedAt    *time.Time  `json:"cleared_at"`
}

func watchViewOf(w db.ImprovementWatch) *watchView {
	return &watchView{
		Status: w.Status, Flag: w.Flag, EndedWhy: w.EndedWhy, Needed: improve.WatchRuns,
		BaselineRuns: w.BaselineRuns, BaselineBad: w.BaselineBad, Bar: w.Bar, BaselineP90: w.BaselineP90,
		Before: side{
			Runs: w.BeforeRuns, Bad: w.BeforeBad, Up: w.BeforeUp, Down: w.BeforeDown,
			Tokens: w.BeforeTokens, Steps: w.BeforeSteps, Signature: w.SigBefore,
		},
		After: side{
			Runs: w.AfterRuns, Bad: w.AfterBad, Up: w.AfterUp, Down: w.AfterDown,
			Tokens: w.AfterTokens, Steps: w.AfterSteps, Signature: w.SigAfter,
		},
		Overlapping: w.Overlapping, StartedAt: w.StartedAt, FinishedAt: w.FinishedAt,
		ClearedBy: w.ClearedBy, ClearedAt: w.ClearedAt,
	}
}
