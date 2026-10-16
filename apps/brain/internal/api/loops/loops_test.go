package loops

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeAuthz struct{ asked []authz.Action }

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	f.asked = append(f.asked, a)
	return true, nil
}

func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("the loops route used the strictly weaker CanInAnyTeam")
}

// loopRoutes is what rbac.json maps for this package, behind the real guard.
func loopRoutes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	if err := rs.Add("GET", "/teams/{id}/loops", "member", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return rs
}

func call(t *testing.T, loops []string, z *fakeAuthz, u *auth.User, method string, team uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), loops).Register(mux, api.NewGuard(loopRoutes(t), z, discardLogger()))

	req := httptest.NewRequestWithContext(t.Context(), method, "/teams/"+team.String()+"/loops", http.NoBody)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func memberOf(team uuid.UUID) *auth.User {
	return &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: team, Name: "platform", Role: "member"}},
	}
}

func names(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()

	var page struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	out := make([]string, len(page.Items))
	for i, l := range page.Items {
		out[i] = l.Name
	}
	return out
}

func TestTheLoopsRouteListsWhatTheRunnerRegisters(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, runtime.Loops(), &fakeAuthz{}, memberOf(team), http.MethodGet, team)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	if got := names(t, rec); !slices.Equal(got, runtime.Loops()) {
		t.Errorf("loops = %v, want %v", got, runtime.Loops())
	}
	if strings.Contains(rec.Body.String(), "next_cursor") {
		t.Errorf("%s has a next_cursor for a list that is never paged", rec.Body)
	}
}

// Every list in the API answers an array, so a client never checks for null.
func TestARunnerWithNoLoopsListsAnEmptyArray(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, nil, &fakeAuthz{}, memberOf(team), http.MethodGet, team)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("status %d, body %s; want 200 with \"items\":[]", rec.Code, rec.Body)
	}
}

// Member scope reads the memberships already on the request: an outsider is
// told the team does not exist, and nobody's permissions are looked up.
func TestTheLoopsRouteIsForMembersOnly(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	z := &fakeAuthz{}
	if rec := call(t, runtime.Loops(), z, memberOf(team), http.MethodGet, team); rec.Code != http.StatusOK {
		t.Errorf("member: %d (%s)", rec.Code, rec.Body)
	}
	if rec := call(t, runtime.Loops(), z, memberOf(uuid.New()), http.MethodGet, team); rec.Code != http.StatusNotFound {
		t.Errorf("a member of another team: %d, want 404", rec.Code)
	}
	if len(z.asked) != 0 {
		t.Errorf("the route asked for %v", z.asked)
	}
}

func TestTheLoopsRouteRefusesARequestWithNoUser(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, runtime.Loops(), &fakeAuthz{}, nil, http.MethodGet, team)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "react") {
		t.Errorf("status %d, body %s; want a 500 that lists nothing", rec.Code, rec.Body)
	}
}

func TestOnlyGETAnswers(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := call(t, runtime.Loops(), &fakeAuthz{}, memberOf(team), method, team)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "react") {
			t.Errorf("%s: %d %s; want it unanswered", method, rec.Code, rec.Body)
		}
	}
}

// A route mapped to a scope with no team in it is a mistake in rbac.json, and
// says so in the log rather than serving the list outside any team.
func TestTheListNeedsATeamFromItsScope(t *testing.T) {
	t.Parallel()

	h := &handler{logger: discardLogger(), items: []loop{{Name: "react"}}}
	rec := httptest.NewRecorder()
	h.list(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/teams/x/loops", http.NoBody))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "react") {
		t.Errorf("status %d, body %s; want a 500 that lists nothing", rec.Code, rec.Body)
	}
}
