package improvements

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Approving starts the change's watch in the same transaction; reverting
// ends it, keeping what it counted; the view shows it.
func TestApproveStartsAWatchAndRevertEndsIt(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	if _, err := w.store.GetImprovementWatch(t.Context(), p.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a waiting proposal has a watch: %v", err)
	}
	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	watch, err := w.store.GetImprovementWatch(t.Context(), p.ID)
	if err != nil || watch.Status != "watching" || len(watch.AgentIds) != 1 || watch.AgentIds[0] != a.ID {
		t.Fatalf("watch = %+v, %v", watch, err)
	}
	rec := w.serve(t, http.MethodGet, "/improvements/"+p.ID.String(), nil)
	var v struct {
		Watch *struct {
			Status string `json:"status"`
			Needed int    `json:"needed"`
		} `json:"watch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Watch == nil || v.Watch.Status != "watching" || v.Watch.Needed != 10 {
		t.Errorf("view watch = %+v (%v): %s", v.Watch, err, rec.Body)
	}

	if rec := w.decide(t, p.ID, "revert"); rec.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", rec.Code, rec.Body)
	}
	if watch, _ := w.store.GetImprovementWatch(t.Context(), p.ID); watch.Status != "ended" || watch.EndedWhy == nil || *watch.EndedWhy != "reverted" {
		t.Errorf("after revert: %+v", watch)
	}
}

// An approve that is refused starts no watch.
func TestARefusedApproveStartsNoWatch(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	if _, err := w.raw.Exec(t.Context(), `UPDATE agents SET instructions = 'by hand' WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusConflict {
		t.Fatalf("approve = %d, want 409", rec.Code)
	}
	if _, err := w.store.GetImprovementWatch(t.Context(), p.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a refused approve left a watch: %v", err)
	}
}

// Clearing keeps a flagged change and needs only the approve permission; it
// is refused on a change its watch has not flagged. Flagged changes list
// under their own status, which stands alone.
func TestClearingAFlaggedChange(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if rec := w.decide(t, p.ID, "clear"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not flagged") {
		t.Fatalf("clear of a watching change = %d %s, want 409 not flagged", rec.Code, rec.Body)
	}

	if _, err := w.raw.Exec(t.Context(), `UPDATE improvement_watches SET status = 'flagged', flag = 'worse', finished_at = now() WHERE proposal_id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	listed := func() int {
		rec := w.serve(t, http.MethodGet, "/improvements?status=flagged", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list flagged: %d %s", rec.Code, rec.Body)
		}
		var page struct {
			Items []struct{ ID string } `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}
	if n := listed(); n != 1 {
		t.Fatalf("flagged lists %d, want 1", n)
	}
	if rec := w.serve(t, http.MethodGet, "/improvements?status=flagged&status=waiting", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("flagged with another status = %d, want 400", rec.Code)
	}

	delete(w.holds, agentWrite)
	delete(w.holds, skillWrite)
	if rec := w.decide(t, p.ID, "clear"); rec.Code != http.StatusOK {
		t.Fatalf("clear with approve only = %d %s", rec.Code, rec.Body)
	}
	watch, _ := w.store.GetImprovementWatch(t.Context(), p.ID)
	if watch.Status != "cleared" || watch.ClearedBy == nil || *watch.ClearedBy != w.user || watch.ClearedAt == nil {
		t.Errorf("after clear: %+v", watch)
	}
	if s := w.status(t, p.ID); s != "applied" {
		t.Errorf("status = %s, want still applied", s)
	}
	if n := listed(); n != 0 {
		t.Errorf("a cleared change still lists as flagged")
	}
}
