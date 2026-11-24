package improvements

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// A proposal shows how it fared on past runs: off until tried, trying while
// a try is queued, and each try beside its run, the request withheld from a
// caller who may not read the run's conversation, as the trace is.
func TestAProposalShowsItsTriesOnPastRuns(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	group, _ := w.run(t, "group")
	direct, _ := w.run(t, "direct")
	p := w.mustPropose(t, w.instructionsChange(t, a))

	get := func() view {
		t.Helper()
		rec := w.serve(t, "GET", "/improvements/"+p.ID.String(), nil)
		var v view
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
			t.Fatalf("get = %d %s", rec.Code, rec.Body)
		}
		return v
	}
	if v := get(); v.Replay == nil || v.Replay.Status != replayOff || len(v.Replay.Runs) != 0 {
		t.Fatalf("an untried proposal = %+v, want off", v.Replay)
	}
	// Chosen for with none to try: none, not off.
	if _, err := w.raw.Exec(t.Context(), `UPDATE improvement_proposals SET replay_chosen_at = now() WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if v := get(); v.Replay.Status != replayNone {
		t.Fatalf("a proposal with no run to try = %s, want none", v.Replay.Status)
	}

	for i, run := range []uuid.UUID{group, direct} {
		if _, err := w.raw.Exec(t.Context(), `INSERT INTO improvement_replays (proposal_id, team_id, source_run_id, position, chosen, original)
			VALUES ($1, $2, $3, $4, 'cited', 'bad')`, p.ID, w.team, run, i); err != nil {
			t.Fatal(err)
		}
	}
	if v := get(); v.Replay.Status != replayTrying {
		t.Errorf("queued tries = %s, want trying", v.Replay.Status)
	}
	if _, err := w.raw.Exec(t.Context(), `UPDATE improvement_replays SET status = 'done', replayed = 'good', verdict = 'done',
		why = 'it did', input_tokens = 900, output_tokens = 100, steps = 2, work_ms = 3000, finished_at = now(),
		answer = 'Refunded.', trace = '[{"name":"orders__lookup","how":"recorded","arguments":"{}"}]'
		WHERE proposal_id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	v := get()
	if v.Replay.Status != replayDone || len(v.Replay.Runs) != 2 {
		t.Fatalf("done tries = %+v", v.Replay)
	}
	shown, held := v.Replay.Runs[0], v.Replay.Runs[1]
	if shown.Withheld || shown.Request == nil || shown.Why == nil || shown.After == nil || shown.After.Tokens != 1000 ||
		*shown.Replayed != "good" || !shown.Run.Readable || shown.Outcome == nil || *shown.Outcome != outcomeFixed {
		t.Errorf("a try on a group run = %+v", shown)
	}
	if !held.Withheld || held.Request != nil || held.Why != nil || held.Run.Readable {
		t.Errorf("a try on someone else's direct run = %+v, want its request and why withheld", held)
	}

	trace := func(run uuid.UUID) int {
		t.Helper()
		return w.serve(t, "GET", "/improvements/"+p.ID.String()+"/replays/"+run.String(), nil).Code
	}
	if code := trace(group); code != http.StatusOK {
		t.Errorf("a group run's trace = %d", code)
	}
	if code := trace(direct); code != http.StatusForbidden {
		t.Errorf("someone else's direct run's trace = %d, want 403", code)
	}
	if code := trace(uuid.New()); code != http.StatusNotFound {
		t.Errorf("a run never tried = %d, want 404", code)
	}

	// The list carries the same.
	rec := w.serve(t, "GET", "/improvements?status=waiting", nil)
	var page struct {
		Items []view `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 1 ||
		page.Items[0].Replay == nil || page.Items[0].Replay.Status != replayDone {
		t.Errorf("list = %d %s", rec.Code, rec.Body)
	}
}

// A failed try's error may quote the conversation: one the caller may not
// read says only that it failed.
func TestAFailedTrysErrorIsWithheldWithItsRun(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	group, _ := w.run(t, "group")
	direct, _ := w.run(t, "direct")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	for i, run := range []uuid.UUID{group, direct} {
		if _, err := w.raw.Exec(t.Context(), `INSERT INTO improvement_replays (proposal_id, team_id, source_run_id, position, chosen, original,
			status, error, finished_at) VALUES ($1, $2, $3, $4, 'cited', 'bad', 'failed', 'the model said: card 4242', now())`,
			p.ID, w.team, run, i); err != nil {
			t.Fatal(err)
		}
	}
	rec := w.serve(t, "GET", "/improvements/"+p.ID.String(), nil)
	var v view
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || len(v.Replay.Runs) != 2 {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if e := v.Replay.Runs[0].Error; e == nil || *e != "the model said: card 4242" {
		t.Errorf("a readable run's error = %v", e)
	}
	if e := v.Replay.Runs[1].Error; e == nil || *e != "the try failed" {
		t.Errorf("a withheld run's error = %v, want only that it failed", e)
	}
	if v.Replay.Status != replayNone {
		t.Errorf("every try failed = %s, want none", v.Replay.Status)
	}
}

func TestATryIsReadBesideItsRunsOutcome(t *testing.T) {
	good, bad, cant := "good", "bad", "cant_tell"
	for _, tc := range []struct {
		original string
		replayed *string
		want     string
	}{
		{"bad", &good, outcomeFixed},
		{"good", &good, outcomeKept},
		{"good", &bad, outcomeBroke},
		{"bad", &bad, outcomeStillBad},
		{"good", &cant, outcomeUnclear},
		{"bad", nil, outcomeUnclear},
	} {
		if got := outcomeOf(tc.original, tc.replayed); got != tc.want {
			t.Errorf("outcomeOf(%s, %v) = %s, want %s", tc.original, tc.replayed, got, tc.want)
		}
	}
}
