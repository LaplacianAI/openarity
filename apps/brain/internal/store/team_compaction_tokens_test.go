package store

import (
	"testing"

	"github.com/google/uuid"
)

// teamCompactionTokens is 20261129090000_team_compaction_tokens.sql, which
// gave a team its own token lines; beforeTeamCompactionTokens is the
// migration before it. Named, as the other migration tests name theirs.
const (
	teamCompactionTokens       = 20261129090000
	beforeTeamCompactionTokens = 20261128090100
)

// A team that saved its settings before the lines existed compacts where the
// brain always did. Rolled back, the columns go and the row stays; migrated
// again, the defaults come back.
func TestATeamFromBeforeTheTokenLinesTakesTheBrainsOld(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), beforeTeamCompactionTokens); err != nil {
		t.Fatalf("UpTo(%d): %v", beforeTeamCompactionTokens, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('platform') RETURNING id`)
	if _, err := s.pool.Exec(t.Context(),
		`INSERT INTO team_settings (team_id, compaction_messages) VALUES ($1, 250)
		 ON CONFLICT (team_id) DO UPDATE SET compaction_messages = 250`, team); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	if _, err := p.UpTo(t.Context(), teamCompactionTokens); err != nil {
		t.Fatalf("UpTo(%d): %v", teamCompactionTokens, err)
	}
	lines := func() (int32, int32) {
		t.Helper()
		var least, most int32
		if err := s.pool.QueryRow(t.Context(),
			`SELECT compaction_tokens_min, compaction_tokens_max FROM team_settings WHERE team_id = $1`,
			team).Scan(&least, &most); err != nil {
			t.Fatalf("reading the lines: %v", err)
		}
		return least, most
	}
	if least, most := lines(); least != 50_000 || most != 60_000 {
		t.Fatalf("a team from before compacts between %d and %d, want 50,000 and 60,000", least, most)
	}

	if _, err := s.pool.Exec(t.Context(),
		`UPDATE team_settings SET compaction_tokens_min = 70000, compaction_tokens_max = 90000 WHERE team_id = $1`,
		team); err != nil {
		t.Fatalf("saving the lines: %v", err)
	}
	downTo(t, s, beforeTeamCompactionTokens)
	if n := scalar[int](t, s, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'team_settings' AND column_name LIKE 'compaction_tokens_%'`); n != 0 {
		t.Errorf("%d token columns are left after the rollback", n)
	}
	if got := scalar[int32](t, s, `SELECT compaction_messages FROM team_settings WHERE team_id = $1`, team); got != 250 {
		t.Errorf("the rollback changed the team's messages to %d", got)
	}

	if _, err := p.UpTo(t.Context(), teamCompactionTokens); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	if least, most := lines(); least != 50_000 || most != 60_000 {
		t.Errorf("migrated again, the lines are %d and %d, want the defaults", least, most)
	}
}
