package approvals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// --- fakes ---

type fakeStore struct {
	approvals []db.Approval // newest first, as the query returns them
	hidden    map[uuid.UUID]bool
	settings  *db.TeamSetting
	rules     []db.ApprovalRule
	ruleErr   error

	listed []db.ListVisibleApprovalsParams
	saved  []db.SetTeamApprovalParams
}

func (s *fakeStore) ListVisibleApprovals(_ context.Context, arg db.ListVisibleApprovalsParams) ([]db.Approval, error) {
	s.listed = append(s.listed, arg)
	var out []db.Approval
	for _, a := range s.approvals {
		if a.TeamID != arg.TeamID || s.hidden[a.ID] && !arg.Moderator {
			continue
		}
		if len(arg.Statuses) > 0 && !slices.Contains(arg.Statuses, a.Status) {
			continue
		}
		if arg.UseCursor && !a.CreatedAt.Before(arg.AfterCreatedAt) {
			continue
		}
		out = append(out, a)
	}
	return out[:min(len(out), int(arg.PageSize))], nil
}

func (s *fakeStore) GetVisibleApproval(_ context.Context, arg db.GetVisibleApprovalParams) (db.Approval, error) {
	for _, a := range s.approvals {
		if a.ID == arg.ID && a.TeamID == arg.TeamID && (!s.hidden[a.ID] || arg.Moderator) {
			return a, nil
		}
	}
	return db.Approval{}, pgx.ErrNoRows
}

func (s *fakeStore) GetTeamSettings(context.Context, uuid.UUID) (db.TeamSetting, error) {
	if s.settings == nil {
		return db.TeamSetting{}, pgx.ErrNoRows
	}
	return *s.settings, nil
}

func (s *fakeStore) SetTeamApproval(_ context.Context, arg db.SetTeamApprovalParams) (db.TeamSetting, error) {
	s.saved = append(s.saved, arg)
	return db.TeamSetting{TeamID: arg.TeamID, ApprovalMode: arg.ApprovalMode, ApprovalTtlMinutes: arg.ApprovalTtlMinutes}, nil
}

func (s *fakeStore) ListApprovalRules(context.Context, uuid.UUID) ([]db.ApprovalRule, error) {
	return s.rules, nil
}

func (s *fakeStore) CreateApprovalRule(_ context.Context, arg db.CreateApprovalRuleParams) (db.ApprovalRule, error) {
	if s.ruleErr != nil {
		return db.ApprovalRule{}, s.ruleErr
	}
	r := db.ApprovalRule{
		ID: uuid.New(), TeamID: arg.TeamID, TargetKind: arg.TargetKind, Target: arg.Target,
		Mode: arg.Mode, RequireOther: arg.RequireOther, CreatedAt: time.Now(),
	}
	s.rules = append(s.rules, r)
	return r, nil
}

func (s *fakeStore) DeleteApprovalRule(_ context.Context, arg db.DeleteApprovalRuleParams) (int64, error) {
	for i, r := range s.rules {
		if r.ID == arg.ID && r.TeamID == arg.TeamID {
			s.rules = slices.Delete(s.rules, i, i+1)
			return 1, nil
		}
	}
	return 0, nil
}

// fakeAuthz holds the actions the caller may take; the route guard and the
// handler's own checks both ask it.
type fakeAuthz struct{ may map[authz.Action]bool }

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	return f.may[a], nil
}

func (f *fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	return false, nil
}

func allowing(actions ...authz.Action) *fakeAuthz {
	f := &fakeAuthz{may: map[authz.Action]bool{}}
	for _, a := range actions {
		f.may[a] = true
	}
	return f
}

type fakeDecider struct {
	got  []Answer
	row  db.Approval
	err  error
	none bool
}

func (d *fakeDecider) Decide(_ context.Context, a Answer) (db.Approval, error) {
	d.got = append(d.got, a)
	if d.none {
		return db.Approval{}, d.err
	}
	row := d.row
	if row.ID == uuid.Nil {
		row = db.Approval{ID: a.ApprovalID, TeamID: a.TeamID, Status: "approved"}
	}
	return row, d.err
}

type liveTeams struct{}

func (liveTeams) TeamDeleted(context.Context, uuid.UUID) (bool, error) { return false, nil }

func routes(t *testing.T) authz.Routes {
	t.Helper()
	rs := authz.NewRoutes()
	add := func(method, path, scope, permission string) {
		var p *string
		if permission != "" {
			p = &permission
		}
		if err := rs.Add(method, path, scope, p); err != nil {
			t.Fatalf("Add %s %s: %v", method, path, err)
		}
	}
	add("GET", "/teams/{id}/approvals", "team", "approval:read")
	add("GET", "/teams/{id}/approvals/{approvalID}", "team", "approval:read")
	add("POST", "/teams/{id}/approvals/{approvalID}/decision", "team", "approval:self")
	add("GET", "/teams/{id}/approval-settings", "member", "")
	add("PUT", "/teams/{id}/approval-settings", "team", "team:settings")
	add("GET", "/teams/{id}/approval-rules", "member", "")
	add("POST", "/teams/{id}/approval-rules", "team", "team:settings")
	add("DELETE", "/teams/{id}/approval-rules/{ruleID}", "team", "team:settings")
	return rs
}

type world struct {
	t       *testing.T
	team    uuid.UUID
	me      *auth.User
	store   *fakeStore
	authz   *fakeAuthz
	decider *fakeDecider
}

func newWorld(t *testing.T) *world {
	team := uuid.New()
	return &world{
		t: t, team: team,
		me: &auth.User{
			ID: uuid.New(), Issuer: "dev", Subject: "sam",
			Teams: []auth.Membership{{TeamID: team, Name: "platform", Role: "member"}},
		},
		store:   &fakeStore{hidden: map[uuid.UUID]bool{}},
		authz:   allowing("approval:read", "approval:self"),
		decider: &fakeDecider{},
	}
}

func (w *world) call(method, path string, body any) *httptest.ResponseRecorder {
	w.t.Helper()
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	New(logger, w.store, w.authz, w.decider).Register(mux, api.NewGuard(routes(w.t), w.authz, liveTeams{}, logger))

	var in io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		in = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			w.t.Fatal(err)
		}
		in = bytes.NewReader(raw)
	}
	req := httptest.NewRequestWithContext(w.t.Context(), method, path, in)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), w.me))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (w *world) request(status string, at time.Time) db.Approval {
	a := db.Approval{
		ID: uuid.New(), TeamID: w.team, RunID: uuid.New(), RootRunID: uuid.New(), CallID: "c1",
		Tool: "mail__send", Class: "write", Arguments: []byte(`{"to":"a@example.com"}`), Summary: "to: a@example.com",
		Code: "ABCD", Status: status, RequestedFor: &w.me.ID, ExpiresAt: at.Add(time.Hour), CreatedAt: at,
	}
	w.store.approvals = append(w.store.approvals, a)
	return a
}

func (w *world) path(rest string) string { return "/teams/" + w.team.String() + rest }

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return v
}

// --- listing and reading ---

// The list is a page of what the caller may see, newest first, the
// arguments as the call would make them; whether the caller moderates every
// conversation is read from their role, and a filter outside its set is a
// 400 before anything is read.
func TestTheListPagesWhatTheCallerMaySee(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	now := time.Now().UTC().Truncate(time.Second)
	newest, middle := w.request("pending", now), w.request("approved", now.Add(-time.Minute))
	hidden := w.request("pending", now.Add(-2*time.Minute))
	w.store.hidden[hidden.ID] = true

	rec := w.call(http.MethodGet, w.path("/approvals?limit=1"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	page := decode[api.Page[approval]](t, rec)
	if len(page.Items) != 1 || page.Items[0].ID != newest.ID || page.NextCursor == nil {
		t.Fatalf("page = %+v, want the newest and a cursor", page)
	}
	if got := page.Items[0]; string(got.Arguments) != `{"to":"a@example.com"}` || got.Code != "ABCD" {
		t.Errorf("item = %+v", got)
	}
	if p := w.store.listed[0]; p.Moderator || p.Viewer != w.me.ID {
		t.Errorf("listed as moderator %v, viewer %s; want only what %s may see", p.Moderator, p.Viewer, w.me.ID)
	}

	rec = w.call(http.MethodGet, w.path("/approvals?limit=5&cursor="+*page.NextCursor), nil)
	if rest := decode[api.Page[approval]](t, rec); len(rest.Items) != 1 || rest.Items[0].ID != middle.ID || rest.NextCursor != nil {
		t.Errorf("second page = %+v, want the middle one and no hidden request", rest)
	}

	run := uuid.New()
	w.call(http.MethodGet, w.path("/approvals?status=pending&run="+run.String()), nil)
	if p := w.store.listed[len(w.store.listed)-1]; !slices.Equal(p.Statuses, []string{"pending"}) || p.RootRunID == nil || *p.RootRunID != run {
		t.Errorf("filters = %v, run %v", p.Statuses, p.RootRunID)
	}

	before := len(w.store.listed)
	for _, q := range []string{"?status=waiting", "?run=nope", "?cursor=notacursor"} {
		if rec := w.call(http.MethodGet, w.path("/approvals"+q), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
	if len(w.store.listed) != before {
		t.Error("a bad filter still read the store")
	}

	w.authz.may["session:read_all"] = true
	w.call(http.MethodGet, w.path("/approvals"), nil)
	if p := w.store.listed[len(w.store.listed)-1]; !p.Moderator {
		t.Error("a moderator's list did not see every conversation")
	}
}

// A guest, who holds no approval:read, reads nothing: requests quote calls.
func TestAGuestReadsNoRequests(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.authz = allowing()
	for _, path := range []string{"/approvals", "/approvals/" + w.request("pending", time.Now()).ID.String()} {
		if rec := w.call(http.MethodGet, w.path(path), nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", path, rec.Code)
		}
	}
}

func TestOneRequestIsReadOnlyWhereItsRunIs(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	mine, theirs := w.request("pending", time.Now()), w.request("pending", time.Now())
	w.store.hidden[theirs.ID] = true

	if rec := w.call(http.MethodGet, w.path("/approvals/"+mine.ID.String()), nil); rec.Code != http.StatusOK ||
		decode[approval](t, rec).ID != mine.ID {
		t.Errorf("mine: status %d (%s)", rec.Code, rec.Body)
	}
	for _, id := range []string{theirs.ID.String(), uuid.NewString()} {
		if rec := w.call(http.MethodGet, w.path("/approvals/"+id), nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", id, rec.Code)
		}
	}
	if rec := w.call(http.MethodGet, w.path("/approvals/nope"), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad id: status %d, want 400", rec.Code)
	}
}

// --- answering ---

// An answer is the caller's, with what their role lets them answer read now,
// and where they answered.
func TestAnAnswerCarriesWhoMayAnswerAndWhere(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	a := w.request("pending", time.Now())

	rec := w.call(http.MethodPost, w.path("/approvals/"+a.ID.String()+"/decision"),
		map[string]any{"approve": true, "scope": "run", "reason": "fine"})
	if rec.Code != http.StatusOK || decode[approval](t, rec).Status != "approved" {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	got := w.decider.got[0]
	if got.ApprovalID != a.ID || got.TeamID != w.team || got.By != w.me.ID || !got.Approve || got.Scope != "run" ||
		got.Reason != "fine" || got.Via != "dashboard" || got.MayDecide || !got.MaySelf {
		t.Errorf("answer = %+v; want sam's own yes for the run, from the dashboard, deciding only their own", got)
	}

	w.authz.may["approval:decide"] = true
	w.call(http.MethodPost, w.path("/approvals/"+a.ID.String()+"/decision"), map[string]any{"approve": false, "via": "cli"})
	if got := w.decider.got[1]; got.Approve || got.Via != "cli" || !got.MayDecide {
		t.Errorf("answer = %+v; want a no from the cli by someone who decides", got)
	}
}

// A body that does not say yes or no, or says something it cannot mean, is
// refused before anyone is asked.
func TestABadAnswerIsRefusedBeforeItIsGiven(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	a := w.request("pending", time.Now())
	for name, body := range map[string]any{
		"no approve":   map[string]any{"reason": "?"},
		"a bad scope":  map[string]any{"approve": true, "scope": "forever"},
		"a no for run": map[string]any{"approve": false, "scope": "run"},
		"a bad via":    map[string]any{"approve": true, "via": "email"},
		"not json":     "{",
	} {
		if rec := w.call(http.MethodPost, w.path("/approvals/"+a.ID.String()+"/decision"), body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	if len(w.decider.got) != 0 {
		t.Errorf("a bad answer reached the decider: %+v", w.decider.got)
	}
}

// What the decider says becomes the status: not allowed is 403, already
// answered or expired is 409 with the request as it stands, gone is 404. A
// request the caller may not see is never handed to it.
func TestADecidersRefusalIsItsStatus(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		row  db.Approval
		none bool
		want int
		body string
	}{
		"not allowed":  {err: errors.Join(ErrNotAllowed, errors.New("x")), none: true, want: http.StatusForbidden},
		"not pending":  {err: ErrNotPending, row: db.Approval{ID: uuid.New(), Status: "denied"}, want: http.StatusConflict, body: "denied"},
		"expired":      {err: ErrExpired, row: db.Approval{ID: uuid.New(), Status: "expired"}, want: http.StatusConflict, body: "expired"},
		"gone":         {err: pgx.ErrNoRows, none: true, want: http.StatusNotFound},
		"failed":       {err: errors.New("database down"), none: true, want: http.StatusInternalServerError},
		"resume fails": {err: errors.New("enqueue refused"), row: db.Approval{ID: uuid.New(), Status: "approved"}, want: http.StatusOK, body: "approved"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			a := w.request("pending", time.Now())
			w.decider.err, w.decider.row, w.decider.none = tc.err, tc.row, tc.none
			rec := w.call(http.MethodPost, w.path("/approvals/"+a.ID.String()+"/decision"), map[string]any{"approve": true})
			if rec.Code != tc.want {
				t.Fatalf("status %d (%s), want %d", rec.Code, rec.Body, tc.want)
			}
			if tc.body != "" && decode[approval](t, rec).Status != tc.body {
				t.Errorf("body %s, want the request %s", rec.Body, tc.body)
			}
		})
	}

	w := newWorld(t)
	hidden := w.request("pending", time.Now())
	w.store.hidden[hidden.ID] = true
	if rec := w.call(http.MethodPost, w.path("/approvals/"+hidden.ID.String()+"/decision"), map[string]any{"approve": true}); rec.Code != http.StatusNotFound || len(w.decider.got) != 0 {
		t.Errorf("a request the caller may not see: status %d, decided %d time(s); want 404 and nothing", rec.Code, len(w.decider.got))
	}
	w.authz = allowing("approval:read")
	if rec := w.call(http.MethodPost, w.path("/approvals/"+uuid.NewString()+"/decision"), map[string]any{"approve": true}); rec.Code != http.StatusForbidden {
		t.Errorf("without approval:self: status %d, want 403", rec.Code)
	}
}

// --- settings and rules ---

func TestApprovalSettingsReadAndSave(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	if got := decode[settings](t, w.call(http.MethodGet, w.path("/approval-settings"), nil)); got.Mode != "off" || got.TTLMinutes != 60 {
		t.Errorf("a team that never chose reads %+v, want off for 60", got)
	}
	w.store.settings = &db.TeamSetting{ApprovalMode: "writes", ApprovalTtlMinutes: 30}
	if got := decode[settings](t, w.call(http.MethodGet, w.path("/approval-settings"), nil)); got.Mode != "writes" || got.TTLMinutes != 30 {
		t.Errorf("read %+v, want writes for 30", got)
	}

	if rec := w.call(http.MethodPut, w.path("/approval-settings"), settings{Mode: "all", TTLMinutes: 90}); rec.Code != http.StatusForbidden {
		t.Errorf("a member saved settings: status %d", rec.Code)
	}
	w.authz.may["team:settings"] = true
	rec := w.call(http.MethodPut, w.path("/approval-settings"), settings{Mode: "all", TTLMinutes: 90})
	if rec.Code != http.StatusOK || len(w.store.saved) != 1 || w.store.saved[0].ApprovalMode != "all" || w.store.saved[0].ApprovalTtlMinutes != 90 {
		t.Fatalf("status %d, saved %+v", rec.Code, w.store.saved)
	}
	for _, bad := range []settings{{Mode: "sometimes", TTLMinutes: 60}, {Mode: "writes", TTLMinutes: 4}, {Mode: "writes", TTLMinutes: 1441}} {
		if rec := w.call(http.MethodPut, w.path("/approval-settings"), bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%+v: status %d, want 400", bad, rec.Code)
		}
	}
	if len(w.store.saved) != 1 {
		t.Error("a refused save was written")
	}
}

func TestRulesAreAddedOncePerTargetAndRemoved(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.authz.may["team:settings"] = true
	rec := w.call(http.MethodPost, w.path("/approval-rules"),
		map[string]any{"target_kind": "server", "target": "  mail ", "mode": "deny", "require_other": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	added := decode[rule](t, rec)
	if added.Target != "mail" || added.Mode != "deny" || !added.RequireOther {
		t.Errorf("added %+v", added)
	}
	if page := decode[api.Page[rule]](t, w.call(http.MethodGet, w.path("/approval-rules"), nil)); len(page.Items) != 1 {
		t.Errorf("listed %+v", page)
	}

	for name, body := range map[string]map[string]any{
		"a bad kind":  {"target_kind": "agent", "target": "x", "mode": "ask"},
		"no target":   {"target_kind": "tool", "target": " ", "mode": "ask"},
		"a bad mode":  {"target_kind": "tool", "target": "x", "mode": "maybe"},
		"a long name": {"target_kind": "tool", "target": strings.Repeat("x", 201), "mode": "ask"},
	} {
		if rec := w.call(http.MethodPost, w.path("/approval-rules"), body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	w.store.ruleErr = &pgconn.PgError{Code: "23505"}
	if rec := w.call(http.MethodPost, w.path("/approval-rules"), map[string]any{"target_kind": "server", "target": "MAIL", "mode": "ask"}); rec.Code != http.StatusConflict {
		t.Errorf("a second rule for a target: status %d, want 409", rec.Code)
	}

	if rec := w.call(http.MethodDelete, w.path("/approval-rules/"+added.ID.String()), nil); rec.Code != http.StatusNoContent || len(w.store.rules) != 0 {
		t.Errorf("remove: status %d, rules left %d", rec.Code, len(w.store.rules))
	}
	if rec := w.call(http.MethodDelete, w.path("/approval-rules/"+added.ID.String()), nil); rec.Code != http.StatusNotFound {
		t.Errorf("remove again: status %d, want 404", rec.Code)
	}
}
