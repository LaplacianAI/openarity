package improvements

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// An approver reads the team's passes newest first, a page at a time, with
// each agent's name; anyone else is refused.
func TestPassesAreAnApproversToRead(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	var older, newer uuid.UUID
	if err := w.raw.QueryRow(t.Context(), `INSERT INTO improver_passes (team_id, agent_id, trigger, signatures, status, report, proposed, finished_at, started_at)
		VALUES ($1, $2, 'pattern', '{instructions::skipped_step}', 'done', 'Proposed one change.', 1, now(), now() - interval '1 hour') RETURNING id`,
		w.team, a.ID).Scan(&older); err != nil {
		t.Fatal(err)
	}
	if err := w.raw.QueryRow(t.Context(), `INSERT INTO improver_passes (team_id, agent_id, trigger) VALUES ($1, $2, 'daily') RETURNING id`,
		w.team, a.ID).Scan(&newer); err != nil {
		t.Fatal(err)
	}

	var page struct {
		Items      []passView `json:"items"`
		NextCursor string     `json:"next_cursor"`
	}
	rec := w.serve(t, "GET", "/improvements/passes?limit=1", nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 1 || page.Items[0].ID != newer || page.Items[0].Status != "running" || page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	rec = w.serve(t, "GET", "/improvements/passes?limit=1&cursor="+page.NextCursor, nil)
	page.Items, page.NextCursor = nil, ""
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("second page = %d %s", rec.Code, rec.Body)
	}
	p := page.Items
	if len(p) != 1 || p[0].ID != older || p[0].Agent.Name != "support-bot" || p[0].Report != "Proposed one change." ||
		p[0].Proposed != 1 || len(p[0].Signatures) != 1 || page.NextCursor != "" {
		t.Errorf("second page = %+v", page)
	}

	if rec := w.serve(t, "GET", "/improvements/passes?agent="+uuid.NewString(), nil); rec.Code != http.StatusOK || rec.Body.String() == "" {
		t.Errorf("another agent's = %d %s", rec.Code, rec.Body)
	}
	if rec := w.serve(t, "GET", "/improvements/passes?agent=nope", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad agent = %d", rec.Code)
	}
	w.holds = nil
	if rec := w.serve(t, "GET", "/improvements/passes", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a member without improvement:approve = %d, want 403", rec.Code)
	}
}
