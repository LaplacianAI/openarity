package store

import (
	"testing"

	"github.com/google/uuid"
)

const (
	// addDeliveryEvent is 20261213090000_add_delivery_event.sql, the one
	// before pending changes.
	addDeliveryEvent = 20261213090000
	// addPendingChanges is 20261214090000_add_pending_changes.sql.
	addPendingChanges = 20261214090000
)

// A proposal made before is proposed when it was made; an ask waits with
// its time, its run and its code, and a proposal with no time is refused;
// the columns come and go.
func TestPendingChangesBackfillCheckAndRollBack(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), addDeliveryEvent); err != nil {
		t.Fatalf("UpTo(%d): %v", addDeliveryEvent, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('asked') RETURNING id`)
	user := scalar[uuid.UUID](t, s, `INSERT INTO users (issuer, subject) VALUES ('https://id.test', 'maker') RETURNING id`)
	session := scalar[uuid.UUID](t, s, `INSERT INTO sessions (team_id, kind, user_id) VALUES ($1, 'direct', $2) RETURNING id`, team, user)
	schedule := func(name, status string) uuid.UUID {
		return scalar[uuid.UUID](t, s, `INSERT INTO schedules (id, team_id, created_by, hub, name, objective, kind, rule, anchor, timezone,
			status, next_fire_at, proposed_in, created_at)
			VALUES (gen_random_uuid(), $1, $2, true, $3, 'o', 'every', '3600', now(), 'UTC', $4,
			CASE WHEN $4 = 'active' THEN now() END, CASE WHEN $4 = 'proposed' THEN $5::uuid END, now() - interval '20 minutes')
			RETURNING id`, team, user, name, status, session)
	}
	proposed, active := schedule("proposed", "proposed"), schedule("active", "active")
	waiting := scalar[uuid.UUID](t, s, `INSERT INTO watchers (id, team_id, created_by, hub, name, objective, source, hook_id, status, created_at)
		VALUES (gen_random_uuid(), $1, $2, true, 'hook', 'o', 'webhook', 'hook-0123456789abcdef', 'waiting_secret', now() - interval '20 minutes')
		RETURNING id`, team, user)

	if _, err := p.UpTo(t.Context(), addPendingChanges); err != nil {
		t.Fatalf("UpTo(%d): %v", addPendingChanges, err)
	}
	if !scalar[bool](t, s, `SELECT proposed_at = created_at FROM schedules WHERE id = $1`, proposed) {
		t.Error("a proposed schedule is not proposed when it was made")
	}
	if !scalar[bool](t, s, `SELECT proposed_at IS NULL AND pending IS NULL FROM schedules WHERE id = $1`, active) {
		t.Error("an active schedule was given a proposal time or an ask")
	}
	if !scalar[bool](t, s, `SELECT proposed_at = created_at FROM watchers WHERE id = $1`, waiting) {
		t.Error("a watcher waiting for its secret is not proposed when it was made")
	}

	for what, sql := range map[string]string{
		"an ask with no time":     `UPDATE schedules SET pending = '{}', pending_run = gen_random_uuid(), yes_code = '1234' WHERE id = $1`,
		"an ask with no run":      `UPDATE schedules SET pending = '{}', pending_at = now(), yes_code = '1234' WHERE id = $1`,
		"an ask with no code":     `UPDATE schedules SET pending = '{}', pending_at = now(), pending_run = gen_random_uuid() WHERE id = $1`,
		"a proposal with no time": `UPDATE schedules SET status = 'proposed', next_fire_at = NULL, proposed_in = '` + session.String() + `', proposed_at = NULL WHERE id = $1`,
	} {
		if _, err := s.pool.Exec(t.Context(), sql, active); err == nil {
			t.Errorf("%s was stored", what)
		}
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE schedules SET pending = '{}', pending_at = now(), pending_run = gen_random_uuid(), yes_code = '1234'
		WHERE id = $1`, active); err != nil {
		t.Errorf("a whole ask was refused: %v", err)
	}

	if _, err := p.DownTo(t.Context(), addDeliveryEvent); err != nil {
		t.Fatalf("DownTo(%d): %v", addDeliveryEvent, err)
	}
	if scalar[bool](t, s, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name IN ('schedules', 'watchers') AND column_name IN ('proposed_at', 'pending', 'yes_code'))`) {
		t.Error("the columns outlived their rollback")
	}
	if !scalar[bool](t, s, `SELECT to_regclass('schedules_proposed') IS NOT NULL`) {
		t.Error("the rollback did not put back the index on what lapses")
	}
}
