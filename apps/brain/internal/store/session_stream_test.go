package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// streamFixture is a team with an agent and a conversation on its dashboard
// channel, which is what a message needs.
type streamFixture struct {
	runFixture
	channel uuid.UUID
}

func newStreamFixture(t *testing.T, s *Store) streamFixture {
	t.Helper()
	// Named apart, so one test may hold several.
	team := mustCreate(t, s, "platform "+uuid.NewString()[:8])
	ch, err := s.DashboardChannel(t.Context(), team.ID)
	if err != nil {
		t.Fatalf("DashboardChannel: %v", err)
	}
	ref := "u:k"
	session, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: team.ID, ChannelID: ch.ID, ProviderRef: ref, Kind: "direct",
	})
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	return streamFixture{
		runFixture: runFixture{team: team.ID, session: session.ID, agent: insertAgent(t, s, team.ID, "triage")},
		channel:    ch.ID,
	}
}

func (f streamFixture) say(t *testing.T, s *Store, text string, asks bool) uuid.UUID {
	t.Helper()
	user := mustUser(t, s)
	id, err := s.InsertMessage(t.Context(), db.InsertMessageParams{
		ChannelID: f.channel, SessionID: f.session, UserID: user, ExternalID: uuid.NewString(), Text: text,
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if asks {
		if err := s.MarkMessageAsks(t.Context(), id); err != nil {
			t.Fatalf("MarkMessageAsks: %v", err)
		}
	}
	return id
}

func mustUser(t *testing.T, s *Store) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('test', $1) RETURNING id`, uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func sessionEvents(t *testing.T, s *Store, session uuid.UUID, after int64) []db.ListSessionEventsRow {
	t.Helper()
	rows, err := s.ListSessionEvents(t.Context(), db.ListSessionEventsParams{SessionID: session, After: after, PageSize: 1000})
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	return rows
}

func seqsOf(rows []db.ListSessionEventsRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = *r.Seq
	}
	return out
}

func kindsOf(rows []db.ListSessionEventsRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Kind
	}
	return out
}

func through(n int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i) + 1
	}
	return out
}

// --- the dashboard channel ---

func TestEveryTeamIsMadeWithOneDashboardChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch, err := s.DashboardChannel(t.Context(), team.ID)
	if err != nil || ch.Provider != "dashboard" || ch.NotifyStrangers {
		t.Fatalf("dashboard channel %+v, %v", ch, err)
	}
	if _, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: team.ID, Provider: "dashboard", Name: "Another"}); err == nil {
		t.Fatal("a team was given a second dashboard channel")
	}
	// The name rule is for the channels people name: theirs may be called
	// Dashboard too.
	if _, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: team.ID, Provider: "custom", Name: "dashboard"}); err != nil {
		t.Fatalf("a channel named dashboard: %v", err)
	}
}

func TestADashboardChannelLeavesNoSecretToDestroy(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	exec(t, s, "DELETE FROM teams WHERE id = '"+team.ID.String()+"'")
	if n := count(t, s, `SELECT count(*) FROM deleted_secrets WHERE path LIKE '%/channels/%' AND team_id = $1`, team.ID); n != 0 {
		t.Fatalf("%d channel secrets tombstoned for a team with only its dashboard", n)
	}
}

// --- one order ---

// A conversation's messages and every run's events share one count, from 1,
// in the order they were written.
func TestASessionsMessagesAndRunsShareOneOrder(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	asked := f.say(t, s, "why did the deploy fail?", true)
	r := createRun(t, s, f.runFixture)
	claim(t, s, r.ID, "w1")
	if _, err := appendEvent(t, s, r.ID, "w1", "started"); err != nil {
		t.Fatal(err)
	}
	f.say(t, s, "probably the migration", false)
	if _, err := appendEvent(t, s, r.ID, "w1", "step"); err != nil {
		t.Fatal(err)
	}

	rows := sessionEvents(t, s, f.session, 0)
	if got := seqsOf(rows); !slices.Equal(got, through(4)) {
		t.Fatalf("seqs %v, want 1 to 4", got)
	}
	if got := kindsOf(rows); !slices.Equal(got, []string{"message", "started", "message", "step"}) {
		t.Fatalf("kinds %v", got)
	}
	var m struct {
		ID   uuid.UUID `json:"id"`
		Asks bool      `json:"asks"`
		Text string    `json:"text"`
	}
	if err := json.Unmarshal(rows[0].Payload, &m); err != nil || m.ID != asked || !m.Asks || m.Text != "why did the deploy fail?" {
		t.Fatalf("message %+v, %v", m, err)
	}
	if rows[1].Agent == nil || *rows[1].Agent != "triage" || rows[1].RunID == nil || *rows[1].RunID != r.ID {
		t.Fatalf("run event %+v", rows[1])
	}
	if got := sessionEvents(t, s, f.session, 2); !slices.Equal(seqsOf(got), []int64{3, 4}) {
		t.Fatalf("after 2: %v", seqsOf(got))
	}
}

// Writers in one conversation, at once: every number is used once, none is
// skipped, and a reader polling as they write never sees a number before one
// below it has committed. That last is what lets a client resume after the
// highest number it saw.
func TestConcurrentWritersAreNumberedInCommitOrder(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	const runs, each = 4, 25
	ids := make([]uuid.UUID, runs)
	for i := range ids {
		ids[i] = createRun(t, s, f.runFixture).ID
		claim(t, s, ids[i], "w1")
	}

	stop := make(chan struct{})
	var seen []int64
	var gap error
	var reader sync.WaitGroup
	reader.Go(func() {
		after := int64(0)
		for {
			rows, err := s.ListSessionEvents(t.Context(), db.ListSessionEventsParams{SessionID: f.session, After: after, PageSize: 1000})
			if err != nil {
				gap = err
				return
			}
			for _, r := range rows {
				if *r.Seq != after+1 {
					gap = fmt.Errorf("read %d after %d", *r.Seq, after)
					return
				}
				after = *r.Seq
				seen = append(seen, after)
			}
			select {
			case <-stop:
				if len(rows) == 0 {
					return
				}
			default:
			}
		}
	})

	var writers sync.WaitGroup
	errs := make(chan error, runs*each+each)
	for _, id := range ids {
		writers.Go(func() {
			for range each {
				if _, err := appendEvent(t, s, id, "w1", "step"); err != nil {
					errs <- err
				}
			}
		})
	}
	writers.Go(func() {
		for range each {
			f.say(t, s, "and another thing", false)
		}
	})
	writers.Wait()
	close(stop)
	reader.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a write failed: %v", err)
	}
	if gap != nil {
		t.Fatalf("the reader saw a gap: %v", gap)
	}
	total := int64(runs*each + each)
	if !slices.Equal(seen, through(total)) {
		t.Fatalf("read %d events, want 1 to %d in order", len(seen), total)
	}
}

// --- every way a run ends ---

func finishedOf(t *testing.T, s *Store, run uuid.UUID) []string {
	t.Helper()
	rows, err := s.pool.Query(t.Context(),
		`SELECT payload->>'status' FROM run_events WHERE run_id = $1 AND kind = 'finished' AND session_seq IS NOT NULL`, run)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAQueuedRunCancelledSaysItEnded(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	r := createRun(t, s, f.runFixture)

	got, err := s.RequestRunCancel(t.Context(), r.ID)
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("cancel: %+v, %v", got.Status, err)
	}
	if st := finishedOf(t, s, r.ID); !slices.Equal(st, []string{"cancelled"}) {
		t.Fatalf("finished events %v, want one, cancelled", st)
	}
	// Asked again: nothing to cancel, and nothing written twice.
	if _, err := s.RequestRunCancel(t.Context(), r.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second cancel: %v, want no row", err)
	}
	if st := finishedOf(t, s, r.ID); len(st) != 1 {
		t.Fatalf("finished events %v after a second cancel", st)
	}
}

// A running run is only flagged: its worker ends it and says so.
func TestARunningRunCancelledIsLeftToItsWorker(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	r := createRun(t, s, f.runFixture)
	claim(t, s, r.ID, "w1")

	got, err := s.RequestRunCancel(t.Context(), r.ID)
	if err != nil || got.Status != "running" || got.CancelRequestedAt == nil {
		t.Fatalf("cancel: %+v, %v", got, err)
	}
	if st := finishedOf(t, s, r.ID); len(st) != 0 {
		t.Fatalf("finished events %v for a run still running", st)
	}
}

// A queued run left with children running ends them with it, and each says so.
func TestAQueuedRunsChildrenCancelledWithItSayTheyEnded(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	parent := createRun(t, s, f.runFixture)
	claim(t, s, parent.ID, "w1")
	child := mustStartChild(t, s, db.Run{ID: parent.ID, TeamID: f.team}, f.agent, "call_1")
	claim(t, s, child.ID, "w1")
	exec(t, s, "UPDATE runs SET status = 'queued', worker = NULL WHERE id = '"+parent.ID.String()+"'")

	if _, err := s.RequestRunCancel(t.Context(), parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{parent.ID, child.ID} {
		if st := finishedOf(t, s, id); !slices.Equal(st, []string{"cancelled"}) {
			t.Errorf("run %s: finished events %v, want cancelled", id, st)
		}
	}
}

func TestReapedChildrenSayTheyEnded(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	parent := createRun(t, s, f.runFixture)
	claim(t, s, parent.ID, "w1")
	child := mustStartChild(t, s, db.Run{ID: parent.ID, TeamID: f.team}, f.agent, "call_1")
	claim(t, s, child.ID, "w1")

	reaped, err := s.ReapChildren(t.Context(), db.ReapChildrenParams{ParentID: parent.ID, Reason: "its parent ended"})
	if err != nil || !slices.Equal(reaped, []uuid.UUID{child.ID}) {
		t.Fatalf("reaped %v, %v", reaped, err)
	}
	if st := finishedOf(t, s, child.ID); !slices.Equal(st, []string{"failed"}) {
		t.Fatalf("finished events %v, want failed", st)
	}
}

func TestAFinishedEventIsWrittenOnceAndOnlyForAnEnd(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	running := createRun(t, s, f.runFixture)
	claim(t, s, running.ID, "w1")
	ended := createRun(t, s, f.runFixture)
	exec(t, s, "UPDATE runs SET status = 'failed', error = 'it broke', finished_at = now() WHERE id = '"+ended.ID.String()+"'")

	for range 2 {
		if err := s.InTx(t.Context(), func(q *db.Queries) error {
			return q.AppendFinishedEvents(t.Context(), []uuid.UUID{running.ID, ended.ID})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if st := finishedOf(t, s, running.ID); len(st) != 0 {
		t.Errorf("a running run says it finished: %v", st)
	}
	if st := finishedOf(t, s, ended.ID); !slices.Equal(st, []string{"failed"}) {
		t.Errorf("finished events %v, want one", st)
	}
}

// --- messages taken ---

func consumedBy(t *testing.T, s *Store, run uuid.UUID) [][]uuid.UUID {
	t.Helper()
	rows, err := s.pool.Query(t.Context(),
		`SELECT payload FROM run_events WHERE run_id = $1 AND kind = 'consumed' ORDER BY seq`, run)
	if err != nil {
		t.Fatal(err)
	}
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatal(err)
	}
	var out [][]uuid.UUID
	for _, p := range payloads {
		var e struct {
			IDs []uuid.UUID `json:"message_ids"`
		}
		if err := json.Unmarshal(p, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e.IDs)
	}
	return out
}

func TestARunTakesPendingMessagesOldestFirst(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	first := f.say(t, s, "one", true)
	aside := f.say(t, s, "aside", false)
	second := f.say(t, s, "two", true)
	third := f.say(t, s, "three", true)
	r := createRun(t, s, f.runFixture)
	claim(t, s, r.ID, "w1")

	if err := s.ConsumeSteer(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.InTx(t.Context(), func(q *db.Queries) error {
		_, err := q.ConsumeMessages(t.Context(), db.ConsumeMessagesParams{RunID: r.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Nothing left: taking writes nothing.
	if err := s.ConsumeSteer(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}

	got := consumedBy(t, s, r.ID)
	want := [][]uuid.UUID{{first}, {second, third}}
	if len(got) != len(want) || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Fatalf("consumed %v, want %v; the aside %s is never taken", got, want, aside)
	}
	if n := count(t, s, `SELECT count(*) FROM messages WHERE session_id = $1 AND consumed_by IS NULL AND asks`, f.session); n != 0 {
		t.Fatalf("%d messages still pending", n)
	}
}

func TestOnlyTheGivenMessageIsTaken(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	waiting := f.say(t, s, "earlier", true)
	mine := f.say(t, s, "mine", true)
	r := createRun(t, s, f.runFixture)
	if err := s.InTx(t.Context(), func(q *db.Queries) error {
		_, err := q.ConsumeMessages(t.Context(), db.ConsumeMessagesParams{RunID: r.ID, Only: &mine})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := consumedBy(t, s, r.ID); len(got) != 1 || !slices.Equal(got[0], []uuid.UUID{mine}) {
		t.Fatalf("consumed %v, want only %s and not %s", got, mine, waiting)
	}
}

// A run finished with steers it never read hands them, and the messages
// behind them, to the follow-up that answers them.
func TestAFollowUpTakesTheMessagesItAnswers(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	r := createRun(t, s, f.runFixture)
	claim(t, s, r.ID, "w1")
	late := f.say(t, s, "one more thing", true)
	later := f.say(t, s, "and another", true)
	for _, text := range []string{"Asha: one more thing", "Ben: and another"} {
		if err := s.AddSteer(t.Context(), db.AddSteerParams{RunID: r.ID, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	out := "done"
	_, followUp, err := s.FinishRunWithEvent(t.Context(),
		db.FinishRunParams{ID: r.ID, Worker: "w1", Status: "succeeded", Output: &out},
		db.AppendRunEventParams{RunID: r.ID, Worker: "w1", Kind: "finished", Payload: []byte(`{"status":"succeeded"}`)})
	if err != nil || followUp == uuid.Nil {
		t.Fatalf("finish: follow-up %s, %v", followUp, err)
	}
	if got := consumedBy(t, s, followUp); len(got) != 1 || !slices.Equal(got[0], []uuid.UUID{late, later}) {
		t.Fatalf("the follow-up took %v, want both, %s and %s", got, late, later)
	}
}

// --- the backfill ---

// unnumber makes a session's rows look as they did before the stream: no
// number, and the counter back where it started.
func unnumber(t *testing.T, s *Store, session uuid.UUID) {
	t.Helper()
	exec(t, s, "UPDATE messages SET session_seq = NULL WHERE session_id = '"+session.String()+"'")
	exec(t, s, "UPDATE run_events SET session_seq = NULL, session_id = NULL WHERE session_id = '"+session.String()+"'")
	exec(t, s, "UPDATE sessions SET event_seq = 0 WHERE id = '"+session.String()+"'")
}

func TestTheBackfillNumbersHistoryBeforeTheStreamInTimeOrder(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	f.say(t, s, "before", false)
	r := createRun(t, s, f.runFixture)
	claim(t, s, r.ID, "w1")
	if _, err := appendEvent(t, s, r.ID, "w1", "started"); err != nil {
		t.Fatal(err)
	}
	unnumber(t, s, f.session)
	// The run carries on after the migration: its next event is numbered.
	if _, err := appendEvent(t, s, r.ID, "w1", "step"); err != nil {
		t.Fatal(err)
	}

	done, err := s.BackfillSession(t.Context(), f.session)
	if err != nil || done.Messages != 1 || done.Events != 1 {
		t.Fatalf("backfill: %+v, %v", done, err)
	}
	rows := sessionEvents(t, s, f.session, -1000)
	if got := seqsOf(rows); !slices.Equal(got, []int64{-1, 0, 1}) {
		t.Fatalf("seqs %v, want history at -1 and 0, then the stream from 1", got)
	}
	if got := kindsOf(rows); !slices.Equal(got, []string{"message", "started", "step"}) {
		t.Fatalf("kinds %v, want history first, in the order it happened", got)
	}

	again, err := s.BackfillSession(t.Context(), f.session)
	if err != nil || again.Messages != 0 || again.Events != 0 {
		t.Fatalf("second backfill numbered %+v, %v; want nothing", again, err)
	}
	if got := seqsOf(sessionEvents(t, s, f.session, -1000)); !slices.Equal(got, []int64{-1, 0, 1}) {
		t.Fatalf("seqs moved: %v", got)
	}
}

func TestTheBackfillOfASessionWithNothingToNumberChangesNothing(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	f.say(t, s, "after", false)
	done, err := s.BackfillSession(t.Context(), f.session)
	if err != nil || done.Messages != 0 || done.Events != 0 {
		t.Fatalf("backfill: %+v, %v", done, err)
	}
	if got := seqsOf(sessionEvents(t, s, f.session, -1000)); !slices.Equal(got, []int64{1}) {
		t.Fatalf("seqs %v", got)
	}
}

// --- the one lock order ---

// Everything that writes to a conversation's stream, or ends a run, at once:
// a child's worker appending, the parent finishing, a cancel, a reap. With
// the session taken first everywhere, none deadlocks and none collides.
func TestStreamWritersNeverDeadlock(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	for round := range 8 {
		f := newStreamFixture(t, s)
		parent := createRun(t, s, f.runFixture)
		claim(t, s, parent.ID, "w1")
		// StartChildRun starts the child running, under the parent's worker.
		child := mustStartChild(t, s, db.Run{ID: parent.ID, TeamID: f.team}, f.agent, fmt.Sprintf("call_%d", round))
		queued := createRun(t, s, f.runFixture)

		var wg sync.WaitGroup
		errs := make(chan error, 64)
		keep := func(err error) {
			var pg *pgconn.PgError
			if errors.As(err, &pg) {
				errs <- err
			}
		}
		wg.Go(func() {
			for range 10 {
				_, err := appendEvent(t, s, child.ID, "w1", "step")
				keep(err)
			}
		})
		wg.Go(func() {
			for range 10 {
				f.say(t, s, "meanwhile", true)
			}
		})
		wg.Go(func() {
			_, _, err := s.FinishRunWithEvent(t.Context(),
				db.FinishRunParams{ID: parent.ID, Worker: "w1", Status: "succeeded"},
				db.AppendRunEventParams{RunID: parent.ID, Worker: "w1", Kind: "finished", Payload: []byte(`{"status":"succeeded"}`)})
			keep(err)
			_, err = s.ReapChildren(t.Context(), db.ReapChildrenParams{ParentID: parent.ID, Reason: "its parent ended"})
			keep(err)
		})
		wg.Go(func() {
			_, err := s.RequestRunCancel(t.Context(), queued.ID)
			keep(err)
			_, err = s.RequestRunCancel(t.Context(), child.ID)
			keep(err)
		})
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}
		rows := sessionEvents(t, s, f.session, 0)
		if got := seqsOf(rows); !slices.Equal(got, through(int64(len(rows)))) {
			t.Fatalf("round %d: seqs %v, want 1 to %d", round, got, len(rows))
		}
	}
}

// A dashboard conversation's answer is its stream: its run posts nothing,
// while a run on a channel that replies still does.
func TestADashboardRunQueuesNoReply(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	exec(t, s, "INSERT INTO team_settings (team_id) VALUES ('"+f.team.String()+"') ON CONFLICT DO NOTHING")
	dash, err := s.CreateChannelRun(t.Context(), db.CreateChannelRunParams{
		ID: uuid.New(), TeamID: f.team, SessionID: f.session, Input: "hi",
	})
	if err != nil || dash.ReplyToChannel {
		t.Fatalf("a dashboard run: replies %v, %v", dash.ReplyToChannel, err)
	}

	custom, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: f.team, Provider: "custom", Name: "support"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{TeamID: f.team, ChannelID: custom.ID, ProviderRef: "c-1", Kind: "group"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateChannelRun(t.Context(), db.CreateChannelRunParams{
		ID: uuid.New(), TeamID: f.team, SessionID: session.ID, Input: "hi",
	})
	if err != nil || !other.ReplyToChannel {
		t.Fatalf("a channel's run: replies %v, %v", other.ReplyToChannel, err)
	}
}

// The one lock order, asserted rather than raced: while another transaction
// holds the session, a delegated run's end waits for it before it takes its
// own row. Were the run taken first, a cancel holding the session and
// wanting the run would deadlock with it.
func TestARunsEndTakesTheSessionBeforeTheRun(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newStreamFixture(t, s)
	parent := createRun(t, s, f.runFixture)
	claim(t, s, parent.ID, "w1")
	child := mustStartChild(t, s, db.Run{ID: parent.ID, TeamID: f.team}, f.agent, "call_1")

	holder, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(t.Context()) }()
	if _, err := holder.Exec(t.Context(), `SELECT id FROM sessions WHERE id = $1 FOR UPDATE`, f.session); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := s.FinishRunWithEvent(t.Context(),
			db.FinishRunParams{ID: child.ID, Worker: "w1", Status: "succeeded"},
			db.AppendRunEventParams{RunID: child.ID, Worker: "w1", Kind: "finished", Payload: []byte(`{"status":"succeeded"}`)})
		done <- err
	}()
	// The finish is waiting on a lock once Postgres says something is.
	for count(t, s, `SELECT count(*) FROM pg_locks WHERE NOT granted`) == 0 {
		select {
		case err := <-done:
			t.Fatalf("the finish did not wait for the session: %v", err)
		default:
		}
	}

	probe, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, lockErr := probe.Exec(t.Context(), `SELECT id FROM runs WHERE id = $1 FOR UPDATE NOWAIT`, child.ID)
	_ = probe.Rollback(t.Context())
	if lockErr != nil {
		t.Fatalf("the finish took the run while waiting for the session: %v", lockErr)
	}

	if err := holder.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// The stream's migration rolls back with conversations in it: the dashboard's
// go with their channel, a channel's own stay as they were, and applied again
// every team has its dashboard once more.
func TestTheSessionStreamMigrationRollsBackWithDataInIt(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	f := newStreamFixture(t, s)
	f.say(t, s, "on the dashboard", true)
	r := createRun(t, s, f.runFixture)
	claim(t, s, r.ID, "w1")
	if _, err := appendEvent(t, s, r.ID, "w1", "started"); err != nil {
		t.Fatal(err)
	}
	custom, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: f.team, Provider: "custom", Name: "support"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{TeamID: f.team, ChannelID: custom.ID, ProviderRef: "c-1", Kind: "group"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMessage(t.Context(), db.InsertMessageParams{
		ChannelID: custom.ID, SessionID: other.ID, UserID: mustUser(t, s), ExternalID: "m-1", Text: "kept",
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM channels WHERE provider = 'dashboard'`); n != 0 {
		t.Errorf("%d dashboard channels after the rollback", n)
	}
	if n := count(t, s, `SELECT count(*) FROM messages WHERE session_id = $1`, other.ID); n != 1 {
		t.Errorf("the channel's own conversation lost its message: %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM runs WHERE id = $1`, r.ID); n != 0 {
		t.Errorf("the dashboard conversation's run survived its channel")
	}
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'messages' AND column_name IN ('session_seq', 'asks', 'consumed_by')`); n != 0 {
		t.Errorf("%d of the stream's columns survived the rollback", n)
	}

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
	if _, err := s.DashboardChannel(t.Context(), f.team); err != nil {
		t.Fatalf("the team has no dashboard after the migration again: %v", err)
	}
}
