package store

import (
	"testing"

	"github.com/google/uuid"
)

// addImproverPasses is 20261202090000_add_improver_passes.sql.
const addImproverPasses = 20261202090000

// A team saved before has the default cap; the passes table, the proposals'
// pass and the index come and go, and a proposal's pass is kept apart from
// the pass's life.
func TestImproverPassesMigrateAndRollBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), indexTopRunsFinished); err != nil {
		t.Fatalf("UpTo(%d): %v", indexTopRunsFinished, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('saved') RETURNING id`)
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO team_settings (team_id, improvement) VALUES ($1, true)`, team); err != nil {
		t.Fatal(err)
	}

	if _, err := p.UpTo(t.Context(), addImproverPasses); err != nil {
		t.Fatalf("UpTo(%d): %v", addImproverPasses, err)
	}
	if scalar[int16](t, s, `SELECT improvement_daily_passes FROM team_settings WHERE team_id = $1`, team) != 20 {
		t.Error("a saved team's cap does not default to 20")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE team_settings SET improvement_daily_passes = 0 WHERE team_id = $1`, team); err == nil {
		t.Error("a cap of 0 was stored")
	}
	agent := scalar[uuid.UUID](t, s, `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'bot', 'm', 100, 'react', 3) RETURNING id`, team)
	pass := scalar[uuid.UUID](t, s, `INSERT INTO improver_passes (team_id, agent_id, trigger) VALUES ($1, $2, 'pattern') RETURNING id`, team, agent)
	if _, err := s.pool.Exec(t.Context(), `UPDATE improver_passes SET status = 'done' WHERE id = $1`, pass); err == nil {
		t.Error("a pass was done with no finished_at")
	}
	proposal := scalar[uuid.UUID](t, s, `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, before, after, pass_id)
		VALUES ($1, $2, 'instructions', 'w', 'h', 'invariant', '{}', '{}', $3) RETURNING id`, team, agent, pass)
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM improver_passes WHERE id = $1`, pass); err != nil {
		t.Fatal(err)
	}
	if scalar[bool](t, s, `SELECT pass_id IS NOT NULL FROM improvement_proposals WHERE id = $1`, proposal) {
		t.Error("a proposal kept the id of a pass that is gone")
	}

	downTo(t, s, indexTopRunsFinished)
	if tableExists(t, s, "improver_passes") {
		t.Error("rolled back, improver_passes survived")
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE (table_name = 'team_settings' AND column_name = 'improvement_daily_passes')
		   OR (table_name = 'improvement_proposals' AND column_name = 'pass_id'))`) {
		t.Error("rolled back, the new columns survived")
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'run_digests_agent_wrong_idx')`) {
		t.Error("rolled back, the index survived")
	}
	if _, err := p.UpTo(t.Context(), addImproverPasses); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
