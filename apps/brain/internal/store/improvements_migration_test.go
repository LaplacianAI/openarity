package store

import (
	"testing"

	"github.com/google/uuid"
)

// addImprovements is 20261130090000_add_improvements.sql, which renamed the
// learning switch and added the proposals and tool notes; the migration
// before it is teamCompactionTokens.
const addImprovements = 20261130090000

// A team's switch keeps its value through the rename, both ways, and the
// new tables come and go with it.
func TestTheLearningSwitchBecomesImprovementAndBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), teamCompactionTokens); err != nil {
		t.Fatalf("UpTo(%d): %v", teamCompactionTokens, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('platform') RETURNING id`)
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO team_settings (team_id, learning) VALUES ($1, true)`, team); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	if _, err := p.UpTo(t.Context(), addImprovements); err != nil {
		t.Fatalf("UpTo(%d): %v", addImprovements, err)
	}
	if !scalar[bool](t, s, `SELECT improvement FROM team_settings WHERE team_id = $1`, team) {
		t.Error("the switch was on before the rename and is off after it")
	}
	if !tableExists(t, s, "improvement_proposals") || !tableExists(t, s, "agent_tool_notes") {
		t.Error("the new tables are missing")
	}

	downTo(t, s, teamCompactionTokens)
	if !scalar[bool](t, s, `SELECT learning FROM team_settings WHERE team_id = $1`, team) {
		t.Error("rolled back, the switch lost its value")
	}
	if tableExists(t, s, "improvement_proposals") || tableExists(t, s, "agent_tool_notes") {
		t.Error("rolled back, the new tables survived")
	}
}
