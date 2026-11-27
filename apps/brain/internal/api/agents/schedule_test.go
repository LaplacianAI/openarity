package agents

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
)

// The schedule grant is off unless given, round-trips on its own, and a PUT
// that says nothing of it takes it away.
func TestTheScheduleGrantRoundTrips(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	a := &fakeAuthz{allowed: true}
	u := memberOf(team)

	rec := call(t, s, a, u, http.MethodPost, collection(team), body("lead"))
	if rec.Code != http.StatusCreated || decode(t, rec).Schedule || s.created[0].Schedule ||
		!strings.Contains(rec.Body.String(), `"schedule":false`) {
		t.Fatalf("created without it: %d %s, stored %v", rec.Code, rec.Body, s.created[0].Schedule)
	}

	granted := body("planner")
	granted["schedule"] = true
	rec = call(t, s, a, u, http.MethodPost, collection(team), granted)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode(t, rec)
	if !created.Schedule || !s.created[1].Schedule || created.Web || created.Browser {
		t.Fatalf("answered %+v, stored %v: want the schedule grant alone", created, s.created[1].Schedule)
	}
	if got := decode(t, call(t, s, a, u, http.MethodGet, one(team, created.ID), nil)); !got.Schedule {
		t.Error("read back without it")
	}
	rec = call(t, s, a, u, http.MethodPut, one(team, created.ID), body("planner"))
	if rec.Code != http.StatusOK || decode(t, rec).Schedule || s.updated[0].Schedule {
		t.Errorf("after a replace without it: %d, answered %v, stored %v", rec.Code, decode(t, rec).Schedule, s.updated[0].Schedule)
	}

	b := body("lead")
	b["schedule"] = "yes"
	if rec := call(t, s, a, u, http.MethodPost, collection(team), b); rec.Code != http.StatusBadRequest {
		t.Errorf("a schedule that is not a boolean: %d %s", rec.Code, rec.Body)
	}
}

// What an agent is, said back as a request, keeps its schedule grant: a
// blueprint exported and applied again grants what it granted.
func TestTheRequestOfAnAgentKeepsItsScheduleGrant(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "planner")
	a.Schedule = true
	req, err := harness.RequestOf(a)
	if err != nil || req.Schedule == nil || !*req.Schedule {
		t.Fatalf("RequestOf schedule = %v, %v", req.Schedule, err)
	}
	cols, err := harness.Columns(req)
	if err != nil || !cols.Schedule || !harness.UpdateParams(a.ID, cols).Schedule {
		t.Errorf("Columns schedule = %v, %v; want it kept through an update", cols.Schedule, err)
	}
}
