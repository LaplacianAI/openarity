package store

import (
	"testing"

	"github.com/google/uuid"
)

// beforeCachedTokens is 20261127090000_approval_replies.sql, the last
// migration before runs counted their cached input. Named, as the other
// migration tests name theirs: a later migration must not move it.
const beforeCachedTokens = 20261127090000

// cachedTokensColumn adds the column; the migration after it fills it.
const cachedTokensColumn = 20261128090000

// A run from before gets the cached input its usage events recorded, held to
// its input; one whose events carry none keeps zero; and the check the
// backfill waited for is validated. Rolled back, the column goes and the
// totals stay; migrated again, it is filled again.
func TestOldRunsGetTheirCachedInputFromTheirUsageEvents(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), beforeCachedTokens); err != nil {
		t.Fatalf("UpTo(%d): %v", beforeCachedTokens, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('platform') RETURNING id`)
	user := mustUser(t, s)
	session := scalar[uuid.UUID](t, s,
		`INSERT INTO sessions (team_id, kind, user_id) VALUES ($1, 'direct', $2) RETURNING id`, team, user)
	run := func(input int64, cached ...string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := s.pool.Exec(t.Context(), `
			INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, input_tokens, reply_to_channel)
			VALUES ($1, $2, $3, $1, 'x', 'hi', $4, false)`, id, team, session, input); err != nil {
			t.Fatalf("insert run: %v", err)
		}
		for i, payload := range cached {
			if _, err := s.pool.Exec(t.Context(),
				`INSERT INTO run_events (run_id, seq, kind, payload) VALUES ($1, $2, 'usage', $3::jsonb)`,
				id, i+1, payload); err != nil {
				t.Fatalf("insert usage: %v", err)
			}
		}
		return id
	}
	cached := run(100_000, `{"input_tokens": 60000, "cached_input_tokens": 50000}`,
		`{"input_tokens": 40000, "cached_input_tokens": 30000}`)
	over := run(1_000, `{"input_tokens": 1000, "cached_input_tokens": 5000}`)
	none := run(500, `{"input_tokens": 500}`)
	live := run(100_000, `{"input_tokens": 100000, "cached_input_tokens": 50000}`)
	// Past a batch of a thousand, so the backfill commits and carries on.
	if _, err := s.pool.Exec(t.Context(), `
		WITH r AS (
			INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, input_tokens, reply_to_channel)
			SELECT id, $1, $2, id, 'bulk', 'hi', 10, false FROM (SELECT gen_random_uuid() AS id FROM generate_series(1, 2100)) g
			RETURNING id)
		INSERT INTO run_events (run_id, seq, kind, payload)
		SELECT id, 1, 'usage', '{"input_tokens": 10, "cached_input_tokens": 7}' FROM r`, team, session); err != nil {
		t.Fatalf("insert bulk runs: %v", err)
	}

	// A run still going when the column lands adds to it before the backfill
	// reaches it: the backfill keeps what it added.
	if _, err := p.UpTo(t.Context(), cachedTokensColumn); err != nil {
		t.Fatalf("UpTo(%d): %v", cachedTokensColumn, err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET cached_input_tokens = 70000 WHERE id = $1`, live); err != nil {
		t.Fatalf("a live run's usage: %v", err)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := scalar[int64](t, s, `SELECT cached_input_tokens FROM runs WHERE id = $1`, live); got != 70_000 {
		t.Errorf("the live run has %d cached after the backfill, want the 70,000 it had added", got)
	}
	backfilled := func() {
		t.Helper()
		if n := count(t, s, `SELECT count(*) FROM runs WHERE agent_name = 'bulk' AND cached_input_tokens <> 7`); n != 0 {
			t.Errorf("%d of the 2,100 bulk runs were not backfilled", n)
		}
		for id, want := range map[uuid.UUID]int64{cached: 80_000, over: 1_000, none: 0} {
			if got := scalar[int64](t, s, `SELECT cached_input_tokens FROM runs WHERE id = $1`, id); got != want {
				t.Errorf("run %s has %d cached, want %d", id, got, want)
			}
		}
		if !scalar[bool](t, s, `SELECT convalidated FROM pg_constraint
			WHERE conname = 'runs_cached_within_input' AND conrelid = 'runs'::regclass`) {
			t.Error("runs_cached_within_input is not validated")
		}
	}
	backfilled()

	downTo(t, s, beforeCachedTokens)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'runs' AND column_name = 'cached_input_tokens'`); n != 0 {
		t.Error("runs.cached_input_tokens is still there after the rollback")
	}
	if got := scalar[int64](t, s, `SELECT input_tokens FROM runs WHERE id = $1`, cached); got != 100_000 {
		t.Errorf("input_tokens = %d after the rollback, want 100,000", got)
	}

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate again: %v", err)
	}
	backfilled()
}
