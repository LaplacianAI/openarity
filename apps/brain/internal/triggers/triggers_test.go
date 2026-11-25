package triggers

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/when"
)

// --- a database of the test's own ---

type world struct {
	dsn     string
	store   *store.Store
	raw     *pgx.Conn
	team    uuid.UUID
	maker   uuid.UUID
	other   uuid.UUID
	clock   time.Time
	runs    *recorded
	service *Service
	firer   *Firer
}

// recorded is the run queue: what was handed to it.
type recorded struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (r *recorded) Enqueue(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

func (r *recorded) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
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

	w := &world{dsn: dsn, store: s, raw: raw, runs: &recorded{}}
	w.scan(t, &w.team, `INSERT INTO teams (name) VALUES ('ops') RETURNING id`)
	w.scan(t, &w.maker, `INSERT INTO users (issuer, subject) VALUES ('dev', 'maker') RETURNING id`)
	w.scan(t, &w.other, `INSERT INTO users (issuer, subject) VALUES ('dev', 'other') RETURNING id`)
	w.exec(t, `INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member'), ($1, $3, 'member')`,
		w.team, w.maker, w.other)
	w.clock = time.Date(2026, 12, 1, 8, 30, 0, 0, time.UTC)
	w.service = NewService(s, w.runs)
	w.service.now = func() time.Time { return w.clock }
	w.firer = NewFirer(s, w.runs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.firer.now = func() time.Time { return w.clock }
	return w
}

func (w *world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.raw.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (w *world) scan(t *testing.T, dst any, sql string, args ...any) {
	t.Helper()
	if err := w.raw.QueryRow(t.Context(), sql, args...).Scan(dst); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (w *world) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	w.scan(t, &n, sql, args...)
	return n
}

func (w *world) who() Who { return Who{Team: w.team, User: w.maker} }

func hourly(name string) ScheduleInput {
	return ScheduleInput{Name: name, Objective: "Summarise what changed.", When: When{Kind: when.Every, Every: time.Hour}}
}

func (w *world) make(t *testing.T, in ScheduleInput) ScheduleView {
	t.Helper()
	v, err := w.service.CreateSchedule(t.Context(), w.who(), Origin{}, in)
	if err != nil {
		t.Fatalf("CreateSchedule(%s): %v", in.Name, err)
	}
	return v
}

// due puts a schedule's next fire at the given time.
func (w *world) due(t *testing.T, id uuid.UUID, at time.Time) {
	t.Helper()
	w.exec(t, `UPDATE schedules SET next_fire_at = $2 WHERE id = $1`, id, at)
}

func (w *world) fires(t *testing.T, id uuid.UUID) map[string]int {
	t.Helper()
	rows, err := w.raw.Query(t.Context(), `SELECT outcome, count(*) FROM schedule_fires WHERE schedule_id = $1 GROUP BY outcome`, id)
	if err != nil {
		t.Fatalf("fires: %v", err)
	}
	out := map[string]int{}
	for rows.Next() {
		var o string
		var n int
		if err := rows.Scan(&o, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[o] = n
	}
	return out
}

// finish ends a run as a worker would, through the store.
func (w *world) finish(t *testing.T, run uuid.UUID, status, output string) {
	t.Helper()
	w.exec(t, `UPDATE runs SET status = 'running', worker = 'w', heartbeat_at = now() WHERE id = $1`, run)
	var out, failure *string
	if output != "" {
		out = &output
	}
	if status == "failed" {
		failure = ptr("the model was unreachable")
	}
	if _, _, err := w.store.FinishRunWithEvent(t.Context(),
		db.FinishRunParams{Status: status, Output: out, Error: failure, ID: run, Worker: "w"},
		db.AppendRunEventParams{RunID: run, Kind: "finished", Payload: []byte(`{}`), Worker: "w"}); err != nil {
		t.Fatalf("FinishRunWithEvent: %v", err)
	}
}

func (w *world) onlyRun(t *testing.T, schedule uuid.UUID) uuid.UUID {
	t.Helper()
	var run uuid.UUID
	w.scan(t, &run, `SELECT run_id FROM schedule_fires WHERE schedule_id = $1 AND run_id IS NOT NULL ORDER BY fired_at DESC LIMIT 1`, schedule)
	return run
}

func kindIs(t *testing.T, err error, want Kind, says string) {
	t.Helper()
	if KindOf(err) != want || !strings.Contains(err.Error(), says) {
		t.Fatalf("err = %v (kind %d), want kind %d saying %q", err, KindOf(err), want, says)
	}
}

// --- making one ---

func TestASchedulesConversationIsItsOwnOnTheDashboardWithItsMaker(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("Repo summary"))

	if v.Status != Active || v.NextFireAt == nil || !v.NextFireAt.Equal(time.Date(2026, 12, 1, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("status %s next %v, want active at 09:30, one interval on", v.Status, v.NextFireAt)
	}
	if len(v.Upcoming) != 3 || v.Timezone != "UTC" {
		t.Errorf("upcoming %v zone %s, want three times in the team's zone", v.Upcoming, v.Timezone)
	}
	var provider, kind, ref string
	var user *uuid.UUID
	w.scan(t, &provider, `SELECT ch.provider FROM sessions s JOIN channels ch ON ch.id = s.channel_id WHERE s.id = $1`, v.SessionID)
	w.scan(t, &kind, `SELECT kind FROM sessions WHERE id = $1`, v.SessionID)
	w.scan(t, &ref, `SELECT provider_ref FROM sessions WHERE id = $1`, v.SessionID)
	w.scan(t, &user, `SELECT user_id FROM sessions WHERE id = $1`, v.SessionID)
	if provider != "dashboard" || kind != "direct" || ref != "trigger:"+v.ID.String() || user == nil || *user != w.maker {
		t.Errorf("session on %s, %s, %q, with %v; want a direct dashboard conversation with the maker", provider, kind, ref, user)
	}
}

func TestTheTeamsZoneIsTheDefaultAndARuleIsHeldToItsInterval(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `INSERT INTO team_settings (team_id, timezone, schedule_min_interval_minutes) VALUES ($1, 'Asia/Kolkata', 120)`, w.team)

	_, err := w.service.CreateSchedule(t.Context(), w.who(), Origin{}, hourly("too often"))
	kindIs(t, err, Invalid, "at most once every 2 hours")

	v := w.make(t, ScheduleInput{Name: "weekdays", Objective: "x", When: When{Kind: when.Cron, Cron: "0 9 * * 1-5"}})
	if v.Timezone != "Asia/Kolkata" || v.NextFireAt.UTC().Hour() != 3 || v.NextFireAt.UTC().Minute() != 30 {
		t.Errorf("zone %s next %v, want 09:00 Kolkata, 03:30 UTC", v.Timezone, v.NextFireAt)
	}
}

func TestTheTeamsLimitsHoldPerPersonAndPerTeam(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `INSERT INTO team_settings (team_id, schedules_per_member, schedules_per_team) VALUES ($1, 2, 3)`, w.team)
	w.make(t, hourly("one"))
	w.make(t, hourly("two"))
	_, err := w.service.CreateSchedule(t.Context(), w.who(), Origin{}, hourly("three"))
	kindIs(t, err, Limit, "limit per person")

	other := Who{Team: w.team, User: w.other}
	if _, err := w.service.CreateSchedule(t.Context(), other, Origin{}, hourly("theirs")); err != nil {
		t.Fatalf("the other member's first: %v", err)
	}
	_, err = w.service.CreateSchedule(t.Context(), other, Origin{}, hourly("theirs too"))
	kindIs(t, err, Limit, "which is its limit")

	// A deleted one no longer counts.
	if err := w.service.Delete(t.Context(), w.who(), Ref{Name: "one"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := w.service.CreateSchedule(t.Context(), other, Origin{}, hourly("theirs too")); err != nil {
		t.Errorf("after a delete: %v", err)
	}
}

func TestANameIsAPersonsOnceAndADeleteFreesIt(t *testing.T) {
	w := newWorld(t)
	w.make(t, hourly("Repo summary"))
	_, err := w.service.CreateSchedule(t.Context(), w.who(), Origin{}, hourly("repo SUMMARY"))
	kindIs(t, err, Conflict, "already have a schedule named")

	if _, err := w.service.CreateSchedule(t.Context(), Who{Team: w.team, User: w.other}, Origin{}, hourly("Repo summary")); err != nil {
		t.Errorf("another person's own of the same name: %v", err)
	}
	if err := w.service.Delete(t.Context(), w.who(), Ref{Name: "repo summary"}); err != nil {
		t.Fatalf("Delete by name: %v", err)
	}
	w.make(t, hourly("Repo summary"))
}

func TestAnotherMembersScheduleIsNotFoundButAManagerReachesIt(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("mine"))
	_, err := w.service.Pause(t.Context(), Who{Team: w.team, User: w.other}, Ref{ID: v.ID})
	kindIs(t, err, NotFound, "no such schedule")
	if _, err := w.service.Pause(t.Context(), Who{Team: w.team, User: w.other, Manage: true}, Ref{ID: v.ID}); err != nil {
		t.Errorf("a manager pausing it: %v", err)
	}
	_, err = w.service.ListSchedules(t.Context(), Who{Team: w.team, User: w.other}, true, nil, nil, nil, 10)
	kindIs(t, err, Forbidden, "schedule:manage")
}

// --- firing ---

func TestAFireStartsOneRunInTheSchedulesConversationAsItsMaker(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.clock = *v.NextFireAt

	fired, err := w.firer.FireDue(t.Context(), 100, time.Minute)
	if err != nil || fired != 1 {
		t.Fatalf("FireDue = %d, %v; want 1", fired, err)
	}
	run := w.onlyRun(t, v.ID)
	var session, user uuid.UUID
	var hub, reply bool
	var input string
	w.scan(t, &session, `SELECT session_id FROM runs WHERE id = $1`, run)
	w.scan(t, &user, `SELECT user_id FROM runs WHERE id = $1`, run)
	w.scan(t, &hub, `SELECT hub FROM runs WHERE id = $1`, run)
	w.scan(t, &reply, `SELECT reply_to_channel FROM runs WHERE id = $1`, run)
	w.scan(t, &input, `SELECT input FROM runs WHERE id = $1`, run)
	if session != v.SessionID || user != w.maker || !hub || reply {
		t.Errorf("run in %s as %s hub=%v reply=%v; want the schedule's conversation, as its maker, by the hub, answered in its stream", session, user, hub, reply)
	}
	if !strings.Contains(input, `scheduled run of "summary"`) || !strings.Contains(input, "Summarise what changed.") ||
		!strings.Contains(input, "[silent]") {
		t.Errorf("input = %q", input)
	}
	if w.runs.count() != 1 {
		t.Errorf("%d runs enqueued, want 1", w.runs.count())
	}
	var next time.Time
	w.scan(t, &next, `SELECT next_fire_at FROM schedules WHERE id = $1`, v.ID)
	if want := v.NextFireAt.Add(time.Hour); !next.Equal(want) {
		t.Errorf("next fire %v, want %v", next, want)
	}
}

// Two workers on one due schedule fire it once: SKIP LOCKED keeps the second
// off the row, and the fire's key is the last guard.
func TestTwoWorkersFireATimeOnce(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.clock = *v.NextFireAt

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
				t.Errorf("FireDue: %v", err)
			}
		})
	}
	wg.Wait()
	if n := w.count(t, `SELECT count(*) FROM runs WHERE session_id = $1`, v.SessionID); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}
	if n := w.count(t, `SELECT count(*) FROM schedule_fires WHERE schedule_id = $1`, v.ID); n != 1 {
		t.Errorf("%d fires, want 1", n)
	}
}

// The fire's key alone: a time already recorded starts no second run even
// when the schedule is due at it again.
func TestATimeAlreadyFiredStartsNothing(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	at := *v.NextFireAt
	w.clock = at
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	w.finish(t, w.onlyRun(t, v.ID), "succeeded", "done")
	w.due(t, v.ID, at)
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	if n := w.count(t, `SELECT count(*) FROM runs WHERE session_id = $1`, v.SessionID); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}
}

func TestMissedTimesCollapseIntoOneCatchUp(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.clock = v.NextFireAt.Add(3*time.Hour + 10*time.Minute)

	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := w.fires(t, v.ID); got["catch_up"] != 1 || got["missed"] != 3 || len(got) != 2 {
		t.Errorf("fires = %v, want one catch_up and three missed", got)
	}
	var input string
	w.scan(t, &input, `SELECT input FROM runs WHERE session_id = $1`, v.SessionID)
	if !strings.Contains(input, "running late") {
		t.Errorf("a catch-up's input does not say it is late: %q", input)
	}
}

func TestAFireWhileTheLastRunWorksIsSkipped(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.clock = *v.NextFireAt
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Still waiting on an approval, an hour on.
	w.exec(t, `UPDATE runs SET status = 'waiting', waiting_since = now() WHERE session_id = $1`, v.SessionID)
	w.clock = w.clock.Add(time.Hour)
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := w.fires(t, v.ID); got["skipped_busy"] != 1 {
		t.Errorf("fires = %v, want one skipped_busy", got)
	}
	if n := w.count(t, `SELECT count(*) FROM runs WHERE session_id = $1`, v.SessionID); n != 1 {
		t.Errorf("%d runs, want the one still waiting", n)
	}
}

func TestTheHourlyCapSkipsAndRecords(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `INSERT INTO team_settings (team_id, fires_per_hour) VALUES ($1, 1)`, w.team)
	a := w.make(t, hourly("a"))
	b := w.make(t, hourly("b"))
	w.clock = *a.NextFireAt
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	got := w.fires(t, a.ID)
	for k, n := range w.fires(t, b.ID) {
		got[k] += n
	}
	if got["started"] != 1 || got["skipped_cap"] != 1 {
		t.Errorf("fires = %v, want one started and one skipped_cap", got)
	}
}

func TestAMakerWhoMayNoLongerRunPausesTheSchedule(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.exec(t, `DELETE FROM team_members WHERE user_id = $1`, w.maker)
	w.clock = *v.NextFireAt
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := w.service.Schedule(t.Context(), Who{Team: w.team, Manage: true}, Ref{ID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Paused || got.PausedReason == nil || *got.PausedReason != MakerCannotRun || got.NextFireAt != nil {
		t.Errorf("status %s reason %v, want paused because the maker may not run", got.Status, got.PausedReason)
	}
	if f := w.fires(t, v.ID); f["skipped_maker"] != 1 {
		t.Errorf("fires = %v", f)
	}

	// An admin takes it over and resumes it as themselves.
	admin := Who{Team: w.team, User: w.other, Manage: true}
	if _, err := w.service.TakeOver(t.Context(), admin, Ref{ID: v.ID}); err != nil {
		t.Fatalf("TakeOver: %v", err)
	}
	if r, err := w.service.Resume(t.Context(), admin, Ref{ID: v.ID}); err != nil || r.Status != Active {
		t.Fatalf("Resume = %v, %v", r.Status, err)
	}
	var user uuid.UUID
	w.scan(t, &user, `SELECT user_id FROM sessions WHERE id = $1`, v.SessionID)
	if user != w.other {
		t.Errorf("the conversation is with %s, want the admin who took it over", user)
	}
}

func TestAOneShotRunsLateWithinADayAndIsDone(t *testing.T) {
	w := newWorld(t)
	at := w.clock.Add(2 * time.Hour)
	v := w.make(t, ScheduleInput{Name: "remind", Objective: "Remind me.", When: When{Kind: when.At, At: at}})
	w.clock = at.Add(5 * time.Hour)
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ := w.service.Schedule(t.Context(), w.who(), Ref{ID: v.ID})
	if got.Status != Done || w.fires(t, v.ID)["late"] != 1 {
		t.Errorf("status %s fires %v, want done after one late run", got.Status, w.fires(t, v.ID))
	}
}

// --- finishing ---

func TestASilentAnswerIsRecordedAndNobodyIsTold(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.clock = *v.NextFireAt
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	w.finish(t, w.onlyRun(t, v.ID), "succeeded", "  [silent]\n")
	if f := w.fires(t, v.ID); f["silent"] != 1 {
		t.Errorf("fires = %v, want silent", f)
	}
	news, err := w.service.News(t.Context(), w.who(), 10)
	if err != nil || len(news) != 0 {
		t.Errorf("news = %v, %v; a silent run tells nobody", news, err)
	}
}

func TestAnAnswerInItsOwnConversationIsNewsUntilSeen(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.clock = *v.NextFireAt
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	w.finish(t, w.onlyRun(t, v.ID), "succeeded", "Three PRs merged.")
	news, err := w.service.News(t.Context(), w.who(), 10)
	if err != nil || len(news) != 1 || news[0].Outcome != "answered" || news[0].SessionID != v.SessionID {
		t.Fatalf("news = %+v, %v", news, err)
	}
	if other, _ := w.service.News(t.Context(), Who{Team: w.team, User: w.other}, 10); len(other) != 0 {
		t.Errorf("another member sees %d news of a schedule not theirs", len(other))
	}
	if err := w.service.Seen(t.Context(), w.who(), v.ID); err != nil {
		t.Fatal(err)
	}
	if news, _ := w.service.News(t.Context(), w.who(), 10); len(news) != 0 {
		t.Errorf("after seen: %d news", len(news))
	}
}

func TestFiveFailuresInARowPauseTheScheduleAndASuccessClearsThem(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	fire := func(status string) {
		w.clock = w.clock.Add(time.Hour)
		w.due(t, v.ID, w.clock)
		if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
			t.Fatal(err)
		}
		w.finish(t, w.onlyRun(t, v.ID), status, "")
	}
	for range 4 {
		fire("failed")
	}
	fire("succeeded")
	var failures int
	w.scan(t, &failures, `SELECT consecutive_failures FROM schedules WHERE id = $1`, v.ID)
	if failures != 0 {
		t.Errorf("%d failures after a success, want 0", failures)
	}
	for range 5 {
		fire("failed")
	}
	got, _ := w.service.Schedule(t.Context(), w.who(), Ref{ID: v.ID})
	if got.Status != Paused || got.PausedReason == nil || *got.PausedReason != Failing {
		t.Errorf("status %s reason %v, want paused as failing", got.Status, got.PausedReason)
	}
	news, _ := w.service.News(t.Context(), w.who(), 50)
	if len(news) < 5 {
		t.Errorf("%d news, want every failure", len(news))
	}
}

// --- changing one ---

func TestDeleteKeepsTheConversationAndItsRuns(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.clock = *v.NextFireAt
	if _, err := w.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := w.service.Delete(t.Context(), w.who(), Ref{ID: v.ID}); err != nil {
		t.Fatal(err)
	}
	var status string
	w.scan(t, &status, `SELECT status FROM sessions WHERE id = $1`, v.SessionID)
	if status != "closed" {
		t.Errorf("session %s, want closed", status)
	}
	if n := w.count(t, `SELECT count(*) FROM runs WHERE session_id = $1`, v.SessionID); n != 1 {
		t.Errorf("%d runs kept, want 1", n)
	}
	w.clock = w.clock.Add(2 * time.Hour)
	if fired, _ := w.firer.FireDue(t.Context(), 100, time.Minute); fired != 0 {
		t.Errorf("a deleted schedule fired")
	}
	_, err := w.service.Schedule(t.Context(), w.who(), Ref{Name: "summary"})
	kindIs(t, err, NotFound, "no schedule named")
	deleted, _ := w.service.ListSchedules(t.Context(), w.who(), false, []string{Deleted}, nil, nil, 10)
	if len(deleted) != 1 {
		t.Errorf("%d deleted listed, want 1", len(deleted))
	}
}

func TestPauseAndResumeFromNow(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	if _, err := w.service.Pause(t.Context(), w.who(), Ref{ID: v.ID}); err != nil {
		t.Fatal(err)
	}
	_, err := w.service.Pause(t.Context(), w.who(), Ref{ID: v.ID})
	kindIs(t, err, Conflict, "only an active schedule")
	w.clock = w.clock.Add(5 * time.Hour)
	r, err := w.service.Resume(t.Context(), w.who(), Ref{ID: v.ID})
	if err != nil || r.NextFireAt == nil || !r.NextFireAt.After(w.clock) {
		t.Fatalf("Resume = %+v, %v; want its next fire after now", r.NextFireAt, err)
	}
	if fired, _ := w.firer.FireDue(t.Context(), 100, time.Minute); fired != 0 {
		t.Errorf("a resumed schedule caught up what it missed while paused")
	}
}

func TestUpdateMovesTheNextFireAndKeepsTheConversation(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	cron := When{Kind: when.Cron, Cron: "0 9 * * *"}
	zone := "Europe/London"
	u, err := w.service.UpdateSchedule(t.Context(), w.who(), Ref{ID: v.ID}, SchedulePatch{When: &cron, Timezone: &zone})
	if err != nil {
		t.Fatal(err)
	}
	if u.SessionID != v.SessionID || u.Kind != "cron" || !u.NextFireAt.Equal(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("updated = %s %v in %s", u.Kind, u.NextFireAt, u.SessionID)
	}
	empty := ""
	_, err = w.service.UpdateSchedule(t.Context(), w.who(), Ref{ID: v.ID}, SchedulePatch{Name: &empty})
	kindIs(t, err, Invalid, "needs a name")
}

func TestRunNowRefusesWhatAFireWouldSkip(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	run, err := w.service.RunNow(t.Context(), w.who(), Ref{ID: v.ID})
	if err != nil || run == uuid.Nil {
		t.Fatalf("RunNow = %s, %v", run, err)
	}
	_, err = w.service.RunNow(t.Context(), w.who(), Ref{ID: v.ID})
	kindIs(t, err, Conflict, "still working")
	if f := w.fires(t, v.ID); f["started"] != 1 || len(f) != 1 {
		t.Errorf("fires = %v: a refused run-now records nothing", f)
	}
}

// --- an agent's proposal ---

// proposal is a schedule proposed in a chat by run, and the conversation it
// was proposed in, with a message from the maker before it.
func (w *world) proposal(t *testing.T) (ScheduleView, Origin) {
	t.Helper()
	// A proposal lapses by the database's clock, so the service reads the same one.
	w.clock = time.Now().Truncate(time.Second)
	var chat, ch, msg, run uuid.UUID
	w.scan(t, &ch, `SELECT id FROM channels WHERE team_id = $1 AND provider = 'dashboard'`, w.team)
	w.scan(t, &chat, `INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id) VALUES ($1, $2, 'chat', 'direct', $3) RETURNING id`, w.team, ch, w.maker)
	w.scan(t, &msg, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, received_at) VALUES ($1, $2, $4, 'm1', 'remind me', $3) RETURNING id`, ch, chat, w.clock.Add(-time.Minute), w.maker)
	run = w.chatRun(t, chat, msg)
	origin := Origin{Run: run, Session: chat}
	v, err := w.service.CreateSchedule(t.Context(), w.who(), origin, hourly("summary"))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	return v, origin
}

func (w *world) chatRun(t *testing.T, session, message uuid.UUID) uuid.UUID {
	t.Helper()
	run, err := w.store.CreateChannelRun(t.Context(), db.CreateChannelRunParams{
		ID: uuid.New(), TeamID: w.team, SessionID: session, UserID: &w.maker, Input: "x", MessageID: &message,
	})
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	return run.ID
}

func TestAProposalWaitsAndOnlyALaterMessageConfirmsIt(t *testing.T) {
	w := newWorld(t)
	v, origin := w.proposal(t)
	if v.Status != Proposed || v.NextFireAt != nil {
		t.Fatalf("status %s next %v, want proposed with nothing due", v.Status, v.NextFireAt)
	}

	_, err := w.service.Confirm(t.Context(), w.who(), origin, Ref{ID: v.ID})
	kindIs(t, err, Conflict, "not in the run that proposed it")

	// A message sent before the proposal cannot confirm it.
	var ch, early uuid.UUID
	w.scan(t, &ch, `SELECT channel_id FROM sessions WHERE id = $1`, origin.Session)
	w.scan(t, &early, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, received_at) VALUES ($1, $2, $4, 'm0', 'yes', $3) RETURNING id`, ch, origin.Session, w.clock.Add(-time.Hour), w.maker)
	w.exec(t, `UPDATE runs SET status = 'succeeded', finished_at = now() WHERE session_id = $1`, origin.Session)
	_, err = w.service.Confirm(t.Context(), w.who(), Origin{Run: w.chatRun(t, origin.Session, early), Session: origin.Session}, Ref{ID: v.ID})
	kindIs(t, err, Forbidden, "sent after the proposal")

	// Nor someone else's run.
	_, err = w.service.Confirm(t.Context(), Who{Team: w.team, User: w.other}, origin, Ref{ID: v.ID})
	kindIs(t, err, NotFound, "no such schedule")

	var yes uuid.UUID
	w.scan(t, &yes, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, received_at) VALUES ($1, $2, $3, 'm2', 'yes', now() + interval '1 day') RETURNING id`, ch, origin.Session, w.maker)
	w.exec(t, `UPDATE runs SET status = 'succeeded', finished_at = now() WHERE session_id = $1`, origin.Session)
	got, err := w.service.Confirm(t.Context(), w.who(), Origin{Run: w.chatRun(t, origin.Session, yes), Session: origin.Session}, Ref{ID: v.ID})
	if err != nil || got.Status != Active || got.NextFireAt == nil {
		t.Fatalf("Confirm = %s %v, %v", got.Status, got.NextFireAt, err)
	}
}

func TestAnUnconfirmedProposalLapsesAfterAnHour(t *testing.T) {
	w := newWorld(t)
	v, _ := w.proposal(t)
	w.exec(t, `UPDATE schedules SET created_at = now() - interval '2 hours' WHERE id = $1`, v.ID)
	j := NewJob(w.store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	j.now = time.Now
	if err := j.sweepOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, session string
	w.scan(t, &status, `SELECT status FROM schedules WHERE id = $1`, v.ID)
	w.scan(t, &session, `SELECT status FROM sessions WHERE id = $1`, v.SessionID)
	if status != Deleted || session != "closed" {
		t.Errorf("schedule %s session %s, want deleted and closed", status, session)
	}
}

func TestEachScheduleKeepsItsNewestFires(t *testing.T) {
	w := newWorld(t)
	v := w.make(t, hourly("summary"))
	w.exec(t, `INSERT INTO schedule_fires (schedule_id, team_id, scheduled_for, outcome)
		SELECT $1, $2, now() - make_interval(hours => g), 'missed' FROM generate_series(1, 230) g`, v.ID, w.team)
	j := NewJob(w.store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := j.sweepOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(t, `SELECT count(*) FROM schedule_fires WHERE schedule_id = $1`, v.ID); n != keepFires {
		t.Errorf("%d fires kept, want %d", n, keepFires)
	}
	var oldest time.Time
	w.scan(t, &oldest, `SELECT min(scheduled_for) FROM schedule_fires WHERE schedule_id = $1`, v.ID)
	if time.Since(oldest) > 201*time.Hour {
		t.Errorf("the oldest kept is %v ago: the newest must be kept", time.Since(oldest))
	}
}

// A schedule's conversation is the schedule's: a team's deletion takes both.
func TestPurgingATeamTakesItsSchedules(t *testing.T) {
	w := newWorld(t)
	w.make(t, hourly("summary"))
	w.exec(t, `DELETE FROM teams WHERE id = $1`, w.team)
	if n := w.count(t, `SELECT count(*) FROM schedules`); n != 0 {
		t.Errorf("%d schedules left", n)
	}
}
