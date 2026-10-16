package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const wallClock = 30 * 60 // seconds, the slice 1 limit

func createRun(t *testing.T, s *Store, f runFixture) db.Run {
	t.Helper()

	r, err := s.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), SessionID: f.session, Input: "triage the bug reports",
		AgentID: f.agent, TeamID: f.team,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return r
}

func claim(t *testing.T, s *Store, id uuid.UUID, worker string) db.ClaimRunRow {
	t.Helper()

	row, err := s.ClaimRun(t.Context(), db.ClaimRunParams{ID: id, Worker: worker, WallClockSeconds: wallClock})
	if err != nil {
		t.Fatalf("ClaimRun(%s): %v", worker, err)
	}
	return row
}

// age moves a run's clock columns into the past on the database's own clock,
// so no assertion here ever compares a Go time with a Postgres one.
func age(t *testing.T, s *Store, id uuid.UUID, column string, seconds int) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(),
		`UPDATE runs SET `+column+` = now() - make_interval(secs => $2) WHERE id = $1`,
		id, seconds); err != nil {
		t.Fatalf("age %s: %v", column, err)
	}
}

func wantNoRow(t *testing.T, err error, what string) {
	t.Helper()

	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("%s: err = %v, want no row", what, err)
	}
}

func statusOf(t *testing.T, s *Store, id uuid.UUID) db.Run {
	t.Helper()

	r, err := s.GetRun(t.Context(), id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return r
}

// --- starting one ---

func TestCreateRunIsItsOwnRootUnderItsAgentsName(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")

	r := createRun(t, s, f)

	if r.RootRunID != r.ID || r.ParentRunID != nil || r.Depth != 0 {
		t.Errorf("root = %s, parent = %v, depth = %d; want its own root at depth 0",
			r.RootRunID, r.ParentRunID, r.Depth)
	}
	if r.AgentName != "triage" || r.Status != "queued" {
		t.Errorf("agent_name = %q, status = %q; want triage, queued", r.AgentName, r.Status)
	}
}

// The SELECT is the team check: an agent of another team inserts nothing,
// before the key on (agent_id, team_id) would have refused it anyway.
func TestCreateRunRefusesAnAgentFromAnotherTeam(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")

	_, err := s.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), SessionID: f.session, Input: "x", AgentID: other.agent, TeamID: f.team,
	})
	wantNoRow(t, err, "CreateRun with another team's agent")
	if n := count(t, s, `SELECT count(*) FROM runs`); n != 0 {
		t.Errorf("%d runs written", n)
	}
}

func TestGetAgentByNameIgnoresCaseAndTeams(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")

	// Both teams have an agent called triage; each must get its own.
	for _, team := range []runFixture{f, other} {
		a, err := s.GetAgentByName(t.Context(), db.GetAgentByNameParams{TeamID: team.team, Name: "TRIAGE"})
		if err != nil || a.ID != team.agent {
			t.Errorf("GetAgentByName(TRIAGE) in %s = %s, %v; want %s", team.team, a.ID, err, team.agent)
		}
	}

	_, err := s.GetAgentByName(t.Context(), db.GetAgentByNameParams{TeamID: f.team, Name: "nobody"})
	wantNoRow(t, err, "an agent nobody created")
}

// --- holding one ---

// Two workers dequeue the same run; the second must see the first one's
// claim and take nothing. tx holds the first claim open so the second really
// overlaps it rather than running after it.
func TestClaimRunTakesAQueuedRunOnce(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)

	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	if _, err := s.WithTx(tx).ClaimRun(t.Context(),
		db.ClaimRunParams{ID: r.ID, Worker: "w1", WallClockSeconds: wallClock}); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		_, err := s.ClaimRun(t.Context(), db.ClaimRunParams{ID: r.ID, Worker: "w2", WallClockSeconds: wallClock})
		second <- err
	}()

	select {
	case err := <-second:
		t.Fatalf("the second claim did not wait for the first: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	wantNoRow(t, <-second, "a second worker's claim")

	if got := statusOf(t, s, r.ID); got.Worker == nil || *got.Worker != "w1" {
		t.Errorf("worker = %v, want w1", got.Worker)
	}
}

// After a takeover the new holder claims again to carry on; that must work,
// and a third worker must still get nothing.
func TestClaimRunCarriesOnARunThisWorkerHolds(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	claim(t, s, r.ID, "w1")

	_, err := s.ClaimRun(t.Context(), db.ClaimRunParams{ID: r.ID, Worker: "w2", WallClockSeconds: wallClock})
	wantNoRow(t, err, "another worker's claim of a running run")
}

func TestClaimRunIgnoresACancelledRun(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	if _, err := s.RequestRunCancel(t.Context(), r.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	_, err := s.ClaimRun(t.Context(), db.ClaimRunParams{ID: r.ID, Worker: "w1", WallClockSeconds: wallClock})
	wantNoRow(t, err, "claiming a run cancelled while queued")
}

// A resumed run must not get a fresh thirty minutes: the budget is counted
// from the first start, on the database's clock.
func TestTheWallClockLeftIsMeasuredFromTheFirstStart(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)

	first := claim(t, s, r.ID, "w1")
	if first.RemainingSeconds < wallClock-30 || first.RemainingSeconds > wallClock {
		t.Fatalf("remaining at start = %.0f, want about %d", first.RemainingSeconds, wallClock)
	}

	age(t, s, r.ID, "started_at", 1000)
	if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET worker = 'w2' WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("hand the run to w2: %v", err)
	}

	again := claim(t, s, r.ID, "w2")
	want := float64(wallClock - 1000)
	if again.RemainingSeconds < want-30 || again.RemainingSeconds > want {
		t.Errorf("remaining after a resume = %.0f, want about %.0f", again.RemainingSeconds, want)
	}
}

func TestAHeartbeatFromALostWorkerReturnsNothing(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	if _, err := s.HeartbeatRun(t.Context(), db.HeartbeatRunParams{ID: r.ID, Worker: "w1"}); err != nil {
		t.Fatalf("the holder's heartbeat: %v", err)
	}
	_, err := s.HeartbeatRun(t.Context(), db.HeartbeatRunParams{ID: r.ID, Worker: "w2"})
	wantNoRow(t, err, "a heartbeat from a worker that does not hold the run")
}

func TestAHeartbeatCarriesACancelRequest(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	before, err := s.HeartbeatRun(t.Context(), db.HeartbeatRunParams{ID: r.ID, Worker: "w1"})
	if err != nil || before != nil {
		t.Fatalf("before a cancel: %v, %v; want no request", before, err)
	}
	if _, err := s.RequestRunCancel(t.Context(), r.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	after, err := s.HeartbeatRun(t.Context(), db.HeartbeatRunParams{ID: r.ID, Worker: "w1"})
	if err != nil || after == nil {
		t.Errorf("after a cancel: %v, %v; want the request", after, err)
	}
}

func TestUsageAddsUpAndOnlyForTheHolder(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	add := func(worker string, in, out int64) (int64, error) {
		return s.AddRunUsage(t.Context(), db.AddRunUsageParams{
			ID: r.ID, Worker: worker, InputTokens: in, OutputTokens: out,
		})
	}

	if spent, err := add("w1", 100, 20); err != nil || spent != 120 {
		t.Fatalf("first usage: %d, %v; want 120", spent, err)
	}
	if spent, err := add("w1", 50, 5); err != nil || spent != 175 {
		t.Errorf("second usage: %d, %v; want 175", spent, err)
	}
	_, err := add("w2", 1000, 1000)
	wantNoRow(t, err, "usage from a worker that does not hold the run")
	if got := statusOf(t, s, r.ID); got.InputTokens+got.OutputTokens != 175 {
		t.Errorf("total = %d after a lost worker's usage, want 175", got.InputTokens+got.OutputTokens)
	}
}

func TestFinishRunOnlyFinishesARunThisWorkerHolds(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	n, err := s.FinishRun(t.Context(), db.FinishRunParams{
		ID: r.ID, Worker: "w2", Status: "succeeded", Output: ptr("not mine"),
	})
	if err != nil || n != 0 {
		t.Fatalf("another worker's finish: %d rows, %v; want 0", n, err)
	}

	n, err = s.FinishRun(t.Context(), db.FinishRunParams{
		ID: r.ID, Worker: "w1", Status: "succeeded", Output: ptr("14 labelled"),
	})
	if err != nil || n != 1 {
		t.Fatalf("the holder's finish: %d rows, %v; want 1", n, err)
	}
	got := statusOf(t, s, r.ID)
	if got.Status != "succeeded" || got.Output == nil || *got.Output != "14 labelled" || got.FinishedAt == nil {
		t.Errorf("after finish: %s, %v, finished %v", got.Status, got.Output, got.FinishedAt)
	}

	n, _ = s.FinishRun(t.Context(), db.FinishRunParams{
		ID: r.ID, Worker: "w1", Status: "failed", Error: ptr("again"),
	})
	if n != 0 {
		t.Error("a finished run was finished a second time")
	}
}

// --- cancelling one ---

func TestCancelOfAQueuedRunFinishesIt(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)

	got, err := s.RequestRunCancel(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.Status != "cancelled" || got.FinishedAt == nil || got.CancelRequestedAt == nil {
		t.Errorf("status %s, finished %v, requested %v; want cancelled and both set",
			got.Status, got.FinishedAt, got.CancelRequestedAt)
	}
}

func TestCancelOfARunningRunOnlyFlagsIt(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	got, err := s.RequestRunCancel(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.Status != "running" || got.FinishedAt != nil || got.CancelRequestedAt == nil {
		t.Errorf("status %s, finished %v, requested %v; want still running, flagged",
			got.Status, got.FinishedAt, got.CancelRequestedAt)
	}
}

// Asking twice is not an error, and does not move the time of the first ask:
// that is when the person wanted it to stop.
func TestCancellingTwiceKeepsTheFirstRequest(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	if _, err := s.RequestRunCancel(t.Context(), r.ID); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	age(t, s, r.ID, "cancel_requested_at", 60)
	first := statusOf(t, s, r.ID).CancelRequestedAt

	second, err := s.RequestRunCancel(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if second.CancelRequestedAt == nil || !second.CancelRequestedAt.Equal(*first) {
		t.Errorf("requested at %v after a second cancel, want the first, %v", second.CancelRequestedAt, first)
	}
}

func TestCancelOfAFinishedRunReturnsNothing(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")
	if _, err := s.FinishRun(t.Context(), db.FinishRunParams{
		ID: r.ID, Worker: "w1", Status: "succeeded", Output: ptr("done"),
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	_, err := s.RequestRunCancel(t.Context(), r.ID)
	wantNoRow(t, err, "cancelling a finished run")
	if got := statusOf(t, s, r.ID); got.Status != "succeeded" || got.CancelRequestedAt != nil {
		t.Errorf("a finished run became %s, requested %v", got.Status, got.CancelRequestedAt)
	}
}

// --- the sweep ---

func TestListUnstartedRunsSkipsFreshAndStartedRuns(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	lost := createRun(t, s, f)
	fresh := createRun(t, s, f)
	started := createRun(t, s, f)
	age(t, s, lost.ID, "created_at", 60)
	age(t, s, started.ID, "created_at", 60)
	claim(t, s, started.ID, "w1")

	got, err := s.ListUnstartedRuns(t.Context(), 30)
	if err != nil {
		t.Fatalf("ListUnstartedRuns: %v", err)
	}
	if !slices.Equal(got, []uuid.UUID{lost.ID}) {
		t.Errorf("unstarted = %v, want only the one queued for a minute (%s), not %s or %s",
			got, lost.ID, fresh.ID, started.ID)
	}
}

func TestTakeOverStaleRunsLeavesALiveRunAlone(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	got, err := s.TakeOverStaleRuns(t.Context(), db.TakeOverStaleRunsParams{Worker: "w2", StaleSeconds: 90})
	if err != nil || len(got) != 0 {
		t.Errorf("took %v, %v; want nothing from a live worker", got, err)
	}
}

func TestTakeOverStaleRunsTakesAStalledRun(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")
	age(t, s, r.ID, "heartbeat_at", 120)

	got, err := s.TakeOverStaleRuns(t.Context(), db.TakeOverStaleRunsParams{Worker: "w2", StaleSeconds: 90})
	if err != nil || !slices.Equal(got, []uuid.UUID{r.ID}) {
		t.Fatalf("took %v, %v; want the stalled run", got, err)
	}

	// The old worker's next heartbeat is how it learns, so it must fail now,
	// and the new holder's must succeed.
	_, err = s.HeartbeatRun(t.Context(), db.HeartbeatRunParams{ID: r.ID, Worker: "w1"})
	wantNoRow(t, err, "the old worker's heartbeat after a takeover")
	if _, err := s.HeartbeatRun(t.Context(), db.HeartbeatRunParams{ID: r.ID, Worker: "w2"}); err != nil {
		t.Errorf("the new holder's heartbeat: %v", err)
	}
}

// Two sweeps at once must split stale runs, never share one. The first holds
// its takeover open; SKIP LOCKED means the second returns at once, empty.
func TestTakeOverStaleRunsHasOneWinner(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")
	age(t, s, r.ID, "heartbeat_at", 120)

	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	first, err := s.WithTx(tx).TakeOverStaleRuns(t.Context(),
		db.TakeOverStaleRunsParams{Worker: "w2", StaleSeconds: 90})
	if err != nil || len(first) != 1 {
		t.Fatalf("first sweep: %v, %v", first, err)
	}

	// Without SKIP LOCKED this would wait on tx forever; the deadline turns
	// that into a failure instead of a hung suite.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	second, err := s.TakeOverStaleRuns(ctx, db.TakeOverStaleRunsParams{Worker: "w3", StaleSeconds: 90})
	if err != nil || len(second) != 0 {
		t.Errorf("second sweep took %v, %v while the first held it; want nothing", second, err)
	}

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := statusOf(t, s, r.ID); got.Worker == nil || *got.Worker != "w2" {
		t.Errorf("worker = %v, want w2", got.Worker)
	}
}

// --- who may see one ---

func insertDirectSession(t *testing.T, s *Store, teamID, userID uuid.UUID) uuid.UUID {
	t.Helper()

	sess, err := s.CreateDirectSession(t.Context(), db.CreateDirectSessionParams{TeamID: teamID, UserID: userID})
	if err != nil {
		t.Fatalf("CreateDirectSession: %v", err)
	}
	if sess.Kind != "direct" || sess.ChannelID != nil || sess.UserID == nil || *sess.UserID != userID {
		t.Fatalf("session = %s, channel %v, user %v; want direct, no channel, its user",
			sess.Kind, sess.ChannelID, sess.UserID)
	}
	return sess.ID
}

func TestSomeoneElsesDirectRunIsHidden(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	alice := insertUser(t, s, testIssuer, "alice")
	bob := insertUser(t, s, testIssuer, "bob")
	f.session = insertDirectSession(t, s, f.team, alice)
	r := createRun(t, s, f)

	see := func(viewer uuid.UUID, moderator bool) error {
		_, err := s.GetRunVisible(t.Context(), db.GetRunVisibleParams{
			ID: r.ID, TeamID: f.team, Viewer: viewer, Moderator: moderator,
		})
		return err
	}

	if err := see(alice, false); err != nil {
		t.Errorf("alice cannot see her own run: %v", err)
	}
	wantNoRow(t, see(bob, false), "bob reading alice's direct run")
	if err := see(bob, true); err != nil {
		t.Errorf("a moderator cannot see it: %v", err)
	}
}

func TestAGroupRunIsVisibleToTheTeamAndNoOneElse(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")
	r := createRun(t, s, f)
	bob := insertUser(t, s, testIssuer, "bob")

	if _, err := s.GetRunVisible(t.Context(), db.GetRunVisibleParams{
		ID: r.ID, TeamID: f.team, Viewer: bob,
	}); err != nil {
		t.Errorf("a group session's run is hidden from the team: %v", err)
	}
	_, err := s.GetRunVisible(t.Context(), db.GetRunVisibleParams{
		ID: r.ID, TeamID: other.team, Viewer: bob, Moderator: true,
	})
	wantNoRow(t, err, "reading a run through another team's id")
}

func TestSomeoneElsesDirectSessionIsHidden(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	alice := insertUser(t, s, testIssuer, "alice")
	bob := insertUser(t, s, testIssuer, "bob")
	sess := insertDirectSession(t, s, f.team, alice)

	see := func(viewer uuid.UUID, moderator bool) error {
		_, err := s.GetSessionVisible(t.Context(), db.GetSessionVisibleParams{
			ID: sess, TeamID: f.team, Viewer: viewer, Moderator: moderator,
		})
		return err
	}

	if err := see(alice, false); err != nil {
		t.Errorf("alice cannot see her own session: %v", err)
	}
	wantNoRow(t, see(bob, false), "bob reading alice's direct session")
	if err := see(bob, true); err != nil {
		t.Errorf("a moderator cannot see it: %v", err)
	}
}

// --- what a run binds ---

func TestListGrantedMCPServersCarriesTheAllowList(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	github := insertServer(t, s, f.team, "github")
	linear := insertServer(t, s, f.team, "linear")
	insertServer(t, s, f.team, "ungranted")
	if _, err := s.pool.Exec(t.Context(), `
		INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id, allow)
		VALUES ($1, $2, $3, ARRAY['list_issues']), ($1, $4, $3, NULL)`,
		f.agent, github, f.team, linear); err != nil {
		t.Fatalf("grant: %v", err)
	}

	got, err := s.ListGrantedMCPServers(t.Context(), f.agent)
	if err != nil {
		t.Fatalf("ListGrantedMCPServers: %v", err)
	}
	if len(got) != 2 || got[0].McpServer.Name != "github" || got[1].McpServer.Name != "linear" {
		t.Fatalf("granted = %v, want github then linear and not the ungranted one", got)
	}
	if !slices.Equal(got[0].Allow, []string{"list_issues"}) || got[1].Allow != nil {
		t.Errorf("allow = %v and %v; want [list_issues] and every tool", got[0].Allow, got[1].Allow)
	}
}

func TestListGrantedSkillsReadsTheBodyOfOnlyThoseGranted(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	review := insertSkill(t, s, f.team, "review")
	insertSkill(t, s, f.team, "ungranted")
	mustGrant(t, grantSkill(t, s, f.team, f.agent, review))

	got, err := s.ListGrantedSkills(t.Context(), f.agent)
	if err != nil {
		t.Fatalf("ListGrantedSkills: %v", err)
	}
	if len(got) != 1 || got[0].Name != "review" || got[0].Body != "# body" {
		t.Errorf("granted = %v, want review with its body", got)
	}
}
