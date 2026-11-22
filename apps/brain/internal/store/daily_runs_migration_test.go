package store

import (
	"testing"

	"github.com/google/uuid"
)

// dailyPassRuns is 20261204090000_daily_pass_runs.sql.
const dailyPassRuns = 20261204090000

// A team saved before waits for five runs; the bar is held between 1 and
// 100; a pass's runs seen and the setting come and go.
func TestDailyPassRunsMigrateAndRollBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), indexRunsRootAgent); err != nil {
		t.Fatalf("UpTo(%d): %v", indexRunsRootAgent, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('saved') RETURNING id`)
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO team_settings (team_id, improvement) VALUES ($1, true)`, team); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(t.Context(), dailyPassRuns); err != nil {
		t.Fatalf("UpTo(%d): %v", dailyPassRuns, err)
	}
	if scalar[int16](t, s, `SELECT improvement_daily_runs FROM team_settings WHERE team_id = $1`, team) != 5 {
		t.Error("a saved team's daily runs do not default to 5")
	}
	for _, bad := range []int{0, 101} {
		if _, err := s.pool.Exec(t.Context(), `UPDATE team_settings SET improvement_daily_runs = $2 WHERE team_id = $1`, team, bad); err == nil {
			t.Errorf("daily runs %d was stored", bad)
		}
	}
	if _, err := p.DownTo(t.Context(), indexRunsRootAgent); err != nil {
		t.Fatalf("DownTo(%d): %v", indexRunsRootAgent, err)
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE (table_name = 'team_settings' AND column_name = 'improvement_daily_runs')
		   OR (table_name = 'improver_passes' AND column_name = 'runs_seen'))`) {
		t.Error("a column outlived its rollback")
	}
}
