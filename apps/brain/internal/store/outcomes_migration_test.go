package store

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// addOutcomes is 20261201090000_add_outcomes.sql; indexTopRunsFinished the
// index after it.
const (
	addOutcomes          = 20261201090000
	indexTopRunsFinished = 20261201090100
)

// A team that already had the switch on starts from the migration, and the
// new tables and columns come and go.
func TestOutcomesMigrateAndRollBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), addImprovements); err != nil {
		t.Fatalf("UpTo(%d): %v", addImprovements, err)
	}
	on := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('on') RETURNING id`)
	off := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('off') RETURNING id`)
	for team, improving := range map[uuid.UUID]bool{on: true, off: false} {
		if _, err := s.pool.Exec(t.Context(), `INSERT INTO team_settings (team_id, improvement) VALUES ($1, $2)`, team, improving); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := p.UpTo(t.Context(), indexTopRunsFinished); err != nil {
		t.Fatalf("UpTo(%d): %v", indexTopRunsFinished, err)
	}
	if !scalar[bool](t, s, `SELECT improvement_since IS NOT NULL FROM team_settings WHERE team_id = $1`, on) {
		t.Error("a team with the switch on has no improvement_since")
	}
	if scalar[bool](t, s, `SELECT improvement_since IS NOT NULL FROM team_settings WHERE team_id = $1`, off) {
		t.Error("a team with the switch off has an improvement_since")
	}
	if scalar[int16](t, s, `SELECT improvement_sample_share FROM team_settings WHERE team_id = $1`, off) != 10 {
		t.Error("the sample share does not default to 10")
	}
	for _, table := range []string{"run_outcomes", "run_feedback", "run_digests"} {
		if !tableExists(t, s, table) {
			t.Errorf("%s is missing", table)
		}
	}

	downTo(t, s, addImprovements)
	for _, table := range []string{"run_outcomes", "run_feedback", "run_digests"} {
		if tableExists(t, s, table) {
			t.Errorf("rolled back, %s survived", table)
		}
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'team_settings' AND column_name IN ('judge_model_name', 'improvement_since', 'improvement_sample_share'))`) {
		t.Error("rolled back, the settings columns survived")
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'runs_top_finished_idx')`) {
		t.Error("rolled back, the index survived")
	}
	if _, err := p.UpTo(t.Context(), indexTopRunsFinished); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// improvement_since is set when the switch goes on, kept while it stays on,
// and set again when it goes off and on.
func TestImprovementSinceFollowsTheSwitch(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('platform') RETURNING id`)
	save := func(on bool) *time.Time {
		t.Helper()
		row, err := s.UpsertTeamSettings(t.Context(), db.UpsertTeamSettingsParams{
			TeamID: team, SearchProvider: "none", Improvement: on, CompactionMessages: 100,
			CompactionTokensMin: 50_000, CompactionTokensMax: 60_000, SandboxProvider: "none", BrowserProvider: "none",
			ImprovementSampleShare: 10,
		})
		if err != nil {
			t.Fatalf("save(%v): %v", on, err)
		}
		return row.ImprovementSince
	}

	if save(false) != nil {
		t.Error("saved off, improvement_since was set")
	}
	first := save(true)
	if first == nil {
		t.Fatal("turned on, improvement_since was not set")
	}
	if again := save(true); again == nil || !again.Equal(*first) {
		t.Errorf("kept on, improvement_since moved from %v to %v", first, again)
	}
	save(false)
	if s.pool.QueryRow(t.Context(), `SELECT pg_sleep(0.01)`).Scan(new(any)) != nil {
		t.Fatal("sleep")
	}
	if later := save(true); later == nil || !later.After(*first) {
		t.Errorf("turned on again, improvement_since = %v, want after %v", later, first)
	}
}
