package store

import (
	"testing"

	"github.com/google/uuid"
)

// addEfficiency is 20261205090000_add_efficiency.sql.
const addEfficiency = 20261205090000

// Existing proposals become quality changes, existing passes keep their
// trigger, a saved team starts with the pass off and the defaults, each
// number is held to its range, and the Down restores the checks it replaced.
func TestEfficiencyMigratesAndRollsBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), dailyPassRuns); err != nil {
		t.Fatalf("UpTo(%d): %v", dailyPassRuns, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('saved') RETURNING id`)
	agent := scalar[uuid.UUID](t, s, `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'bot', 'm', 1024, 'react', 5) RETURNING id`, team)
	exec := func(sql string, args ...any) error {
		_, err := s.pool.Exec(t.Context(), sql, args...)
		return err
	}
	if err := exec(`INSERT INTO team_settings (team_id, improvement) VALUES ($1, true)`, team); err != nil {
		t.Fatal(err)
	}
	proposal := scalar[uuid.UUID](t, s, `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, before, after)
		VALUES ($1, $2, 'instructions', 'w', 'h', 'domain', '{}', '{}') RETURNING id`, team, agent)
	if err := exec(`INSERT INTO improver_passes (team_id, agent_id, trigger, signatures, pruned, run_ids, model, workflow_id)
		VALUES ($1, $2, 'daily', '{}', '{}', '{}', 'm', 'w1')`, team, agent); err != nil {
		t.Fatal(err)
	}

	if _, err := p.UpTo(t.Context(), addEfficiency); err != nil {
		t.Fatalf("UpTo(%d): %v", addEfficiency, err)
	}
	if scalar[string](t, s, `SELECT goal FROM improvement_proposals WHERE id = $1`, proposal) != "quality" ||
		scalar[int](t, s, `SELECT cardinality(saves) FROM improvement_proposals WHERE id = $1`, proposal) != 0 {
		t.Error("an existing proposal is not a quality change saving nothing")
	}
	if scalar[string](t, s, `SELECT trigger FROM improver_passes WHERE workflow_id = 'w1'`) != "daily" {
		t.Error("an existing pass lost its trigger")
	}
	if scalar[bool](t, s, `SELECT improvement_efficiency OR improvement_efficiency_runs <> 20 OR improvement_efficiency_days <> 14
		OR improvement_efficiency_spread <> 200 OR improvement_efficiency_mark_runs <> 3 OR improvement_efficiency_margin <> 1
		FROM team_settings WHERE team_id = $1`, team) {
		t.Error("a saved team does not start with the pass off and the defaults")
	}
	for _, bad := range []string{
		`improvement_efficiency_runs = 9`, `improvement_efficiency_days = 61`, `improvement_efficiency_spread = 124`,
		`improvement_efficiency_mark_runs = 21`, `improvement_efficiency_margin = 4`,
	} {
		if err := exec(`UPDATE team_settings SET `+bad+` WHERE team_id = $1`, team); err == nil {
			t.Errorf("%s was stored", bad)
		}
	}
	for _, bad := range []string{
		`goal = 'efficiency'`,                    // saving nothing
		`saves = '{cost}'`,                       // a quality change
		`goal = 'efficiency', saves = '{money}'`, // not a saving
		`goal = 'speed', saves = '{time}'`,       // not a goal
	} {
		if err := exec(`UPDATE improvement_proposals SET `+bad+` WHERE id = $1`, proposal); err == nil {
			t.Errorf("a proposal with %s was stored", bad)
		}
	}
	if err := exec(`UPDATE improvement_proposals SET goal = 'efficiency', saves = '{cost,time}' WHERE id = $1`, proposal); err != nil {
		t.Errorf("an efficiency change saving cost and time: %v", err)
	}
	if err := exec(`UPDATE improver_passes SET trigger = 'efficiency' WHERE workflow_id = 'w1'`); err != nil {
		t.Errorf("an efficiency pass: %v", err)
	}

	if _, err := p.DownTo(t.Context(), dailyPassRuns); err != nil {
		t.Fatalf("DownTo(%d): %v", dailyPassRuns, err)
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE (table_name = 'team_settings' AND column_name LIKE 'improvement_efficiency%')
		   OR (table_name = 'improvement_proposals' AND column_name IN ('goal', 'saves'))
		   OR (table_name = 'improvement_watches' AND column_name LIKE '%work_ms'))`) {
		t.Error("a column outlived its rollback")
	}
	if scalar[string](t, s, `SELECT trigger FROM improver_passes WHERE workflow_id = 'w1'`) != "daily" {
		t.Error("an efficiency pass was not rolled back to one the old check knows")
	}
	if err := exec(`UPDATE improver_passes SET trigger = 'efficiency' WHERE workflow_id = 'w1'`); err == nil {
		t.Error("the rolled-back check takes an efficiency pass")
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'run_work_ms')`) {
		t.Error("run_work_ms outlived its rollback")
	}
}
