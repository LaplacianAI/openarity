package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A delegate parks as its root does, holding nothing, and only while the
// worker that holds it is the one parking it. A root is not parked this way.
func TestADelegateParksOnlyWhileItsWorkerHoldsIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "park-child")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	child := mustInsertRun(t, s, inState(f.child(root, "call_1#x"), "running"))

	if n, err := s.ParkChild(t.Context(), db.ParkChildParams{ID: child, Worker: "someone-else"}); err != nil || n != 0 {
		t.Fatalf("parked by another worker: %d, %v", n, err)
	}
	if n, err := s.ParkChild(t.Context(), db.ParkChildParams{ID: root, Worker: "worker-1"}); err != nil || n != 0 {
		t.Fatalf("a root parked as a delegate: %d, %v", n, err)
	}
	if n, err := s.ParkChild(t.Context(), db.ParkChildParams{ID: child, Worker: "worker-1"}); err != nil || n != 1 {
		t.Fatalf("ParkChild = %d, %v", n, err)
	}
	got, err := s.GetRun(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "waiting" || got.Worker != nil || got.WaitingSince == nil {
		t.Errorf("child is %s, worker %v, waiting since %v", got.Status, got.Worker, got.WaitingSince)
	}
}

// A resumed parent takes up each child that waits, in the order they were
// started, and only one that waits; the time it waited is counted as
// waiting.
func TestAResumeTakesUpTheChildrenThatWait(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "adopt")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	waits := mustInsertRun(t, s, inState(f.child(root, "call_1#a"), "running"))
	park(t, s, waits)
	exec(t, s, `UPDATE runs SET waiting_since = now() - interval '10 minutes' WHERE id = '`+waits.String()+`'`)
	runs := mustInsertRun(t, s, inState(f.child(root, "call_2#b"), "running"))

	listed, err := s.ListWaitingChildren(t.Context(), root)
	if err != nil || len(listed) != 1 || listed[0].ID != waits {
		t.Fatalf("ListWaitingChildren = %v, %v; want only the waiting one", listed, err)
	}
	if _, err := s.AdoptChild(t.Context(), db.AdoptChildParams{ID: runs, Worker: "worker-2"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a running child was taken up: %v", err)
	}
	if _, err := s.AdoptChild(t.Context(), db.AdoptChildParams{ID: root, Worker: "worker-2"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a root was taken up as a child: %v", err)
	}

	got, err := s.AdoptChild(t.Context(), db.AdoptChildParams{ID: waits, Worker: "worker-2"})
	if err != nil {
		t.Fatalf("AdoptChild: %v", err)
	}
	if got.Status != "running" || deref(got.Worker) != "worker-2" || got.WaitingSince != nil || got.WaitedSeconds < 590 {
		t.Errorf("adopted as %s by %v, waiting since %v, waited %ds", got.Status, got.Worker, got.WaitingSince, got.WaitedSeconds)
	}
}

// A resume cuts off what ran beneath the run, at any depth, and leaves what
// waits for the resume to take up; a parent that ends takes its waiting
// children with it.
func TestAResumeLeavesWhatWaitsAndAnEndTakesIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "reap")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	waits := mustInsertRun(t, s, inState(f.child(root, "call_1#a"), "running"))
	park(t, s, waits)
	running := mustInsertRun(t, s, inState(f.child(root, "call_2#b"), "running"))
	under := f.child(running, "call_3#c")
	under.depth, under.root = 2, root
	below := mustInsertRun(t, s, inState(under, "running"))

	reaped, err := s.ReapChildren(t.Context(), db.ReapChildrenParams{ParentID: root, Reason: "resumed"})
	if err != nil {
		t.Fatalf("ReapChildren: %v", err)
	}
	slices.SortFunc(reaped, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	want := []uuid.UUID{running, below}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if !slices.Equal(reaped, want) {
		t.Fatalf("reaped %v, want %v", reaped, want)
	}
	if got, _ := s.GetRun(t.Context(), waits); got.Status != "waiting" {
		t.Errorf("the waiting child is %s, want left for the resume", got.Status)
	}

	if _, err := s.ReapChildren(t.Context(), db.ReapChildrenParams{ParentID: root, Reason: "ended", IncludeWaiting: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRun(t.Context(), waits); got.Status != "failed" || got.WaitingSince != nil {
		t.Errorf("after the parent ended the waiting child is %s, waiting since %v; want failed", got.Status, got.WaitingSince)
	}
}

// A run whose turn ended waits while it has a request its model has not been
// told the answer to, or a child that waits; once told, it may finish.
func TestARunWaitsUntilItIsToldEveryAnswer(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "awaits")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	awaits := func() bool {
		t.Helper()
		got, err := s.RunAwaits(t.Context(), root)
		if err != nil {
			t.Fatalf("RunAwaits: %v", err)
		}
		return got
	}
	if awaits() {
		t.Fatal("a run that asked nothing waits")
	}

	a := mustAsk(t, s, asking(f, root, root, "c1", "AWTA"))
	if !awaits() {
		t.Error("a run with a request pending does not wait")
	}
	a = approve(t, s, a)
	if !awaits() {
		t.Error("a run not yet told its answer does not wait")
	}
	if err := s.MarkApprovalsTold(t.Context(), []uuid.UUID{a.ID}); err != nil {
		t.Fatal(err)
	}
	if awaits() {
		t.Error("a run told every answer still waits")
	}

	child := mustInsertRun(t, s, inState(f.child(root, "call_1#a"), "running"))
	park(t, s, child)
	if !awaits() {
		t.Error("a run whose child waits does not wait")
	}
}

// A request is told once it is answered, and only then; a call asked again
// while it waits finds the request already made.
func TestARequestIsToldOnceAnsweredAndFoundAgainWhilePending(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "told")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	p := asking(f, root, root, "c1", "TLAB")
	a := mustAsk(t, s, p)

	found, err := s.FindPendingRequest(t.Context(), db.FindPendingRequestParams{RunID: root, Tool: p.Tool, Arguments: []byte(`{ "title" : "x" }`)})
	if err != nil || found.ID != a.ID {
		t.Fatalf("FindPendingRequest = %v, %v; want the request, whatever the spacing", found.ID, err)
	}
	if _, err := s.FindPendingRequest(t.Context(), db.FindPendingRequestParams{RunID: root, Tool: p.Tool, Arguments: []byte(`{"title":"y"}`)}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("other arguments found a request: %v", err)
	}

	if err := s.MarkApprovalsTold(t.Context(), []uuid.UUID{a.ID}); err != nil {
		t.Fatal(err)
	}
	if untold, _ := s.ListUntoldApprovals(t.Context(), root); len(untold) != 0 {
		t.Errorf("a pending request is listed as answered: %v", untold)
	}
	if got, _ := s.GetApproval(t.Context(), db.GetApprovalParams{ID: a.ID, TeamID: f.team}); got.ToldAt != nil {
		t.Error("a pending request was marked told")
	}
	approve(t, s, a)
	if untold, _ := s.ListUntoldApprovals(t.Context(), root); len(untold) != 1 {
		t.Errorf("untold = %v, want the answered request", untold)
	}
	_, err = s.pool.Exec(t.Context(), `UPDATE approvals SET status = 'pending', decided_at = NULL, told_at = now() WHERE id = $1`, a.ID)
	wantConstraint(t, err, "23514", "approvals_told_after_decided")
}

// The tree's six hours are what it ran, not what it waited.
func TestATreesElapsedTimeLeavesOutItsWait(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "usage")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	exec(t, s, `UPDATE runs SET started_at = now() - interval '7 hours', waited_seconds = 3*3600 WHERE id = '`+root.String()+`'`)

	used, err := s.TreeUsage(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if used.ElapsedSeconds < 3.9*3600 || used.ElapsedSeconds > 4.1*3600 {
		t.Errorf("elapsed %.0fs, want about 4 hours", used.ElapsedSeconds)
	}
}

// Writing again gives up only the writer's own requests in the tree,
// answered by them; another person's request stands.
func TestWritingAgainGivesUpOnlyTheWritersRequests(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "supersede")
	sam, ana := insertUser(t, s, "dev", "sam"), insertUser(t, s, "dev", "ana")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	child := mustInsertRun(t, s, inState(f.child(root, "call_1#a"), "running"))
	mine := asking(f, child, root, "c1", "SAMA")
	mine.RequestedFor = &sam
	theirs := asking(f, root, root, "c2", "ANAB")
	theirs.RequestedFor = &ana
	m, a := mustAsk(t, s, mine), mustAsk(t, s, theirs)

	ended, err := s.SupersedeApprovals(t.Context(), db.SupersedeApprovalsParams{RootRunID: root, UserID: sam, Via: "channel"})
	if err != nil {
		t.Fatalf("SupersedeApprovals: %v", err)
	}
	if len(ended) != 1 || ended[0].ID != m.ID || ended[0].Status != "cancelled" ||
		ended[0].DecidedBy == nil || *ended[0].DecidedBy != sam || deref(ended[0].DecidedVia) != "channel" {
		t.Fatalf("ended %+v, want only sam's, cancelled by sam via the channel", ended)
	}
	if got, _ := s.GetApproval(t.Context(), db.GetApprovalParams{ID: a.ID, TeamID: f.team}); got.Status != "pending" {
		t.Errorf("ana's request is %s, want still pending", got.Status)
	}

	if err := s.AppendDecidedEvents(t.Context(), []uuid.UUID{m.ID}); err != nil {
		t.Fatalf("AppendDecidedEvents: %v", err)
	}
	events, err := s.ListRunEvents(t.Context(), db.ListRunEventsParams{RunID: child, PageSize: 10})
	if err != nil || len(events) != 1 || events[0].Kind != "approval_decided" {
		t.Fatalf("child events = %v, %v", events, err)
	}
	want := `{"by": "` + sam.String() + `", "via": "channel", "decision": "cancelled", "superseded": true, "approval_id": "` + m.ID.String() + `"}`
	if got := string(events[0].Payload); got != want {
		t.Errorf("payload = %s\nwant      %s", got, want)
	}
}

// However a tree ends, nothing in it is left to answer, and each asking run
// says so: cancelled while it waits, or finished with a request open.
func TestATreeThatEndsLeavesNothingToAnswer(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "end")

	waiting := mustInsertRun(t, s, inState(f.root(), "running"))
	child := mustInsertRun(t, s, inState(f.child(waiting, "call_1#a"), "running"))
	asked := mustAsk(t, s, asking(f, child, waiting, "c1", "ENDA"))
	park(t, s, child)
	park(t, s, waiting)
	if _, err := s.RequestRunCancel(t.Context(), waiting); err != nil {
		t.Fatalf("RequestRunCancel: %v", err)
	}
	wantEnded(t, s, f, asked, child)

	finished := mustInsertRun(t, s, inState(f.root(), "running"))
	open := mustAsk(t, s, asking(f, finished, finished, "c1", "ENDB"))
	if _, _, err := s.FinishRunWithEvent(t.Context(),
		db.FinishRunParams{ID: finished, Worker: "worker-1", Status: "failed", Error: ptr("it broke")},
		db.AppendRunEventParams{RunID: finished, Worker: "worker-1", Kind: "finished", Payload: []byte(`{"status":"failed"}`)},
	); err != nil {
		t.Fatalf("FinishRunWithEvent: %v", err)
	}
	wantEnded(t, s, f, open, finished)
}

func wantEnded(t *testing.T, s *Store, f runFixture, a db.Approval, asker uuid.UUID) {
	t.Helper()

	got, err := s.GetApproval(t.Context(), db.GetApprovalParams{ID: a.ID, TeamID: f.team})
	if err != nil || got.Status != "cancelled" || got.DecidedBy != nil {
		t.Errorf("request = %s by %v, %v; want cancelled by nobody", got.Status, got.DecidedBy, err)
	}
	events, err := s.ListRunEvents(t.Context(), db.ListRunEventsParams{RunID: asker, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(e db.RunEvent) bool { return e.Kind == "approval_decided" }) {
		t.Errorf("the asking run's events %v hold no approval_decided", events)
	}
}

// A request reaches no further than its run: a direct conversation's are
// its person's and a moderator's; a group's are the team's. Status and run
// filters narrow, and the page goes newest first, after its cursor.
func TestARequestIsSeenWhereItsRunIs(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "visible")
	sam, ana := insertUser(t, s, "dev", "sam"), insertUser(t, s, "dev", "ana")
	var direct uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind, user_id) VALUES ($1, 'direct', $2) RETURNING id`, f.team, sam).Scan(&direct); err != nil {
		t.Fatal(err)
	}
	groupRun := mustInsertRun(t, s, inState(f.root(), "running"))
	samsRun := f.root()
	samsRun.session = direct
	samsRun = inState(samsRun, "running")
	mustInsertRun(t, s, samsRun)

	open := mustAsk(t, s, asking(f, groupRun, groupRun, "c1", "GRPA"))
	answered := approve(t, s, mustAsk(t, s, asking(f, groupRun, groupRun, "c2", "GRPB")))
	private := mustAsk(t, s, asking(f, samsRun.id, samsRun.id, "c1", "SAMA"))

	list := func(viewer uuid.UUID, moderator bool, statuses []string, root *uuid.UUID) []uuid.UUID {
		t.Helper()
		rows, err := s.ListVisibleApprovals(t.Context(), db.ListVisibleApprovalsParams{
			TeamID: f.team, Viewer: viewer, Moderator: moderator, Statuses: statuses, RootRunID: root, PageSize: 10,
		})
		if err != nil {
			t.Fatalf("ListVisibleApprovals: %v", err)
		}
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		return ids
	}
	if got := list(ana, false, nil, nil); !slices.Equal(got, []uuid.UUID{answered.ID, open.ID}) {
		t.Errorf("ana sees %v, want the group's two, newest first", got)
	}
	if got := list(sam, false, nil, nil); len(got) != 3 {
		t.Errorf("sam sees %d, want the group's and their own", len(got))
	}
	if got := list(ana, true, nil, nil); len(got) != 3 {
		t.Errorf("a moderator sees %d, want all three", len(got))
	}
	if got := list(ana, false, []string{"pending"}, nil); !slices.Equal(got, []uuid.UUID{open.ID}) {
		t.Errorf("pending = %v", got)
	}
	if got := list(sam, false, nil, &samsRun.id); !slices.Equal(got, []uuid.UUID{private.ID}) {
		t.Errorf("under sam's run = %v", got)
	}

	for viewer, want := range map[uuid.UUID]error{sam: nil, ana: pgx.ErrNoRows} {
		_, err := s.GetVisibleApproval(t.Context(), db.GetVisibleApprovalParams{ID: private.ID, TeamID: f.team, Viewer: viewer})
		if !errors.Is(err, want) && (want != nil || err != nil) {
			t.Errorf("GetVisibleApproval as %s: %v, want %v", viewer, err, want)
		}
	}
	if _, err := s.GetVisibleApproval(t.Context(), db.GetVisibleApprovalParams{ID: open.ID, TeamID: uuid.New(), Viewer: ana}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("another team's id: %v, want no row", err)
	}
}
