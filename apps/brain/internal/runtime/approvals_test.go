package runtime

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// held is the fixture's run as a worker holds it, with requests asked for
// under it: what a run that has just asked looks like, before it parks.
func (f *execFixture) held(t *testing.T, calls ...string) []db.Approval {
	t.Helper()
	f.mustExec(t, `UPDATE runs SET status = 'running', worker = $2, heartbeat_at = now(), started_at = now() WHERE id = $1`, f.run, worker)
	out := make([]db.Approval, len(calls))
	for i, call := range calls {
		a, err := f.store.InsertApproval(t.Context(), db.InsertApprovalParams{
			TeamID: f.team, RunID: f.run, RootRunID: f.run, CallID: call, Tool: "mail__send", Class: "write",
			Arguments: []byte(`{}`), Summary: "", Code: []string{"ABCD", "WXYZ"}[i], ExpiresAt: fixedNow.AddDate(1, 0, 0),
		})
		if err != nil {
			t.Fatalf("InsertApproval: %v", err)
		}
		out[i] = a
	}
	return out
}

func (f *execFixture) decide(t *testing.T, a db.Approval, status string, allowed func(db.Approval) error) (*Resumed, error) {
	t.Helper()
	if allowed == nil {
		allowed = func(db.Approval) error { return nil }
	}
	var by uuid.UUID
	if err := f.raw.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('dev', $1) RETURNING id`, uuid.NewString()).Scan(&by); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, again, err := decideApproval(t.Context(), f.store, decision{
		ID: a.ID, TeamID: f.team, By: by, Status: status, Scope: "once", Via: "dashboard", Allowed: allowed,
	})
	return again, err
}

// A person can answer before the run has finished parking. The answer is
// written and, the run still being held, nothing is queued; the park then
// sees nothing pending and queues the run itself, as its next attempt.
func TestAnAnswerThatBeatsTheParkQueuesTheRunAtThePark(t *testing.T) {
	f := newExecFixture(t, newScripted())
	a := f.held(t, "c1")[0]

	if again, err := f.decide(t, a, "approved", nil); err != nil || again != nil {
		t.Fatalf("an answer while the run is held = %v, queued %v; want no requeue yet", err, again)
	}
	parked, again, _, err := parkAndAsk(t.Context(), f.store, f.run, worker, false, nil, []byte(`{}`))
	if err != nil || !parked || again == nil || again.Attempt != 1 {
		t.Fatalf("park = %v, %v, %v; want parked and queued again at attempt 1", parked, again, err)
	}
	if row := f.row(t); row.Status != "queued" || row.WaitingSince != nil || row.Worker != nil {
		t.Errorf("the run is %s, waiting since %v, worker %v; want queued", row.Status, row.WaitingSince, row.Worker)
	}
}

// A run waiting on two requests is queued again only once both are answered.
func TestARunWaitsForEveryRequestOfItsTree(t *testing.T) {
	f := newExecFixture(t, newScripted())
	asked := f.held(t, "c1", "c2")
	if parked, again, _, err := parkAndAsk(t.Context(), f.store, f.run, worker, false, nil, []byte(`{}`)); err != nil || !parked || again != nil {
		t.Fatalf("park = %v, %v, %v; want parked and waiting", parked, again, err)
	}

	if again, err := f.decide(t, asked[0], "approved", nil); err != nil || again != nil {
		t.Fatalf("the first answer: %v, queued %v; want still waiting on the second", err, again)
	}
	again, err := f.decide(t, asked[1], "denied", nil)
	if err != nil || again == nil || again.ID != f.run {
		t.Fatalf("the second answer: %v, queued %v; want the run queued", err, again)
	}
}

// Only the worker that holds a run parks it.
func TestOnlyTheHoldingWorkerParksARun(t *testing.T) {
	f := newExecFixture(t, newScripted())
	f.held(t, "c1")
	parked, _, _, err := parkAndAsk(t.Context(), f.store, f.run, "someone-else", false, nil, []byte(`{}`))
	if err != nil || parked {
		t.Fatalf("another worker's park = %v, %v; want nothing parked", parked, err)
	}
	if row := f.row(t); row.Status != "running" {
		t.Errorf("the run is %s, want running, untouched", row.Status)
	}
}

// An answer someone may not give changes nothing: the request is still
// waiting, nothing is said on the run, and nothing is queued.
func TestARefusedAnswerChangesNothing(t *testing.T) {
	f := newExecFixture(t, newScripted())
	a := f.held(t, "c1")[0]
	if _, _, _, err := parkAndAsk(t.Context(), f.store, f.run, worker, false, nil, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	no := errors.New("not yours")
	if again, err := f.decide(t, a, "approved", func(db.Approval) error { return no }); !errors.Is(err, no) || again != nil {
		t.Fatalf("a refused answer = %v, queued %v; want the refusal and nothing queued", err, again)
	}
	got, err := f.store.GetApproval(t.Context(), db.GetApprovalParams{ID: a.ID, TeamID: f.team})
	if err != nil || got.Status != "pending" {
		t.Errorf("the request is %s, %v; want pending", got.Status, err)
	}
	for _, e := range f.events(t) {
		if e.Kind == "approval_decided" {
			t.Error("a refused answer was recorded on the run")
		}
	}
	if row := f.row(t); row.Status != "waiting" {
		t.Errorf("the run is %s, want still waiting", row.Status)
	}
}
