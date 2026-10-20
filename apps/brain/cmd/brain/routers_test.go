package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/runs"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

type runWorld struct {
	store       runStore
	raw         *pgx.Conn
	team, user  uuid.UUID
	agent       uuid.UUID
	otherAgent  uuid.UUID
	teamSession uuid.UUID
}

func newRunWorld(t *testing.T) *runWorld {
	t.Helper()

	dsn := schemaDSN(t)
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	raw, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })

	w := &runWorld{store: runStore{s}, raw: raw}
	var other uuid.UUID
	for _, q := range []struct {
		sql  string
		args []any
		into *uuid.UUID
	}{
		{`INSERT INTO teams (name) VALUES ('platform') RETURNING id`, nil, &w.team},
		{`INSERT INTO teams (name) VALUES ('elsewhere') RETURNING id`, nil, &other},
		{`INSERT INTO users (issuer, subject) VALUES ('https://id.test', 'ada') RETURNING id`, nil, &w.user},
	} {
		if err := raw.QueryRow(t.Context(), q.sql, q.args...).Scan(q.into); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	for _, q := range []struct {
		team uuid.UUID
		into *uuid.UUID
	}{{w.team, &w.agent}, {other, &w.otherAgent}} {
		if err := raw.QueryRow(t.Context(), `
			INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
			VALUES ($1, 'triage', 'test/model', 1024, 'react', 5) RETURNING id`, q.team).Scan(q.into); err != nil {
			t.Fatalf("insert agent: %v", err)
		}
	}
	if err := raw.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`, w.team).Scan(&w.teamSession); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return w
}

func (w *runWorld) count(t *testing.T, table string) int {
	t.Helper()

	var n int
	if err := w.raw.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestARunWithNoSessionIsGivenADirectSessionOfItsCallers(t *testing.T) {
	w := newRunWorld(t)

	r, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.agent, UserID: w.user, Input: "hello",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	var kind string
	var owner *uuid.UUID
	var team uuid.UUID
	if err := w.raw.QueryRow(t.Context(),
		`SELECT kind, user_id, team_id FROM sessions WHERE id = $1`, r.SessionID).Scan(&kind, &owner, &team); err != nil {
		t.Fatalf("read the run's session: %v", err)
	}
	if kind != "direct" || owner == nil || *owner != w.user || team != w.team {
		t.Errorf("session is %s of %v in %s, want direct, of the caller, in the run's team", kind, owner, team)
	}
	if r.UserID == nil || *r.UserID != w.user || r.Status != "queued" || r.Input != "hello" {
		t.Errorf("run = %+v", r)
	}
}

func TestARunInANamedSessionMakesNoOtherSession(t *testing.T) {
	w := newRunWorld(t)
	session := w.teamSession

	r, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.agent, UserID: w.user, SessionID: &session, Input: "hello",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if r.SessionID != w.teamSession {
		t.Errorf("session = %s, want the one named", r.SessionID)
	}
	if n := w.count(t, "sessions"); n != 1 {
		t.Errorf("%d sessions, want only the one named", n)
	}
}

// CreateRun inserts nothing for an agent outside the team, which is how an
// agent deleted or moved mid-request looks. The direct session made for it a
// statement earlier must go too, or the caller has an empty conversation they
// never started.
func TestARefusedRunLeavesNoSessionBehind(t *testing.T) {
	w := newRunWorld(t)
	before := w.count(t, "sessions")

	_, err := w.store.StartRun(t.Context(), runs.StartArgs{
		ID: uuid.New(), TeamID: w.team, AgentID: w.otherAgent, UserID: w.user, Input: "hello",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("StartRun = %v, want pgx.ErrNoRows for the handler's 404", err)
	}
	if after := w.count(t, "sessions"); after != before {
		t.Errorf("sessions went from %d to %d for a run that was refused", before, after)
	}
	if n := w.count(t, "runs"); n != 0 {
		t.Errorf("%d runs written", n)
	}
}
