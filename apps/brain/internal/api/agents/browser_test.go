package agents

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Leaving the grant out is no browser, and the answer says so rather than
// leaving the field out.
func TestAnAgentCreatedWithoutABrowserHasNone(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), body("lead"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if decode(t, rec).Browser || s.created[0].Browser {
		t.Errorf("answered %v, stored %v; want no browser", decode(t, rec).Browser, s.created[0].Browser)
	}
	if !strings.Contains(rec.Body.String(), `"browser":false`) {
		t.Errorf("browser is not in the answer: %s", rec.Body)
	}
}

// The browser is a grant of its own: given without the web, it is the
// browser alone, and a PUT that says nothing of it takes it away.
func TestABrowserRoundTripsApartFromTheWeb(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	a := &fakeAuthz{allowed: true}
	u := memberOf(team)

	granted := body("clicker")
	granted["browser"] = true
	rec := call(t, s, a, u, http.MethodPost, collection(team), granted)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode(t, rec)
	if !created.Browser || !s.created[0].Browser || created.Web || s.created[0].Web {
		t.Fatalf("answered browser %v web %v, stored browser %v web %v; want the browser alone",
			created.Browser, created.Web, s.created[0].Browser, s.created[0].Web)
	}
	if got := decode(t, call(t, s, a, u, http.MethodGet, one(team, created.ID), nil)); !got.Browser {
		t.Error("read back with no browser")
	}

	rec = call(t, s, a, u, http.MethodPut, one(team, created.ID), body("clicker"))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if decode(t, rec).Browser || s.updated[0].Browser {
		t.Errorf("after a replace without the grant: answered %v, stored %v", decode(t, rec).Browser, s.updated[0].Browser)
	}
}

func TestABrowserMustBeABoolean(t *testing.T) {
	t.Parallel()

	s := newFakeStore()
	b := body("lead")
	b["browser"] = "yes"
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(uuid.New()), http.MethodPost, collection(uuid.New()), b)
	if rec.Code != http.StatusBadRequest || len(s.created) != 0 {
		t.Errorf("%d %s, %d created; want 400 and none", rec.Code, rec.Body, len(s.created))
	}
}

// What an agent is, said back as a request, keeps its browser: a blueprint
// exported from a team and applied again grants what it granted.
func TestTheRequestOfAnAgentKeepsItsBrowser(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "clicker")
	a.Browser = true
	req, err := RequestOf(a)
	if err != nil {
		t.Fatalf("RequestOf: %v", err)
	}
	if req.Browser == nil || !*req.Browser {
		t.Errorf("RequestOf browser = %v, want true", req.Browser)
	}
	cols, err := Columns(req)
	if err != nil || !cols.Browser {
		t.Errorf("Columns browser = %v, %v; want true", cols.Browser, err)
	}
}
