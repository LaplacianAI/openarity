package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/approvals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

type noResume struct{ got int }

func (r *noResume) Resume(context.Context, uuid.UUID, int32) error { r.got++; return nil }

// The handler knows the runtime only by the decider's words: each refusal
// arrives as the API's own error, and an answer that stands resumes the run.
func TestTheDeciderNamesTheRuntimesRefusals(t *testing.T) {
	dsn := schemaDSN(t)
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })

	var team, session, run, sam, req uuid.UUID
	for _, q := range []struct {
		sql  string
		args []any
		into *uuid.UUID
	}{
		{`INSERT INTO teams (name) VALUES ('platform') RETURNING id`, nil, &team},
		{`INSERT INTO users (issuer, subject) VALUES ('dev', 'sam') RETURNING id`, nil, &sam},
		{`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`, []any{&team}, &session},
		{`WITH id AS (SELECT gen_random_uuid() AS v)
		  INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, status, waiting_since, user_id)
		  SELECT v, $1, $2, v, 'triage', 'x', 'waiting', now(), $3 FROM id RETURNING id`, []any{&team, &session, &sam}, &run},
	} {
		if err := raw.QueryRow(t.Context(), q.sql, q.args...).Scan(q.into); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if err := raw.QueryRow(t.Context(), `
		INSERT INTO approvals (team_id, run_id, root_run_id, call_id, tool, class, summary, code, requested_for, expires_at)
		VALUES ($1, $2, $2, 'c1', 'mail__send', 'write', 'to: a', 'ABCD', $3, now() + interval '1 hour') RETURNING id`,
		team, run, sam).Scan(&req); err != nil {
		t.Fatal(err)
	}

	r := &noResume{}
	d := decider{store: s, resumer: r}
	answer := approvals.Answer{ApprovalID: req, TeamID: team, By: uuid.New(), Approve: true, Via: "dashboard"}
	if _, err := d.Decide(t.Context(), answer); !errors.Is(err, approvals.ErrNotAllowed) {
		t.Fatalf("someone who may not answer: %v, want ErrNotAllowed", err)
	}

	answer.By, answer.MaySelf = sam, true
	got, err := d.Decide(t.Context(), answer)
	if err != nil || got.Status != "approved" || r.got != 1 {
		t.Fatalf("sam's own yes: %s, %v, resumed %d; want approved and the run resumed", got.Status, err, r.got)
	}
	if got, err := d.Decide(t.Context(), answer); !errors.Is(err, approvals.ErrNotPending) || got.Status != "approved" {
		t.Errorf("a second answer: %v, %s; want ErrNotPending with the request as it stands", err, got.Status)
	}
}
