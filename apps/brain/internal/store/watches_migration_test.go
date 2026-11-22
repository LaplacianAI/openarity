package store

import (
	"testing"

	"github.com/google/uuid"
)

const (
	// e2bTemplates is 20261202100000_e2b_templates.sql, the one before I4.
	e2bTemplates = 20261202100000
	// addImprovementWatches is 20261203090000_add_improvement_watches.sql.
	addImprovementWatches = 20261203090000
	// indexRunsRootAgent is 20261203090100_index_runs_root_agent.sql.
	indexRunsRootAgent = 20261203090100
)

// Changes applied in the last 30 days are watched from when they were
// applied, an older one is not, and a waiting one never is; the table
// comes and goes.
func TestImprovementWatchesBackfillAndRollBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), e2bTemplates); err != nil {
		t.Fatalf("UpTo(%d): %v", e2bTemplates, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('watched') RETURNING id`)
	agent := scalar[uuid.UUID](t, s, `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'bot', 'm', 100, 'react', 3) RETURNING id`, team)
	propose := func(status, decided string) uuid.UUID {
		return scalar[uuid.UUID](t, s, `INSERT INTO improvement_proposals (team_id, agent_id, kind, status, why, hypothesis, scope,
			before, after, decided_at) VALUES ($1, $2, 'instructions', $3, 'w', 'h', 'domain', '{}', '{}',
			CASE WHEN $4 = '' THEN NULL ELSE now() - $4::interval END) RETURNING id`, team, agent, status, decided)
	}
	recent, old, waiting := propose("applied", "5 days"), propose("applied", "40 days"), propose("waiting", "")

	if _, err := p.UpTo(t.Context(), addImprovementWatches); err != nil {
		t.Fatalf("UpTo(%d): %v", addImprovementWatches, err)
	}
	if n := scalar[int64](t, s, `SELECT count(*) FROM improvement_watches WHERE proposal_id = $1 AND status = 'watching'
		AND agent_ids = ARRAY[$2]::uuid[] AND on_seconds BETWEEN 431000 AND 433000`, recent, agent); n != 1 {
		t.Error("a change applied five days ago is not watched from then")
	}
	if n := scalar[int64](t, s, `SELECT count(*) FROM improvement_watches WHERE proposal_id = ANY($1)`, []uuid.UUID{old, waiting}); n != 0 {
		t.Errorf("%d watches for a change older than 30 days or one never applied", n)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE improvement_watches SET status = 'flagged' WHERE proposal_id = $1`, recent); err == nil {
		t.Error("a flagged watch without its flag was stored")
	}

	if _, err := p.UpTo(t.Context(), indexRunsRootAgent); err != nil {
		t.Fatalf("UpTo(%d): %v", indexRunsRootAgent, err)
	}
	if !scalar[bool](t, s, `SELECT to_regclass('runs_root_agent_idx') IS NOT NULL`) {
		t.Fatal("the runs index was not built")
	}
	if _, err := p.DownTo(t.Context(), e2bTemplates); err != nil {
		t.Fatalf("DownTo(%d): %v", e2bTemplates, err)
	}
	if scalar[bool](t, s, `SELECT to_regclass('improvement_watches') IS NOT NULL`) {
		t.Error("the watches table outlived its rollback")
	}
	if scalar[bool](t, s, `SELECT to_regclass('runs_root_agent_idx') IS NOT NULL`) {
		t.Error("the runs index outlived its rollback")
	}
}
