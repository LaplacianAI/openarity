package agents

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func delegating(name, setting string, ids ...uuid.UUID) map[string]any {
	b := body(name)
	b["delegation"] = setting
	if ids != nil {
		b["delegates"] = ids
	}
	return b
}

func TestAnAgentReadsBackItsDelegation(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	clerk := anAgent(team, "clerk")
	s := newFakeStore(clerk)
	u := memberOf(team)
	a := &fakeAuthz{allowed: true}

	rec := call(t, s, a, u, http.MethodPost, collection(team), delegating("lead", "listed", clerk.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode(t, rec)
	if created.Delegation != "listed" || !slices.Equal(created.Delegates, []uuid.UUID{clerk.ID}) {
		t.Errorf("created %q %v, want listed [clerk]", created.Delegation, created.Delegates)
	}

	got := decode(t, call(t, s, a, u, http.MethodGet, one(team, created.ID), nil))
	if got.Delegation != "listed" || !slices.Equal(got.Delegates, []uuid.UUID{clerk.ID}) {
		t.Errorf("read back %q %v, want what was written", got.Delegation, got.Delegates)
	}
}

// Leaving the setting out is the default, and the list is an array, never null.
func TestAnAgentWithNoDelegationDelegatesToTheTeam(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), body("lead"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if got := decode(t, rec); got.Delegation != "team" || got.Delegates == nil || len(got.Delegates) != 0 {
		t.Errorf("delegation %q delegates %v, want team and []", got.Delegation, got.Delegates)
	}
	if !strings.Contains(rec.Body.String(), `"delegates":[]`) {
		t.Errorf("delegates is not an empty array: %s", rec.Body)
	}
	if s.created[0].Delegation != "team" {
		t.Errorf("stored %q, want team", s.created[0].Delegation)
	}
}

// A PUT is the whole agent: one that says nothing about delegation puts it
// back to the default and drops the list.
func TestReplacingWithoutDelegationResetsItToTeam(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	lead, clerk := anAgent(team, "lead"), anAgent(team, "clerk")
	lead.Delegation = "listed"
	s := newFakeStore(lead, clerk)
	s.delegates[lead.ID] = []uuid.UUID{clerk.ID}

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPut, one(team, lead.ID), body("lead"))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if got := decode(t, rec); got.Delegation != "team" || len(got.Delegates) != 0 {
		t.Errorf("after replace %q %v, want team and none", got.Delegation, got.Delegates)
	}
	if len(s.delegates[lead.ID]) != 0 {
		t.Errorf("the old list survived: %v", s.delegates[lead.ID])
	}
}

func TestReplaceSwapsTheDelegatesWhole(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	lead, a, b := anAgent(team, "lead"), anAgent(team, "a"), anAgent(team, "b")
	lead.Delegation = "listed"
	s := newFakeStore(lead, a, b)
	s.delegates[lead.ID] = []uuid.UUID{a.ID}

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPut, one(team, lead.ID), delegating("lead", "listed", b.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if got := s.delegates[lead.ID]; !slices.Equal(got, []uuid.UUID{b.ID}) {
		t.Errorf("delegates = %v, want only b", got)
	}
}

// Refused before anything is read or written.
func TestABadDelegationIsRefusedBeforeTheStore(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	other := uuid.New()
	for name, tc := range map[string]struct {
		body any
		says string
	}{
		"an unknown setting":             {delegating("lead", "some"), "delegation must be team, listed or none"},
		"an empty setting":               {delegating("lead", ""), "delegation must be team, listed or none"},
		"delegates under team":           {delegating("lead", "team", other), "delegates are only read when delegation is listed"},
		"delegates under none":           {delegating("lead", "none", other), "delegates are only read when delegation is listed"},
		"the same delegate listed twice": {delegating("lead", "listed", other, other), "delegates lists " + other.String() + " twice"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newFakeStore()
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), tc.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("%d %q, want 400 saying %q", rec.Code, rec.Body, tc.says)
			}
			if s.touched() {
				t.Error("the store was used for a refused body")
			}
		})
	}
}

// An omitted setting is team, but one sent as "" is a mistake, not the default.
func TestAnEmptyStringIsNotTheDefault(t *testing.T) {
	t.Parallel()

	rec := call(t, newFakeStore(), &fakeAuthz{allowed: true}, memberOf(uuid.New()), http.MethodPost,
		collection(uuid.New()), delegating("lead", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

func TestAnAgentCannotListItself(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	lead := anAgent(team, "lead")
	s := newFakeStore(lead)

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPut, one(team, lead.ID), delegating("lead", "listed", lead.ID))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "an agent cannot list itself") {
		t.Errorf("%d %q, want 400 saying it cannot list itself", rec.Code, rec.Body)
	}
	if s.txs != 0 {
		t.Error("a write was attempted")
	}
}

// Another team's agent, or one that does not exist, is refused by the
// database inside the transaction, and the agent is left as it was.
func TestADelegateOutsideTheTeamIsRefusedAndNothingChanges(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	lead, stranger := anAgent(team, "lead"), anAgent(uuid.New(), "stranger")
	s := newFakeStore(lead, stranger)
	a := &fakeAuthz{allowed: true}
	u := memberOf(team)

	for name, id := range map[string]uuid.UUID{"another team's": stranger.ID, "nobody's": uuid.New()} {
		rec := call(t, s, a, u, http.MethodPut, one(team, lead.ID), func() map[string]any {
			b := delegating("renamed", "listed", id)
			return b
		}())
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "a listed delegate is not an agent in this team") {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
	}
	if got := s.agents[lead.ID]; got.Name != "lead" || got.Delegation != "" {
		t.Errorf("the agent changed: %q %q", got.Name, got.Delegation)
	}
}

func TestAFailedDelegatesReadIsA500(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	lead := anAgent(team, "lead")
	s := newFakeStore(lead)
	s.delegatesErr = errAny

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodGet, one(team, lead.ID), nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "reset by peer") {
		t.Errorf("%d %q, want a 500 that says nothing", rec.Code, rec.Body)
	}
	// The status is committed by the first write, so only the body shows an
	// agent written after the failure.
	if strings.Contains(rec.Body.String(), `"id"`) {
		t.Errorf("the agent was written after the 500: %s", rec.Body)
	}
}
