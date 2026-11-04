package teamsummary

import (
	"bytes"
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
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type fakeStore struct {
	row   db.TeamSummaryRow
	err   error
	asked []db.TeamSummaryParams
}

func (f *fakeStore) TeamSummary(_ context.Context, arg db.TeamSummaryParams) (db.TeamSummaryRow, error) {
	f.asked = append(f.asked, arg)
	return f.row, f.err
}

// fakeAuthz holds the permissions the caller has in the team, and fails the
// actions listed in broken, as an authorizer whose database is down would.
type fakeAuthz struct {
	holds  map[authz.Action]bool
	broken map[authz.Action]bool
	asked  []authz.Resource
}

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, r authz.Resource) (bool, error) {
	f.asked = append(f.asked, r)
	if f.broken[a] {
		return true, errors.New("connection reset by peer")
	}
	return f.holds[a], nil
}

func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	return false, nil
}

type liveTeams struct{}

func (liveTeams) TeamDeleted(context.Context, uuid.UUID) (bool, error) { return false, nil }

func call(t *testing.T, s Store, a *fakeAuthz, u *auth.User, path string, logs io.Writer) *httptest.ResponseRecorder {
	t.Helper()

	routes := authz.NewRoutes()
	if err := routes.Add(http.MethodGet, "/teams/{id}/summary", "member", nil); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	mux := http.NewServeMux()
	New(logger, s, a).Register(mux, api.NewGuard(routes, a, liveTeams{}, logger))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func memberOf(team uuid.UUID) *auth.User {
	return &auth.User{ID: uuid.New(), Subject: "someone", Teams: []auth.Membership{{TeamID: team, Role: "member"}}}
}

func of(team uuid.UUID) string { return "/teams/" + team.String() + "/summary" }

var counts = db.TeamSummaryRow{
	RunsQueued: 1, RunsRunning: 2, RunsSucceeded: 12, RunsFailed: 3, RunsCancelled: 0,
	Sessions: 128, WaitingSenders: 4, ServersFailing: 1,
}

// The response is the shape the spec draws: every status present, zero
// included, and each count where the dashboard looks for it.
func TestTheSummaryIsServedAsTheSpecDescribesIt(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	a := &fakeAuthz{holds: map[authz.Action]bool{readTheQueue: true}}
	rec := call(t, &fakeStore{row: counts}, a, memberOf(team), of(team), io.Discard)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	want := `{"runs_24h":{"queued":1,"running":2,"succeeded":12,"failed":3,"cancelled":0},` +
		`"sessions":128,"waiting_senders":4,"servers_failing":1}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body =\n %s\nwant\n %s", got, want)
	}
}

// Without channel:read the caller cannot list the queue, so it is not told
// how long the queue is either. Null, and present, so "you may not see this"
// is not mistaken for "nobody is waiting".
func TestWaitingSendersIsNullWithoutChannelRead(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, &fakeStore{row: counts}, &fakeAuthz{}, memberOf(team), of(team), io.Discard)

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("status %d, %v: %s", rec.Code, err, rec.Body)
	}
	if v, ok := raw["waiting_senders"]; !ok || v != nil {
		t.Errorf("waiting_senders = %v (present %v), want null and present", v, ok)
	}
	if raw["sessions"] != float64(128) {
		t.Errorf("the rest of the summary went with it: %s", rec.Body)
	}
}

// Whether a direct conversation counts is session:read_all, asked in this
// team and passed to the query with the caller as the viewer.
func TestTheSummaryCountsByTheCallersOwnView(t *testing.T) {
	t.Parallel()

	for _, moderator := range []bool{false, true} {
		team := uuid.New()
		u := memberOf(team)
		s := &fakeStore{}
		a := &fakeAuthz{holds: map[authz.Action]bool{readEveryConversation: moderator}}

		if rec := call(t, s, a, u, of(team), io.Discard); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		if len(s.asked) != 1 || s.asked[0] != (db.TeamSummaryParams{TeamID: team, Moderator: moderator, Viewer: u.ID}) {
			t.Errorf("moderator=%v: asked %+v", moderator, s.asked)
		}
		for _, r := range a.asked {
			if r.TeamID != team {
				t.Errorf("a permission was asked about team %s, not %s", r.TeamID, team)
			}
		}
	}
}

// A permission check that fails is logged and read as no: the summary counts
// less, never more. A broken check must not hand out the queue's length or
// every private conversation's runs.
func TestAFailedPermissionCheckCountsLessAndSaysSo(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	var logs bytes.Buffer
	s := &fakeStore{row: counts}
	a := &fakeAuthz{broken: map[authz.Action]bool{readEveryConversation: true, readTheQueue: true}}

	rec := call(t, s, a, memberOf(team), of(team), &logs)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if s.asked[0].Moderator {
		t.Error("a failed session:read_all check counted every private conversation")
	}
	if strings.Contains(rec.Body.String(), `"waiting_senders":4`) {
		t.Errorf("a failed channel:read check showed the queue: %s", rec.Body)
	}
	for _, want := range []string{"session:read_all", "channel:read", team.String()} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log does not name %q:\n%s", want, logs.String())
		}
	}
}

func TestAFailedSummaryIsA500ThatLeaksNothing(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, &fakeStore{err: errors.New("connection reset by peer")}, &fakeAuthz{}, memberOf(team), of(team), io.Discard)

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "reset by peer") ||
		strings.Contains(rec.Body.String(), "runs_24h") {
		t.Errorf("status %d %q, want a 500 that says nothing", rec.Code, rec.Body)
	}
}

// Somebody outside the team is not told it exists, and nothing is counted.
func TestAnotherTeamsSummaryIsNotFound(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := &fakeStore{row: counts}

	if rec := call(t, s, &fakeAuthz{}, memberOf(uuid.New()), of(team), io.Discard); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if len(s.asked) != 0 {
		t.Error("another team's summary was counted")
	}
}
