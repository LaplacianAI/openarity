package store

import (
	"testing"

	"github.com/google/uuid"
)

// addReplays is 20261206090000_add_replays.sql.
const addReplays = 20261206090000

// A saved team starts with trying off at the defaults, each number held to
// its range; a try's row holds to its shape; the Down drops both.
func TestReplaysMigrateAndRollBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), addEfficiency); err != nil {
		t.Fatalf("UpTo(%d): %v", addEfficiency, err)
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

	if _, err := p.UpTo(t.Context(), addReplays); err != nil {
		t.Fatalf("UpTo(%d): %v", addReplays, err)
	}
	if scalar[bool](t, s, `SELECT improvement_replay OR improvement_replay_runs <> 4 OR improvement_replay_tokens <> 200000
		OR improvement_replay_minutes <> 10 OR improvement_replay_daily <> 40
		FROM team_settings WHERE team_id = $1`, team) {
		t.Error("a saved team does not start with trying off and the defaults")
	}
	for _, bad := range []string{
		`improvement_replay_runs = 0`, `improvement_replay_runs = 11`, `improvement_replay_tokens = 19999`,
		`improvement_replay_tokens = 1000001`, `improvement_replay_minutes = 31`, `improvement_replay_daily = 501`,
	} {
		if err := exec(`UPDATE team_settings SET `+bad+` WHERE team_id = $1`, team); err == nil {
			t.Errorf("%s was stored", bad)
		}
	}

	session := scalar[uuid.UUID](t, s, `INSERT INTO sessions (team_id, channel_id, provider_ref, kind)
		SELECT $1, id, 'x', 'group' FROM channels WHERE team_id = $1 LIMIT 1 RETURNING id`, team)
	run := uuid.New()
	if err := exec(`INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, input)
		VALUES ($1, $2, $3, $1, $4, 'bot', 'go')`, run, team, session, agent); err != nil {
		t.Fatal(err)
	}
	proposal := scalar[uuid.UUID](t, s, `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, before, after)
		VALUES ($1, $2, 'instructions', 'w', 'h', 'domain', '{}', '{}') RETURNING id`, team, agent)
	insert := func(set string) error {
		return exec(`INSERT INTO improvement_replays (proposal_id, team_id, source_run_id, position, chosen, original)
			VALUES ($1, $2, $3, 0, 'cited', 'bad')`+set, proposal, team, run)
	}
	if err := insert(""); err != nil {
		t.Fatalf("a queued try: %v", err)
	}
	for _, bad := range []string{
		`status = 'done'`,                         // done with no outcome
		`replayed = 'good'`,                       // an outcome while queued
		`status = 'skipped', finished_at = now()`, // skipped with no why
		`status = 'failed', finished_at = now()`,  // failed with no error
		`status = 'running'`,                      // a run's status, not a try's
		`chosen = 'random'`,
		`answer = repeat('x', 16385)`,
	} {
		if err := exec(`UPDATE improvement_replays SET `+bad+` WHERE proposal_id = $1`, proposal); err == nil {
			t.Errorf("a try with %s was stored", bad)
		}
	}
	if err := exec(`UPDATE improvement_replays SET status = 'done', replayed = 'good', finished_at = now() WHERE proposal_id = $1`, proposal); err != nil {
		t.Errorf("a done try: %v", err)
	}

	if _, err := p.DownTo(t.Context(), addEfficiency); err != nil {
		t.Fatalf("DownTo(%d): %v", addEfficiency, err)
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'team_settings' AND column_name LIKE 'improvement_replay%')
		OR to_regclass('improvement_replays') IS NOT NULL`) {
		t.Error("a column or the table outlived its rollback")
	}
	if _, err := p.UpTo(t.Context(), addReplays); err != nil {
		t.Fatalf("UpTo(%d) again: %v", addReplays, err)
	}
}
