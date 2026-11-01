package teamgraph

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
)

type fakeViewer struct {
	snap      graph.Snapshot
	truncated bool
	err       error

	teams  []uuid.UUID
	limits []int
}

func (f *fakeViewer) View(_ context.Context, team uuid.UUID, limit int) (graph.Snapshot, bool, error) {
	f.teams = append(f.teams, team)
	f.limits = append(f.limits, limit)
	return f.snap, f.truncated, f.err
}

type allow struct{}

func (allow) IsSuperAdmin(*auth.User) bool { return false }
func (allow) Can(context.Context, *auth.User, authz.Action, authz.Resource) (bool, error) {
	return true, nil
}

func (allow) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	return true, nil
}

func call(t *testing.T, v Viewer, u *auth.User, path string) *httptest.ResponseRecorder {
	t.Helper()

	routes := authz.NewRoutes()
	if err := routes.Add(http.MethodGet, "/teams/{id}/graph", "member", nil); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	New(logger, v).Register(mux, api.NewGuard(routes, allow{}, liveTeams{}, logger))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	req = req.WithContext(auth.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func memberOf(team uuid.UUID) *auth.User {
	return &auth.User{ID: uuid.New(), Subject: "someone", Teams: []auth.Membership{{TeamID: team, Role: "member"}}}
}

func TestTheGraphIsServedAsTheSpecDescribesIt(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	agent, kit := uuid.New(), uuid.New()
	v := &fakeViewer{snap: graph.Snapshot{
		Nodes: []graph.Node{
			{Ref: graph.Ref{Label: graph.LabelTeam, ID: team}, Props: map[string]any{}},
			{Ref: graph.Ref{Label: graph.LabelAgent, ID: agent}, Props: map[string]any{"name": "triage", "description": "Sorts", "kind": "custom"}, Model: "m"},
			{Ref: graph.Ref{Label: graph.LabelToolkit, ID: kit}, Props: map[string]any{"name": "github", "kind": "mcp"}},
		},
		Edges: []graph.StoredEdge{
			{Type: graph.EdgeOwns, From: graph.Ref{Label: graph.LabelTeam, ID: team}, To: graph.Ref{Label: graph.LabelAgent, ID: agent}, Props: map[string]any{}},
			{Type: graph.EdgeGranted, From: graph.Ref{Label: graph.LabelAgent, ID: agent}, To: graph.Ref{Label: graph.LabelToolkit, ID: kit}, Props: map[string]any{"only": []any{"search"}}},
		},
	}, truncated: true}

	rec := call(t, v, memberOf(team), "/teams/"+team.String()+"/graph")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	var got view
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Nodes) != 3 || len(got.Edges) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.Nodes[1] != (node{ID: agent, Label: "Agent", Name: "triage", Description: "Sorts", Kind: "custom", Model: "m"}) {
		t.Errorf("agent = %+v", got.Nodes[1])
	}
	if e := got.Edges[1]; e.Type != "GRANTED" || e.From != agent || e.To != kit || len(e.Only) != 1 || e.Only[0] != "search" {
		t.Errorf("granted = %+v", e)
	}
	// Absent, not empty: the spec says a node with no vector has no model.
	if strings.Contains(rec.Body.String(), `"model":""`) || strings.Contains(rec.Body.String(), `"only":null`) {
		t.Errorf("empty fields were sent: %s", rec.Body)
	}
	if v.teams[0] != team || v.limits[0] != defaultLimit {
		t.Errorf("asked for %v with limit %v", v.teams, v.limits)
	}
}

func TestAnEmptyGraphIsTwoEmptyLists(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, &fakeViewer{}, memberOf(team), "/teams/"+team.String()+"/graph")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nodes":[]`) || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestTheLimitIsBounded(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	for raw, want := range map[string]int{"1": 1, "2000": 2000, "37": 37} {
		v := &fakeViewer{}
		if rec := call(t, v, memberOf(team), "/teams/"+team.String()+"/graph?limit="+raw); rec.Code != http.StatusOK || v.limits[0] != want {
			t.Errorf("limit=%s: status %d, asked for %v", raw, rec.Code, v.limits)
		}
	}
	for _, raw := range []string{"0", "-1", "2001", "lots", "1.5", ""} {
		v := &fakeViewer{}
		rec := call(t, v, memberOf(team), "/teams/"+team.String()+"/graph?limit="+raw)
		if raw == "" {
			if rec.Code != http.StatusOK || v.limits[0] != defaultLimit {
				t.Errorf("an empty limit: %d %v", rec.Code, v.limits)
			}
			continue
		}
		if rec.Code != http.StatusBadRequest || len(v.limits) != 0 {
			t.Errorf("limit=%s: status %d, and the graph was read %d times", raw, rec.Code, len(v.limits))
		}
	}
}

// No embedding model, no graph: a 503 that says so, and nothing else in the
// API depends on it.
func TestNoGraphIsServiceUnavailable(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, nil, memberOf(team), "/teams/"+team.String()+"/graph")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not enabled") {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

// A graph store that fails is a 503 naming the store, and the error itself
// stays in the log: it can carry an address.
func TestAFailingGraphStoreIsServiceUnavailable(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, &fakeViewer{err: errors.New("dial tcp 10.0.0.7:6379: refused")}, memberOf(team), "/teams/"+team.String()+"/graph")
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

// Another team's graph is not confirmed to exist, and is never read.
func TestAnotherTeamsGraphIsNotFound(t *testing.T) {
	t.Parallel()

	v := &fakeViewer{}
	rec := call(t, v, memberOf(uuid.New()), "/teams/"+uuid.NewString()+"/graph")
	if rec.Code != http.StatusNotFound || len(v.teams) != 0 {
		t.Errorf("status = %d, read %d times", rec.Code, len(v.teams))
	}
}

// liveTeams answers that no team is deleted. The guard's read-only rule for a
// deleted team is tested once, in internal/api; here it must not get in the way.
type liveTeams struct{}

func (liveTeams) TeamDeleted(context.Context, uuid.UUID) (bool, error) { return false, nil }
