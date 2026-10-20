package runs

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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var errAny = errors.New("connection reset by peer")

// --- fakes ---

// fakeStore keeps the database's visibility rule: a direct session, and every
// run in it, is its owner's or a moderator's; any other session is the team's.
type fakeStore struct {
	mu sync.Mutex

	agents   map[string]db.Agent
	sessions map[uuid.UUID]db.Session
	runs     map[uuid.UUID]db.Run
	events   map[uuid.UUID][]db.RunEvent

	startErr  error
	readErr   error
	started   []StartArgs
	cancelled []uuid.UUID
	visible   []db.GetRunVisibleParams
	calls     int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		agents: map[string]db.Agent{}, sessions: map[uuid.UUID]db.Session{},
		runs: map[uuid.UUID]db.Run{}, events: map[uuid.UUID][]db.RunEvent{},
	}
}

func (s *fakeStore) sees(sessionID, teamID, viewer uuid.UUID, moderator bool) bool {
	sess, ok := s.sessions[sessionID]
	if !ok || sess.TeamID != teamID {
		return false
	}
	return sess.Kind != "direct" || moderator || (sess.UserID != nil && *sess.UserID == viewer)
}

func (s *fakeStore) GetAgentByName(_ context.Context, arg db.GetAgentByNameParams) (db.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	a, ok := s.agents[arg.Name]
	if !ok || a.TeamID != arg.TeamID {
		return db.Agent{}, pgx.ErrNoRows
	}
	return a, nil
}

func (s *fakeStore) GetSessionVisible(_ context.Context, arg db.GetSessionVisibleParams) (db.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if !s.sees(arg.ID, arg.TeamID, arg.Viewer, arg.Moderator) {
		return db.Session{}, pgx.ErrNoRows
	}
	return s.sessions[arg.ID], nil
}

func (s *fakeStore) GetRunVisible(_ context.Context, arg db.GetRunVisibleParams) (db.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.visible = append(s.visible, arg)
	if s.readErr != nil {
		return db.Run{}, s.readErr
	}
	r, ok := s.runs[arg.ID]
	if !ok || r.TeamID != arg.TeamID || !s.sees(r.SessionID, arg.TeamID, arg.Viewer, arg.Moderator) {
		return db.Run{}, pgx.ErrNoRows
	}
	return r, nil
}

func (s *fakeStore) RequestRunCancel(_ context.Context, id uuid.UUID) (db.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	r := s.runs[id]
	if r.FinishedAt != nil {
		return db.Run{}, pgx.ErrNoRows
	}
	s.cancelled = append(s.cancelled, id)
	now := time.Now()
	r.CancelRequestedAt = &now
	if r.Status == "queued" {
		r.Status, r.FinishedAt = "cancelled", &now
	}
	s.runs[id] = r
	return r, nil
}

func (s *fakeStore) ListRunEvents(_ context.Context, arg db.ListRunEventsParams) ([]db.RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.RunEvent
	for _, e := range s.events[arg.RunID] {
		if e.Seq > arg.After && int32(len(out)) < arg.PageSize {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *fakeStore) StartRun(_ context.Context, arg StartArgs) (db.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.started = append(s.started, arg)
	if s.startErr != nil {
		return db.Run{}, s.startErr
	}
	session := uuid.New()
	if arg.SessionID != nil {
		session = *arg.SessionID
	}
	r := db.Run{
		ID: arg.ID, TeamID: arg.TeamID, SessionID: session, AgentID: &arg.AgentID, AgentName: "triage",
		UserID: &arg.UserID, Status: "queued", Input: arg.Input, CreatedAt: time.Now(),
	}
	s.runs[r.ID] = r
	return r, nil
}

func (s *fakeStore) touched() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type fakeStarter struct {
	mu  sync.Mutex
	ids []uuid.UUID
	err error
}

func (f *fakeStarter) Enqueue(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	return f.err
}

// fakeAuthz grants the actions it is given.
type fakeAuthz struct {
	grants map[authz.Action]bool
}

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	return f.grants[a], nil
}

func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("a runs route used the strictly weaker CanInAnyTeam")
}

func member() *fakeAuthz { return &fakeAuthz{grants: map[authz.Action]bool{"run:write": true}} }

func moderator() *fakeAuthz {
	return &fakeAuthz{grants: map[authz.Action]bool{"run:write": true, readEveryConversation: true}}
}

// --- the harness ---

// runRoutes is what rbac.json maps for this package, behind the real guard.
func runRoutes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	write := "run:write"
	for _, r := range []struct {
		method, path, scope string
		permission          *string
	}{
		{"POST", "/teams/{id}/runs", "team", &write},
		{"GET", "/teams/{id}/runs/{runID}", "member", nil},
		{"GET", "/teams/{id}/runs/{runID}/events", "member", nil},
		{"POST", "/teams/{id}/runs/{runID}/cancel", "team", &write},
	} {
		if err := rs.Add(r.method, r.path, r.scope, r.permission); err != nil {
			t.Fatalf("Add %s %s: %v", r.method, r.path, err)
		}
	}
	return rs
}

type world struct {
	store   *fakeStore
	starter *fakeStarter
	authz   *fakeAuthz
	team    uuid.UUID
	me      *auth.User
	other   *auth.User
	agent   db.Agent
	mux     *http.ServeMux
}

func newWorld(t *testing.T) *world {
	t.Helper()

	w := &world{store: newFakeStore(), starter: &fakeStarter{}, authz: member(), team: uuid.New()}
	w.me, w.other = w.person(), w.person()
	w.agent = db.Agent{ID: uuid.New(), TeamID: w.team, Name: "triage"}
	w.store.agents["triage"] = w.agent

	w.mux = http.NewServeMux()
	New(discardLogger(), w.store, w.authz, w.starter).Register(w.mux, api.NewGuard(runRoutes(t), w.authz, discardLogger()))
	return w
}

func (w *world) person() *auth.User {
	return &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: uuid.NewString(),
		Teams: []auth.Membership{{TeamID: w.team, Name: "platform", Role: "member"}},
	}
}

func (w *world) session(kind string, owner *auth.User) uuid.UUID {
	s := db.Session{ID: uuid.New(), TeamID: w.team, Kind: kind}
	if owner != nil {
		s.UserID = &owner.ID
	}
	w.store.sessions[s.ID] = s
	return s.ID
}

// run is a run in a session, as the store would hold it.
func (w *world) run(session uuid.UUID, status string) db.Run {
	r := db.Run{ID: uuid.New(), TeamID: w.team, SessionID: session, AgentName: "triage", Status: status, Input: "hi", CreatedAt: time.Now()}
	if status == "succeeded" || status == "failed" || status == "cancelled" {
		now := time.Now()
		r.FinishedAt = &now
	}
	w.store.runs[r.ID] = r
	return r
}

func (w *world) call(t *testing.T, u *auth.User, method, path string, body any, header ...string) *httptest.ResponseRecorder {
	t.Helper()

	var in io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		in = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		in = bytes.NewReader(raw)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, in)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

func (w *world) runs() string { return "/teams/" + w.team.String() + "/runs" }

func (w *world) one(id uuid.UUID) string { return w.runs() + "/" + id.String() }

func decodeRun(t *testing.T, rec *httptest.ResponseRecorder) run {
	t.Helper()

	var out run
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return out
}

func start(input string, session *uuid.UUID) map[string]any {
	b := map[string]any{"agent": "triage", "input": input}
	if session != nil {
		b["session_id"] = session.String()
	}
	return b
}

// --- starting ---

func TestStartingARunReturns202AndEnqueuesIt(t *testing.T) {
	w := newWorld(t)

	rec := w.call(t, w.me, http.MethodPost, w.runs(), start("triage the bug reports", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d (%s), want 202", rec.Code, rec.Body)
	}
	got := decodeRun(t, rec)
	if got.Status != "queued" || got.Agent != "triage" || got.Input != "triage the bug reports" ||
		got.Output != nil || got.Error != nil || got.StartedAt != nil || got.FinishedAt != nil {
		t.Errorf("run = %+v", got)
	}
	if len(w.starter.ids) != 1 || w.starter.ids[0] != got.ID {
		t.Errorf("enqueued %v, want the run %s", w.starter.ids, got.ID)
	}
	a := w.store.started[0]
	if a.TeamID != w.team || a.AgentID != w.agent.ID || a.UserID != w.me.ID || a.Input != "triage the bug reports" {
		t.Errorf("started with %+v", a)
	}
}

// A run started through the API names no session unless it means one; the
// store gives it a direct session of the caller's own.
func TestARunWithNoSessionGetsADirectSessionOfItsOwn(t *testing.T) {
	w := newWorld(t)

	if rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", nil)); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d", rec.Code)
	}
	if a := w.store.started[0]; a.SessionID != nil || a.UserID != w.me.ID {
		t.Errorf("started with session %v for %s; want none named, owned by the caller", a.SessionID, a.UserID)
	}
}

func TestARunCanBeStartedInATeamSession(t *testing.T) {
	w := newWorld(t)
	group := w.session("group", nil)

	rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", &group))

	if rec.Code != http.StatusAccepted || decodeRun(t, rec).SessionID != group {
		t.Errorf("status %d (%s), want 202 in the group session", rec.Code, rec.Body)
	}
}

func TestARunCannotBeStartedInSomeoneElsesDirectSession(t *testing.T) {
	w := newWorld(t)
	theirs := w.session("direct", w.other)

	rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", &theirs))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
	if len(w.store.started) != 0 || len(w.starter.ids) != 0 {
		t.Errorf("started %d and enqueued %d after the refusal", len(w.store.started), len(w.starter.ids))
	}
}

// A moderator may follow any conversation, and so may start a run in one.
func TestAModeratorCanStartARunInADirectSession(t *testing.T) {
	w := newWorld(t)
	w.authz.grants = moderator().grants
	theirs := w.session("direct", w.other)

	if rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", &theirs)); rec.Code != http.StatusAccepted {
		t.Errorf("status %d (%s), want 202", rec.Code, rec.Body)
	}
}

func TestASessionOfAnotherTeamIsNotFound(t *testing.T) {
	w := newWorld(t)
	foreign := db.Session{ID: uuid.New(), TeamID: uuid.New(), Kind: "group"}
	w.store.sessions[foreign.ID] = foreign

	if rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", &foreign.ID)); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// The row is committed before the enqueue, and the sweep starts any queued run
// older than 30 s, so a failed enqueue is not the caller's to retry.
func TestAFailedEnqueueStillAnswers202(t *testing.T) {
	w := newWorld(t)
	w.starter.err = errAny

	rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", nil))

	if rec.Code != http.StatusAccepted || decodeRun(t, rec).Status != "queued" {
		t.Errorf("status %d (%s), want 202 with the queued run", rec.Code, rec.Body)
	}
}

func TestARefusedBodyNeverReachesTheStore(t *testing.T) {
	w := newWorld(t)

	for name, tc := range map[string]struct {
		body any
		says string
	}{
		"no agent":          {map[string]any{"input": "hi"}, "agent is required"},
		"no input":          {map[string]any{"agent": "triage"}, "input is required"},
		"empty input":       {start("", nil), "input is required"},
		"input over 100KiB": {start(strings.Repeat("a", maxInputBytes+1), nil), "at most 100 KiB"},
		"a NUL in input":    {start("a\x00b", nil), "NUL"},
		"an unknown field":  {map[string]any{"agent": "triage", "input": "hi", "user_id": "x"}, "invalid request body"},
		"not JSON":          {"{", "invalid request body"},
		"a bad session id":  {map[string]any{"agent": "triage", "input": "hi", "session_id": "nope"}, "invalid request body"},
	} {
		t.Run(name, func(t *testing.T) {
			before := w.store.touched()
			rec := w.call(t, w.me, http.MethodPost, w.runs(), tc.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("status %d %q, want 400 saying %q", rec.Code, rec.Body, tc.says)
			}
			if n := w.store.touched() - before; n != 0 {
				t.Errorf("the store was asked %d things", n)
			}
		})
	}
}

func TestAnInputOfExactly100KiBIsAccepted(t *testing.T) {
	w := newWorld(t)

	if rec := w.call(t, w.me, http.MethodPost, w.runs(), start(strings.Repeat("a", maxInputBytes), nil)); rec.Code != http.StatusAccepted {
		t.Errorf("status %d (%s), want 202", rec.Code, rec.Body)
	}
}

func TestAnUnknownAgentIsRefusedByName(t *testing.T) {
	w := newWorld(t)
	b := start("hi", nil)
	b["agent"] = "telepath"

	rec := w.call(t, w.me, http.MethodPost, w.runs(), b)

	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no agent in this team has that name") {
		t.Errorf("status %d %q, want 404 naming the agent as missing", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "telepath") {
		t.Errorf("%q repeats the name back", rec.Body)
	}
	if len(w.store.started) != 0 {
		t.Error("a run was started for an agent that does not exist")
	}
}

// CreateRun selects the agent within the team as it inserts, so one deleted
// after the name was read leaves no row and the same answer.
func TestAnAgentDeletedWhileStartingIsNotFound(t *testing.T) {
	w := newWorld(t)
	w.store.startErr = pgx.ErrNoRows

	rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", nil))

	if rec.Code != http.StatusNotFound || len(w.starter.ids) != 0 {
		t.Errorf("status %d, %d enqueued; want 404 and nothing enqueued", rec.Code, len(w.starter.ids))
	}
}

func TestAStartThatFailsIsA500ThatLeaksNothing(t *testing.T) {
	w := newWorld(t)
	w.store.startErr = errAny

	rec := w.call(t, w.me, http.MethodPost, w.runs(), start("hi", nil))

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "reset by peer") {
		t.Errorf("status %d %q, want a 500 that says nothing", rec.Code, rec.Body)
	}
	if len(w.starter.ids) != 0 {
		t.Error("a run that was not written was enqueued")
	}
}

// --- reading and cancelling ---

func TestTheRunIsReadAsItIs(t *testing.T) {
	w := newWorld(t)
	out, msg := "done", "boom"
	r := w.run(w.session("group", nil), "succeeded")
	r.Output, r.Error, r.InputTokens, r.OutputTokens = &out, &msg, 120, 30
	w.store.runs[r.ID] = r

	rec := w.call(t, w.me, http.MethodGet, w.one(r.ID), nil)

	got := decodeRun(t, rec)
	if rec.Code != http.StatusOK || got.ID != r.ID || *got.Output != "done" || *got.Error != "boom" ||
		got.InputTokens != 120 || got.OutputTokens != 30 || got.FinishedAt == nil {
		t.Errorf("status %d, run %+v", rec.Code, got)
	}
	keys := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &keys)
	for _, k := range []string{
		"id", "session_id", "agent", "status", "input", "output", "error",
		"input_tokens", "output_tokens", "created_at", "started_at", "finished_at",
	} {
		if _, ok := keys[k]; !ok {
			t.Errorf("the run has no %q", k)
		}
	}
	if len(keys) != 12 {
		t.Errorf("the run has %d fields, want the 12 in the contract", len(keys))
	}
}

func TestAnotherPersonsDirectRunIsNotFound(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("direct", w.other), "running")

	for name, tc := range map[string]struct{ method, path string }{
		"get":    {http.MethodGet, w.one(r.ID)},
		"cancel": {http.MethodPost, w.one(r.ID) + "/cancel"},
		"events": {http.MethodGet, w.one(r.ID) + "/events"},
	} {
		rec := w.call(t, w.me, tc.method, tc.path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, rec.Code)
		}
	}
	if len(w.store.cancelled) != 0 {
		t.Error("a refused cancel reached the store")
	}
}

func TestAModeratorCanReadADirectRun(t *testing.T) {
	w := newWorld(t)
	w.authz.grants = moderator().grants
	r := w.run(w.session("direct", w.other), "running")

	if rec := w.call(t, w.me, http.MethodGet, w.one(r.ID), nil); rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200", rec.Code)
	}
	if p := w.store.visible[0]; !p.Moderator || p.Viewer != w.me.ID || p.TeamID != w.team {
		t.Errorf("read with %+v", p)
	}
}

func TestTheOwnerOfADirectRunCanReadIt(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("direct", w.me), "running")

	if rec := w.call(t, w.me, http.MethodGet, w.one(r.ID), nil); rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200", rec.Code)
	}
	if w.store.visible[0].Moderator {
		t.Error("a member without session:read_all was read as a moderator")
	}
}

// Nothing is holding a queued run, so cancelling it is the whole of it.
func TestCancellingAQueuedRunNeedsNoWorker(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "queued")

	rec := w.call(t, w.me, http.MethodPost, w.one(r.ID)+"/cancel", nil)

	if got := decodeRun(t, rec); rec.Code != http.StatusAccepted || got.Status != "cancelled" || got.FinishedAt == nil {
		t.Errorf("status %d, run %+v; want 202 and the run cancelled", rec.Code, got)
	}
}

func TestCancellingARunningRunAsksItsWorker(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "running")

	rec := w.call(t, w.me, http.MethodPost, w.one(r.ID)+"/cancel", nil)

	if got := decodeRun(t, rec); rec.Code != http.StatusAccepted || got.Status != "running" {
		t.Errorf("status %d, run %+v; want 202 with it still running until its heartbeat", rec.Code, got)
	}
}

func TestCancellingAFinishedRunIsAConflict(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "succeeded")

	if rec := w.call(t, w.me, http.MethodPost, w.one(r.ID)+"/cancel", nil); rec.Code != http.StatusConflict {
		t.Errorf("status %d, want 409", rec.Code)
	}
}

func TestANonUUIDRunIDIs400(t *testing.T) {
	w := newWorld(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, w.runs() + "/nope"},
		{http.MethodPost, w.runs() + "/nope/cancel"},
		{http.MethodGet, w.runs() + "/nope/events"},
	} {
		if rec := w.call(t, w.me, tc.method, tc.path, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
	if n := w.store.touched(); n != 0 {
		t.Errorf("the store was asked %d things", n)
	}
}

func TestAReadThatFailsIsA500ThatLeaksNothing(t *testing.T) {
	w := newWorld(t)
	w.store.readErr = errAny

	rec := w.call(t, w.me, http.MethodGet, w.one(uuid.New()), nil)

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "reset") {
		t.Errorf("status %d %q", rec.Code, rec.Body)
	}
}

// --- who may call ---

func TestStartingAndCancellingNeedRunWrite(t *testing.T) {
	w := newWorld(t)
	w.authz.grants = map[authz.Action]bool{}
	r := w.run(w.session("group", nil), "running")

	for name, path := range map[string]string{"start": w.runs(), "cancel": w.one(r.ID) + "/cancel"} {
		rec := w.call(t, w.me, http.MethodPost, path, start("hi", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, rec.Code)
		}
	}
	if len(w.store.started) != 0 || len(w.store.cancelled) != 0 {
		t.Error("a refused write reached the store")
	}
}

func TestAnOutsiderIsToldTheTeamDoesNotExist(t *testing.T) {
	w := newWorld(t)
	outsider := &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"}
	r := w.run(w.session("group", nil), "running")

	if rec := w.call(t, outsider, http.MethodGet, w.one(r.ID), nil); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

func TestEveryRouteRefusesARequestWithNoUser(t *testing.T) {
	w := newWorld(t)
	id := uuid.New()

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, w.runs()},
		{http.MethodGet, w.one(id)},
		{http.MethodGet, w.one(id) + "/events"},
		{http.MethodPost, w.one(id) + "/cancel"},
	} {
		if rec := w.call(t, nil, tc.method, tc.path, start("hi", nil)); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s: status %d, want 500", tc.method, tc.path, rec.Code)
		}
	}
	if n := w.store.touched(); n != 0 {
		t.Errorf("the store was asked %d things", n)
	}
}
