package runs

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeFeedback is the store's feedback and outcomes, held in maps.
type fakeFeedback struct {
	feedback map[[2]uuid.UUID]db.RunFeedback // run, user
	outcomes map[uuid.UUID]db.RunOutcome
	reopened []uuid.UUID
}

func (s *fakeStore) UpsertRunFeedback(_ context.Context, arg db.UpsertRunFeedbackParams) (db.RunFeedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := db.RunFeedback{RunID: arg.RunID, TeamID: arg.TeamID, UserID: arg.UserID, Value: arg.Value, Note: arg.Note, Via: arg.Via, UpdatedAt: time.Now()}
	s.feedback[[2]uuid.UUID{arg.RunID, arg.UserID}] = f
	return f, nil
}

func (s *fakeStore) DeleteRunFeedback(_ context.Context, arg db.DeleteRunFeedbackParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]uuid.UUID{arg.RunID, arg.UserID}
	if _, ok := s.feedback[k]; !ok {
		return 0, nil
	}
	delete(s.feedback, k)
	return 1, nil
}

func (s *fakeStore) GetRunFeedback(_ context.Context, arg db.GetRunFeedbackParams) (db.RunFeedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.feedback[[2]uuid.UUID{arg.RunID, arg.UserID}]
	if !ok {
		return db.RunFeedback{}, pgx.ErrNoRows
	}
	return f, nil
}

func (s *fakeStore) CountRunFeedback(_ context.Context, runID uuid.UUID) (db.CountRunFeedbackRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c db.CountRunFeedbackRow
	for k, f := range s.feedback {
		if k[0] != runID {
			continue
		}
		if f.Value == "up" {
			c.Up++
		} else {
			c.Down++
		}
	}
	return c, nil
}

func (s *fakeStore) GetRunOutcome(_ context.Context, runID uuid.UUID) (db.RunOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outcomes[runID]
	if !ok {
		return db.RunOutcome{}, pgx.ErrNoRows
	}
	return o, nil
}

func (s *fakeStore) ReopenRunDigest(_ context.Context, runID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reopened = append(s.reopened, runID)
	return nil
}

func (s *fakeStore) ListMyFeedbackInSession(_ context.Context, arg db.ListMyFeedbackInSessionParams) ([]db.ListMyFeedbackInSessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.ListMyFeedbackInSessionRow
	for k, f := range s.feedback {
		if r, ok := s.runs[k[0]]; ok && r.SessionID == arg.SessionID && k[1] == arg.UserID {
			out = append(out, db.ListMyFeedbackInSessionRow{RunID: f.RunID, Value: f.Value, Note: f.Note, UpdatedAt: f.UpdatedAt})
		}
	}
	return out, nil
}

// improving turns the team's improvement on.
func (w *world) improving() {
	w.store.settings = &db.TeamSetting{TeamID: w.team, Improvement: true}
}

func (w *world) feedbackOf(id uuid.UUID) string { return w.one(id) + "/feedback" }

func TestFeedbackIsSavedAndReplaced(t *testing.T) {
	w := newWorld(t)
	w.improving()
	r := w.run(w.session("group", nil), "succeeded")

	rec := w.call(t, w.me, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "up"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"value":"up"`) {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	rec = w.call(t, w.me, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "down", "note": "  missed the order id  ", "via": "cli"})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace = %d %s", rec.Code, rec.Body)
	}
	f := w.store.feedback[[2]uuid.UUID{r.ID, w.me.ID}]
	if f.Value != "down" || f.Note != "missed the order id" || f.Via != "cli" || len(w.store.feedback) != 1 {
		t.Errorf("stored = %+v (%d rows)", f, len(w.store.feedback))
	}
}

// A 👎 reopens the run's digest; a 👍 does not.
func TestADownReopensTheDigest(t *testing.T) {
	w := newWorld(t)
	w.improving()
	r := w.run(w.session("group", nil), "succeeded")

	w.call(t, w.me, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "up"})
	if len(w.store.reopened) != 0 {
		t.Errorf("an up reopened the digest")
	}
	w.call(t, w.me, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "down"})
	if !slices.Equal(w.store.reopened, []uuid.UUID{r.ID}) {
		t.Errorf("reopened = %v, want the run", w.store.reopened)
	}
}

func TestFeedbackIsRefused(t *testing.T) {
	w := newWorld(t)
	w.improving()
	group := w.session("group", nil)
	r := w.run(group, "succeeded")
	child := w.run(group, "succeeded")
	child.ParentRunID = &r.ID
	w.store.runs[child.ID] = child
	theirs := w.run(w.session("direct", w.other), "succeeded")

	for name, tc := range map[string]struct {
		id   uuid.UUID
		body map[string]string
		want int
	}{
		"bad value":          {r.ID, map[string]string{"value": "meh"}, http.StatusBadRequest},
		"long note":          {r.ID, map[string]string{"value": "down", "note": strings.Repeat("é", 1001)}, http.StatusBadRequest},
		"bad via":            {r.ID, map[string]string{"value": "up", "via": "slack"}, http.StatusBadRequest},
		"a delegate's run":   {child.ID, map[string]string{"value": "up"}, http.StatusConflict},
		"a run I cannot see": {theirs.ID, map[string]string{"value": "up"}, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := w.call(t, w.me, http.MethodPut, w.feedbackOf(tc.id), tc.body); rec.Code != tc.want {
				t.Errorf("put = %d %s, want %d", rec.Code, rec.Body, tc.want)
			}
		})
	}
	if len(w.store.feedback) != 0 {
		t.Errorf("a refused feedback was stored: %v", w.store.feedback)
	}
}

// A team that has not opted in records nothing.
func TestFeedbackNeedsImprovementOn(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "succeeded")
	rec := w.call(t, w.me, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "up"})
	if rec.Code != http.StatusConflict || len(w.store.feedback) != 0 {
		t.Errorf("put with improvement off = %d %s, %d stored", rec.Code, rec.Body, len(w.store.feedback))
	}
}

func TestFeedbackCanBeRemoved(t *testing.T) {
	w := newWorld(t)
	w.improving()
	r := w.run(w.session("group", nil), "succeeded")
	w.call(t, w.me, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "up"})
	w.call(t, w.other, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "down"})

	if rec := w.call(t, w.me, http.MethodDelete, w.feedbackOf(r.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, mine := w.store.feedback[[2]uuid.UUID{r.ID, w.me.ID}]; mine || len(w.store.feedback) != 1 {
		t.Errorf("delete removed the wrong rows: %v", w.store.feedback)
	}
}

// The conversation view reads which replies the caller has already judged.
func TestSessionFeedbackIsMineOnly(t *testing.T) {
	w := newWorld(t)
	w.improving()
	s := w.session("group", nil)
	a, b := w.run(s, "succeeded"), w.run(s, "succeeded")
	w.call(t, w.me, http.MethodPut, w.feedbackOf(a.ID), map[string]string{"value": "up"})
	w.call(t, w.other, http.MethodPut, w.feedbackOf(b.ID), map[string]string{"value": "down"})

	rec := w.call(t, w.me, http.MethodGet, "/teams/"+w.team.String()+"/sessions/"+s.String()+"/feedback", nil)
	var page struct {
		Items []sessionFeedback `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 1 || page.Items[0].RunID != a.ID || page.Items[0].Value != "up" {
		t.Errorf("items = %+v, want only mine", page.Items)
	}

	hidden := w.session("direct", w.other)
	if rec := w.call(t, w.me, http.MethodGet, "/teams/"+w.team.String()+"/sessions/"+hidden.String()+"/feedback", nil); rec.Code != http.StatusNotFound {
		t.Errorf("someone else's direct session = %d, want 404", rec.Code)
	}
}

// A run's read carries its outcome and the caller's word once there are any.
func TestARunShowsItsOutcome(t *testing.T) {
	w := newWorld(t)
	w.improving()
	r := w.run(w.session("group", nil), "succeeded")

	if out := decodeRun(t, w.call(t, w.me, http.MethodGet, w.one(r.ID), nil)); out.Outcome != nil || out.MyFeedback != nil {
		t.Errorf("before judging: outcome %+v, mine %+v", out.Outcome, out.MyFeedback)
	}

	w.store.outcomes[r.ID] = db.RunOutcome{RunID: r.ID, Verdict: "done", Implicit: "none", Missed: "", CreatedAt: time.Now()}
	w.call(t, w.other, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "down"})
	w.call(t, w.me, http.MethodPut, w.feedbackOf(r.ID), map[string]string{"value": "up", "note": "fine"})

	out := decodeRun(t, w.call(t, w.me, http.MethodGet, w.one(r.ID), nil))
	if out.Outcome == nil || out.Outcome.Verdict != "done" || out.Outcome.Effective != "bad" ||
		out.Outcome.Feedback.Up != 1 || out.Outcome.Feedback.Down != 1 {
		t.Errorf("outcome = %+v, want done judged but bad by a person's down", out.Outcome)
	}
	if out.MyFeedback == nil || out.MyFeedback.Value != "up" || out.MyFeedback.Note != "fine" {
		t.Errorf("mine = %+v", out.MyFeedback)
	}
}
