package agents

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Leaving the grant out is off the web, and the answer says so rather than
// leaving the field out: a client tells an agent without it from one whose
// brain is too old to know.
func TestAnAgentCreatedWithoutWebIsOffTheWeb(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), body("lead"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if decode(t, rec).Web || s.created[0].Web {
		t.Errorf("answered %v, stored %v; want off the web", decode(t, rec).Web, s.created[0].Web)
	}
	if !strings.Contains(rec.Body.String(), `"web":false`) {
		t.Errorf("web is not in the answer: %s", rec.Body)
	}
}

// A PUT is the whole agent, as it is for delegation: one that says nothing
// about the web takes the grant away.
func TestWebRoundTripsThroughCreateAndReplace(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	a := &fakeAuthz{allowed: true}
	u := memberOf(team)

	granted := body("researcher")
	granted["web"] = true
	rec := call(t, s, a, u, http.MethodPost, collection(team), granted)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode(t, rec)
	if !created.Web || !s.created[0].Web {
		t.Fatalf("answered %v, stored %v; want the grant", created.Web, s.created[0].Web)
	}
	if got := decode(t, call(t, s, a, u, http.MethodGet, one(team, created.ID), nil)); !got.Web {
		t.Error("read back off the web")
	}

	rec = call(t, s, a, u, http.MethodPut, one(team, created.ID), body("researcher"))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if decode(t, rec).Web || s.updated[0].Web {
		t.Errorf("after a replace without the grant: answered %v, stored %v", decode(t, rec).Web, s.updated[0].Web)
	}
}

func TestWebMustBeABoolean(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	for name, value := range map[string]any{"a string": "yes", "a number": 1} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newFakeStore()
			b := body("lead")
			b["web"] = value
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), b)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%d %s, want 400", rec.Code, rec.Body)
			}
			if len(s.created) != 0 {
				t.Errorf("%d created, want none", len(s.created))
			}
		})
	}
}

// The other direction of a whole PUT: an agent made without the grant gets
// it by a replace that says so.
func TestAReplaceCanGrantTheWeb(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	lead := anAgent(team, "lead")
	s := newFakeStore(lead)

	b := body("lead")
	b["web"] = true
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPut, one(team, lead.ID), b)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if !decode(t, rec).Web || !s.updated[0].Web {
		t.Errorf("answered %v, stored %v; want the grant", decode(t, rec).Web, s.updated[0].Web)
	}
}
