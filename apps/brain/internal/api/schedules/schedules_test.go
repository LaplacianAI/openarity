package schedules

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

type world struct {
	store *store.Store
	raw   *pgx.Conn
	team  uuid.UUID
	maker uuid.UUID
	other uuid.UUID
	holds map[authz.Action]bool
	runs  *queue
}

type queue struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (q *queue) Enqueue(_ context.Context, id uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ids = append(q.ids, id)
	return nil
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dsn := storetest.Migrated(t, func(ctx context.Context, dsn string) error {
		s, err := store.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer s.Close()
		_, err = s.Migrate(ctx)
		return err
	})
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	raw, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })
	w := &world{store: s, raw: raw, holds: map[authz.Action]bool{}, runs: &queue{}}
	for _, q := range []struct {
		dst *uuid.UUID
		sql string
	}{
		{&w.team, `INSERT INTO teams (name) VALUES ('ops') RETURNING id`},
		{&w.maker, `INSERT INTO users (issuer, subject) VALUES ('dev', 'maker') RETURNING id`},
		{&w.other, `INSERT INTO users (issuer, subject) VALUES ('dev', 'other') RETURNING id`},
	} {
		if err := raw.QueryRow(t.Context(), q.sql).Scan(q.dst); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if _, err := raw.Exec(t.Context(), `INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member'), ($1, $3, 'member')`,
		w.team, w.maker, w.other); err != nil {
		t.Fatal(err)
	}
	return w
}

type fakeAuthz struct{ holds map[authz.Action]bool }

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }
func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	return f.holds[a], nil
}

func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	return false, nil
}

type liveTeams struct{}

func (liveTeams) TeamDeleted(context.Context, uuid.UUID) (bool, error) { return false, nil }

var routes = []struct{ method, path string }{
	{"GET", "/teams/{id}/schedules"},
	{"POST", "/teams/{id}/schedules"},
	{"POST", "/teams/{id}/schedules/preview"},
	{"GET", "/teams/{id}/schedules/{scheduleID}"},
	{"PATCH", "/teams/{id}/schedules/{scheduleID}"},
	{"DELETE", "/teams/{id}/schedules/{scheduleID}"},
	{"POST", "/teams/{id}/schedules/{scheduleID}/confirm"},
	{"POST", "/teams/{id}/schedules/{scheduleID}/run"},
	{"POST", "/teams/{id}/schedules/{scheduleID}/take-over"},
	{"GET", "/teams/{id}/schedules/{scheduleID}/fires"},
	{"GET", "/teams/{id}/scheduled/news"},
	{"POST", "/teams/{id}/scheduled/news/seen"},
	{"GET", "/teams/{id}/schedule-targets"},
	{"GET", "/teams/{id}/schedule-settings"},
	{"PUT", "/teams/{id}/schedule-settings"},
}

// serve answers a request as user, the guard passing every route through as
// a member's: the permission each needs is rbac.json's, tested there.
func (w *world) serve(t *testing.T, user uuid.UUID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, "/teams/"+w.team.String()+path, rd)
	req.Header.Set("Content-Type", "application/json")
	rs := authz.NewRoutes()
	for _, r := range routes {
		if err := rs.Add(r.method, r.path, "member", nil); err != nil {
			t.Fatalf("route: %v", err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	az := &fakeAuthz{holds: w.holds}
	New(logger, w.store, az, triggers.NewService(w.store, w.runs, logger)).Register(mux, api.NewGuard(rs, az, liveTeams{}, logger))
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{
		ID: user, Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: w.team, Name: "ops", Role: "member"}},
	}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func want(t *testing.T, rec *httptest.ResponseRecorder, status int, says string) {
	t.Helper()
	if rec.Code != status || !strings.Contains(rec.Body.String(), says) {
		t.Fatalf("got %d %q, want %d saying %q", rec.Code, rec.Body.String(), status, says)
	}
}

func hourly(name string) map[string]any {
	return map[string]any{"name": name, "objective": "Summarise.", "when": map[string]any{"kind": "every", "every_seconds": 3600}}
}

func (w *world) make(t *testing.T, name string) schedule {
	t.Helper()
	rec := w.serve(t, w.maker, "POST", "/schedules", hourly(name))
	want(t, rec, http.StatusCreated, "")
	return decode[schedule](t, rec)
}

func TestMakingOneAndReadingItBack(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	if s.Status != "active" || s.When.Kind != "every" || s.When.EverySeconds == nil || *s.When.EverySeconds != 3600 ||
		s.Target.Kind != "own" || len(s.Upcoming) != 3 || s.Timezone != "UTC" {
		t.Errorf("made %+v", s)
	}
	got := decode[schedule](t, w.serve(t, w.maker, "GET", "/schedules/"+s.ID.String(), nil))
	if got.ID != s.ID || got.SessionID != s.SessionID {
		t.Errorf("read back %+v", got)
	}
	page := decode[api.Page[schedule]](t, w.serve(t, w.maker, "GET", "/schedules", nil))
	if len(page.Items) != 1 || page.Items[0].ID != s.ID {
		t.Errorf("listed %+v", page.Items)
	}
}

func TestARuleIsOneOfItsKinds(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct {
		when map[string]any
		says string
	}{
		{map[string]any{"kind": "every", "every_seconds": 3600, "cron": "0 9 * * *"}, "exactly one"},
		{map[string]any{"kind": "cron", "every_seconds": 3600}, "takes when.cron"},
		{map[string]any{"kind": "every", "every_seconds": 600}, "at most once every 1 hour"},
		{map[string]any{"kind": "cron", "cron": "*/5 * * * *"}, "at most once every 1 hour"},
		{map[string]any{"kind": "at", "at": "2020-01-01T00:00:00Z"}, "already passed"},
		{map[string]any{"kind": "soon", "cron": "x"}, "must be at, every or cron"},
	} {
		body := map[string]any{"name": "x", "objective": "y", "when": c.when}
		want(t, w.serve(t, w.maker, "POST", "/schedules", body), http.StatusBadRequest, c.says)
	}
	want(t, w.serve(t, w.maker, "POST", "/schedules", map[string]any{
		"name": "x", "objective": "y", "when": map[string]any{"kind": "cron", "cron": "0 9 * * *"},
		"target": map[string]any{"kind": "channel"},
	}), http.StatusForbidden, "takes channel:write")
	want(t, w.serve(t, w.maker, "POST", "/schedules", map[string]any{
		"name": "x", "objective": "y", "when": map[string]any{"kind": "cron", "cron": "0 9 * * *"},
		"target": map[string]any{"kind": "elsewhere"},
	}), http.StatusBadRequest, "own, chat, channel or direct")
	want(t, w.serve(t, w.maker, "POST", "/schedules", map[string]any{
		"name": "x", "objective": "y", "when": map[string]any{"kind": "cron", "cron": "0 9 * * *"},
		"target": map[string]any{"kind": "chat", "session_id": uuid.New()},
	}), http.StatusBadRequest, "not a chat you have talked in")
	want(t, w.serve(t, w.maker, "POST", "/schedules", `{"name":"x","objective":"y","when":{"kind":"cron","cron":"0 9 * * *"},"extra":1}`),
		http.StatusBadRequest, "invalid request body")
}

func TestPreviewSaysTheNextTimesInTheZone(t *testing.T) {
	w := newWorld(t)
	rec := w.serve(t, w.maker, "POST", "/schedules/preview", map[string]any{
		"when": map[string]any{"kind": "cron", "cron": "0 9 * * 1-5"}, "timezone": "Asia/Kolkata",
	})
	want(t, rec, http.StatusOK, "")
	p := decode[preview](t, rec)
	if p.Timezone != "Asia/Kolkata" || len(p.Upcoming) != 3 {
		t.Fatalf("preview %+v", p)
	}
	for _, at := range p.Upcoming {
		if local := at.In(time.FixedZone("IST", 5*3600+1800)); local.Hour() != 9 || local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
			t.Errorf("%v is not a weekday 09:00 in Kolkata", local)
		}
	}
}

func TestAnotherMembersIsNotFoundAndEveryonesTakesManage(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	want(t, w.serve(t, w.other, "GET", "/schedules/"+s.ID.String(), nil), http.StatusNotFound, "no such schedule")
	want(t, w.serve(t, w.other, "DELETE", "/schedules/"+s.ID.String(), nil), http.StatusNotFound, "no such schedule")
	want(t, w.serve(t, w.other, "GET", "/schedules/"+s.ID.String()+"/fires", nil), http.StatusNotFound, "no such schedule")
	want(t, w.serve(t, w.other, "GET", "/schedules?all=true", nil), http.StatusForbidden, "schedule:manage")
	if page := decode[api.Page[schedule]](t, w.serve(t, w.other, "GET", "/schedules", nil)); len(page.Items) != 0 {
		t.Errorf("another member lists %d", len(page.Items))
	}
	w.holds[manageEveryones] = true
	if page := decode[api.Page[schedule]](t, w.serve(t, w.other, "GET", "/schedules?all=true", nil)); len(page.Items) != 1 {
		t.Errorf("a manager lists %d of everyone's", len(page.Items))
	}
	want(t, w.serve(t, w.other, "GET", "/schedules?all=maybe", nil), http.StatusBadRequest, "all must be")

	// A manager sees and pauses it, but its words and its runs are its maker's.
	path := "/schedules/" + s.ID.String()
	want(t, w.serve(t, w.other, "PATCH", path, map[string]any{"objective": "Something else."}), http.StatusForbidden, "only the person who made")
	want(t, w.serve(t, w.other, "POST", path+"/run", nil), http.StatusForbidden, "only the person who made")
	want(t, w.serve(t, w.other, "PATCH", path, map[string]any{"status": "paused"}), http.StatusOK, "")
}

func TestPauseResumeAndChangeThroughPatch(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	path := "/schedules/" + s.ID.String()
	paused := decode[schedule](t, w.serve(t, w.maker, "PATCH", path, map[string]any{"status": "paused"}))
	if paused.Status != "paused" || paused.PausedReason == nil || *paused.PausedReason != "by_person" || paused.NextFireAt != nil {
		t.Errorf("paused %+v", paused)
	}
	want(t, w.serve(t, w.maker, "PATCH", path, map[string]any{"status": "done"}), http.StatusBadRequest, "active or paused")
	resumed := decode[schedule](t, w.serve(t, w.maker, "PATCH", path, map[string]any{
		"status": "active", "name": "daily summary", "when": map[string]any{"kind": "cron", "cron": "0 9 * * *"},
	}))
	if resumed.Status != "active" || resumed.Name != "daily summary" || resumed.When.Kind != "cron" || resumed.NextFireAt == nil {
		t.Errorf("resumed %+v", resumed)
	}
	want(t, w.serve(t, w.maker, "PATCH", path, map[string]any{"hub": true, "agent_id": uuid.New()}), http.StatusBadRequest, "not both")
	want(t, w.serve(t, w.maker, "PATCH", path, map[string]any{"agent_id": uuid.New()}), http.StatusBadRequest, "no such agent")
}

func TestDeleteIsSoftAndListedByAsking(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	want(t, w.serve(t, w.maker, "DELETE", "/schedules/"+s.ID.String(), nil), http.StatusNoContent, "")
	want(t, w.serve(t, w.maker, "GET", "/schedules/"+s.ID.String(), nil), http.StatusOK, `"status":"deleted"`)
	if page := decode[api.Page[schedule]](t, w.serve(t, w.maker, "GET", "/schedules", nil)); len(page.Items) != 0 {
		t.Errorf("a deleted one listed without asking: %d", len(page.Items))
	}
	if page := decode[api.Page[schedule]](t, w.serve(t, w.maker, "GET", "/schedules?status=deleted", nil)); len(page.Items) != 1 {
		t.Errorf("%d deleted listed when asked", len(page.Items))
	}
	want(t, w.serve(t, w.maker, "DELETE", "/schedules/"+s.ID.String(), nil), http.StatusNotFound, "no such schedule")
}

func TestRunNowStartsOneAndRefusesASecondWhileItWorks(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	rec := w.serve(t, w.maker, "POST", "/schedules/"+s.ID.String()+"/run", nil)
	want(t, rec, http.StatusAccepted, "run_id")
	if len(w.runs.ids) != 1 || w.runs.ids[0] != decode[ranNow](t, rec).RunID {
		t.Errorf("enqueued %v", w.runs.ids)
	}
	want(t, w.serve(t, w.maker, "POST", "/schedules/"+s.ID.String()+"/run", nil), http.StatusConflict, "still working")

	fires := decode[api.Page[fire]](t, w.serve(t, w.maker, "GET", "/schedules/"+s.ID.String()+"/fires", nil))
	if len(fires.Items) != 1 || fires.Items[0].Outcome != "started" || fires.Items[0].RunID == nil {
		t.Errorf("fires %+v", fires.Items)
	}
}

func TestFiresPageNewestFirst(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	if _, err := w.raw.Exec(t.Context(), `INSERT INTO schedule_fires (schedule_id, team_id, scheduled_for, outcome)
		SELECT $1, $2, now() - make_interval(hours => g), 'missed' FROM generate_series(1, 3) g`, s.ID, w.team); err != nil {
		t.Fatal(err)
	}
	first := decode[api.Page[fire]](t, w.serve(t, w.maker, "GET", "/schedules/"+s.ID.String()+"/fires?limit=2", nil))
	if len(first.Items) != 2 || first.NextCursor == nil || !first.Items[0].ScheduledFor.After(first.Items[1].ScheduledFor) {
		t.Fatalf("first page %+v", first)
	}
	rest := decode[api.Page[fire]](t, w.serve(t, w.maker, "GET", "/schedules/"+s.ID.String()+"/fires?limit=2&cursor="+*first.NextCursor, nil))
	if len(rest.Items) != 1 || rest.NextCursor != nil || !rest.Items[0].ScheduledFor.Before(first.Items[1].ScheduledFor) {
		t.Errorf("second page %+v", rest)
	}
}

func TestNewsAndSeen(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	if _, err := w.raw.Exec(t.Context(), `INSERT INTO schedule_fires (schedule_id, team_id, scheduled_for, outcome)
		VALUES ($1, $2, now() - interval '1 hour', 'skipped_cap')`, s.ID, w.team); err != nil {
		t.Fatal(err)
	}
	n := decode[news](t, w.serve(t, w.maker, "GET", "/scheduled/news", nil))
	if n.Count != 1 || n.More || n.Items[0].Name != "summary" || n.Items[0].Outcome != "skipped_cap" {
		t.Fatalf("news %+v", n)
	}
	want(t, w.serve(t, w.maker, "POST", "/scheduled/news/seen", map[string]any{}), http.StatusBadRequest, "schedule_id is required")
	want(t, w.serve(t, w.maker, "POST", "/scheduled/news/seen", map[string]any{"schedule_id": s.ID}), http.StatusNoContent, "")
	if n := decode[news](t, w.serve(t, w.maker, "GET", "/scheduled/news", nil)); n.Count != 0 {
		t.Errorf("news after seen %+v", n)
	}
}

func TestTakeOverTakesManage(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	want(t, w.serve(t, w.other, "POST", "/schedules/"+s.ID.String()+"/take-over", nil), http.StatusForbidden, "schedule:manage")
	w.holds[manageEveryones] = true
	want(t, w.serve(t, w.other, "POST", "/schedules/"+s.ID.String()+"/take-over", nil), http.StatusConflict, "only a schedule paused")
}

func TestConfirmingOneNotProposedIsAConflict(t *testing.T) {
	w := newWorld(t)
	s := w.make(t, "summary")
	want(t, w.serve(t, w.maker, "POST", "/schedules/"+s.ID.String()+"/confirm", nil), http.StatusConflict, "not waiting to be confirmed")
	want(t, w.serve(t, w.maker, "POST", "/schedules/not-a-uuid/confirm", nil), http.StatusBadRequest, "must be a uuid")
}

func TestSettingsReadTheDefaultsAndSaveWithinTheirRanges(t *testing.T) {
	w := newWorld(t)
	got := decode[settings](t, w.serve(t, w.maker, "GET", "/schedule-settings", nil))
	if !got.AgentsMaySchedule || got.Timezone != "UTC" || got.ScheduleMinIntervalMinutes != 60 ||
		got.SchedulesPerMember != 5 || got.SchedulesPerTeam != 20 || got.FiresPerHour != 20 {
		t.Errorf("defaults %+v", got)
	}
	good := settings{
		AgentsMaySchedule: false, Timezone: "Europe/London", ScheduleMinIntervalMinutes: 30,
		SchedulesPerMember: 10, SchedulesPerTeam: 50, FiresPerHour: 100,
	}
	for _, c := range []struct {
		change func(*settings)
		says   string
	}{
		{func(s *settings) { s.Timezone = "Mars/Base" }, "IANA"},
		{func(s *settings) { s.Timezone = "" }, "IANA"},
		{func(s *settings) { s.ScheduleMinIntervalMinutes = 1 }, "between 5 and 10080"},
		{func(s *settings) { s.SchedulesPerMember = 0 }, "between 1 and 100"},
		{func(s *settings) { s.SchedulesPerTeam = 5 }, "at least schedules_per_member"},
		{func(s *settings) { s.FiresPerHour = 5000 }, "between 1 and 1000"},
	} {
		bad := good
		c.change(&bad)
		want(t, w.serve(t, w.maker, "PUT", "/schedule-settings", bad), http.StatusBadRequest, c.says)
	}
	if saved := decode[settings](t, w.serve(t, w.maker, "PUT", "/schedule-settings", good)); saved != good {
		t.Errorf("saved %+v, want %+v", saved, good)
	}
	if again := decode[settings](t, w.serve(t, w.maker, "GET", "/schedule-settings", nil)); again != good {
		t.Errorf("read back %+v", again)
	}
	// The other settings are untouched: a team that saved only these keeps
	// its default approvals.
	var mode string
	if err := w.raw.QueryRow(t.Context(), `SELECT approval_mode FROM team_settings WHERE team_id = $1`, w.team).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "off" {
		t.Errorf("approval mode %q after saving schedule settings", mode)
	}
}

// The picker's places: a chat the caller talked in, carrying the target to
// send back, which makes a schedule answering there.
func TestThePlacesToAnswerAndPickingOne(t *testing.T) {
	w := newWorld(t)
	var channel, session uuid.UUID
	ctx := t.Context()
	if err := w.raw.QueryRow(ctx, `INSERT INTO channels (team_id, provider, name) VALUES ($1, 'slack', 'Acme Slack') RETURNING id`,
		w.team).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := w.raw.QueryRow(ctx, `INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id)
		VALUES ($1, $2, 'D0MAKERDM', 'direct', $3) RETURNING id`, w.team, channel, w.maker).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if _, err := w.raw.Exec(ctx, `INSERT INTO messages (channel_id, user_id, external_id, text, session_id)
		VALUES ($1, $2, 'm1', 'how did the release go?', $3)`, channel, w.maker, session); err != nil {
		t.Fatal(err)
	}

	page := decode[placePage](t, w.serve(t, w.maker, "GET", "/schedule-targets?q=release", nil))
	if len(page.Items) != 1 || page.Problems == nil {
		t.Fatalf("places = %+v, want the one chat and an empty problems list", page)
	}
	it := page.Items[0]
	if it.Kind != "chat" || it.Channel.Provider != "slack" || !it.Available || it.Target.SessionID == nil ||
		*it.Target.SessionID != session || it.At == nil {
		t.Errorf("place = %+v", it)
	}
	if page := decode[placePage](t, w.serve(t, w.maker, "GET", "/schedule-targets?q=nothing+here", nil)); len(page.Items) != 0 {
		t.Errorf("a search for other words found %d", len(page.Items))
	}
	want(t, w.serve(t, w.maker, "GET", "/schedule-targets?kind=elsewhere", nil), http.StatusBadRequest, "chat, channel or direct")
	if page := decode[placePage](t, w.serve(t, w.other, "GET", "/schedule-targets", nil)); len(page.Items) != 0 {
		t.Errorf("another member sees %d of the maker's chats", len(page.Items))
	}
	want(t, w.serve(t, w.maker, "GET", "/schedule-targets?q="+strings.Repeat("a", 201), nil), http.StatusBadRequest, "at most 200")
	want(t, w.serve(t, w.maker, "GET", "/schedule-targets?q=%00", nil), http.StatusBadRequest, "must be text")
	want(t, w.serve(t, w.maker, "GET", "/schedule-targets?cursor=zzz", nil), http.StatusBadRequest, "invalid cursor")
	want(t, w.serve(t, w.maker, "GET", "/schedule-targets?limit=0", nil), http.StatusBadRequest, "positive integer")
	one := decode[placePage](t, w.serve(t, w.maker, "GET", "/schedule-targets?limit=1&kind=chat", nil))
	if len(one.Items) != 1 || one.NextCursor != "" {
		t.Errorf("a page of one = %+v, want the chat and no next page", one)
	}

	// Another member's chat is not theirs to pick, and nothing is made.
	theirs := hourly("in their DM")
	theirs["target"] = it.Target
	want(t, w.serve(t, w.other, "POST", "/schedules", theirs), http.StatusBadRequest, "not a chat you have talked in")
	var made int
	if err := w.raw.QueryRow(ctx, `SELECT count(*) FROM schedules`).Scan(&made); err != nil || made != 0 {
		t.Errorf("%d schedules after a refused pick, want none", made)
	}

	body := hourly("in my DM")
	body["target"] = it.Target
	rec := w.serve(t, w.maker, "POST", "/schedules", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	s := decode[schedule](t, rec)
	if s.Target.Kind != "chat" || s.Target.Label != "Acme Slack · Your direct messages" ||
		s.Target.ChannelID == nil || *s.Target.ChannelID != channel {
		t.Errorf("target = %+v", s.Target)
	}
	if own := w.make(t, "own"); own.Target.Kind != "own" || own.Target.ChannelID != nil {
		t.Errorf("its own conversation's target = %+v", own.Target)
	}
}
