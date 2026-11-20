package store

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func asking(f runFixture, runID, root uuid.UUID, call, code string) db.InsertApprovalParams {
	return db.InsertApprovalParams{
		TeamID: f.team, RunID: runID, RootRunID: root, CallID: call,
		Tool: "github__create_issue", Class: "write", Arguments: []byte(`{"title":"x"}`),
		Summary: "create_issue title=x", Code: code, ExpiresAt: time.Now().Add(time.Hour),
	}
}

func mustAsk(t *testing.T, s *Store, p db.InsertApprovalParams) db.Approval {
	t.Helper()

	a, err := s.InsertApproval(t.Context(), p)
	if err != nil {
		t.Fatalf("InsertApproval(%s): %v", p.CallID, err)
	}
	return a
}

func approve(t *testing.T, s *Store, a db.Approval) db.Approval {
	t.Helper()

	got, err := s.DecideApproval(t.Context(), db.DecideApprovalParams{
		ID: a.ID, TeamID: a.TeamID, Status: "approved", DecidedVia: ptr("dashboard"), DecisionScope: ptr("once"),
	})
	if err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	return got
}

// A gate retried after a crash asks about the same call again: it gets no new
// row, and finds the one it wrote.
func TestAskingTwiceAboutOneCallKeepsTheFirstRequest(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "asking")
	r := mustInsertRun(t, s, inState(f.root(), "running"))

	first := mustAsk(t, s, asking(f, r, r, "call_1", "ABCD"))
	again := asking(f, r, r, "call_1", "WXYZ")
	if _, err := s.InsertApproval(t.Context(), again); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("asking again: err = %v, want pgx.ErrNoRows", err)
	}

	got, err := s.GetApprovalByCall(t.Context(), db.GetApprovalByCallParams{RunID: r, CallID: "call_1"})
	if err != nil {
		t.Fatalf("GetApprovalByCall: %v", err)
	}
	if got.ID != first.ID || got.Code != "ABCD" || got.Status != "pending" {
		t.Errorf("found %s code %s %s, want the first request, ABCD, pending", got.ID, got.Code, got.Status)
	}
}

// Two gates racing to spend one approval: exactly one wins, and an approval
// that was never approved cannot be spent at all.
func TestAnApprovalIsUsedOnce(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "used-once")
	r := mustInsertRun(t, s, inState(f.root(), "running"))

	pending := mustAsk(t, s, asking(f, r, r, "call_0", "PEND"))
	if _, err := s.ClaimApprovalUse(t.Context(), pending.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("spending a pending approval: err = %v, want pgx.ErrNoRows", err)
	}

	a := approve(t, s, mustAsk(t, s, asking(f, r, r, "call_1", "ABCD")))

	const racers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		won  int
		errs []error
	)
	start := make(chan struct{})
	for range racers {
		wg.Go(func() {
			<-start
			_, err := s.ClaimApprovalUse(t.Context(), a.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case !errors.Is(err, pgx.ErrNoRows):
				errs = append(errs, err)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("ClaimApprovalUse: %v", errs)
	}
	if won != 1 {
		t.Errorf("%d of %d gates spent the approval, want exactly 1", won, racers)
	}
}

// A code names one request among what a run tree is waiting on. Once that
// request is answered its code is free again, and another tree may use it
// all along.
func TestAPendingCodeNamesOneRequestUnderItsRoot(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "codes")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	child := mustInsertRun(t, s, inState(f.child(root, "call_d"), "running"))
	other := mustInsertRun(t, s, inState(f.root(), "running"))

	first := mustAsk(t, s, asking(f, root, root, "call_1", "ABCD"))
	_, err := s.InsertApproval(t.Context(), asking(f, child, root, "call_1", "ABCD"))
	wantConstraint(t, err, "23505", "approvals_pending_code_key")

	mustAsk(t, s, asking(f, other, other, "call_1", "ABCD"))

	approve(t, s, first)
	mustAsk(t, s, asking(f, child, root, "call_1", "ABCD"))
}

// A late answer to a request already answered changes nothing.
func TestOnlyAPendingApprovalIsDecided(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "decide")
	r := mustInsertRun(t, s, inState(f.root(), "running"))
	a := approve(t, s, mustAsk(t, s, asking(f, r, r, "call_1", "ABCD")))

	_, err := s.DecideApproval(t.Context(), db.DecideApprovalParams{
		ID: a.ID, TeamID: f.team, Status: "denied", DecidedVia: ptr("channel"), Reason: ptr("no"),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deciding again: err = %v, want pgx.ErrNoRows", err)
	}

	got, err := s.GetApproval(t.Context(), db.GetApprovalParams{ID: a.ID, TeamID: f.team})
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got.Status != "approved" || got.Reason != nil || got.DecidedVia == nil || *got.DecidedVia != "dashboard" {
		t.Errorf("after a late denial: %s via %s reason %s, want approved via dashboard, no reason",
			got.Status, deref(got.DecidedVia), deref(got.Reason))
	}

	other := newRunFixture(t, s, "decide-elsewhere")
	if _, err := s.GetApproval(t.Context(), db.GetApprovalParams{ID: a.ID, TeamID: other.team}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("another team's GetApproval: err = %v, want pgx.ErrNoRows", err)
	}
}

func TestAnApprovalsShapeIsChecked(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "shape")
	r := mustInsertRun(t, s, inState(f.root(), "running"))

	bad := asking(f, r, r, "call_1", "AB1D")
	_, err := s.InsertApproval(t.Context(), bad)
	wantConstraint(t, err, "23514", "approvals_code_shape")

	a := mustAsk(t, s, asking(f, r, r, "call_2", "ABCD"))
	for constraint, sql := range map[string]string{
		"approvals_decided_when_decided": `UPDATE approvals SET status = 'denied' WHERE id = $1`,
		"approvals_used_after_approved":  `UPDATE approvals SET used_at = now() WHERE id = $1`,
		"approvals_status_known":         `UPDATE approvals SET status = 'maybe', decided_at = now() WHERE id = $1`,
	} {
		_, err := s.pool.Exec(t.Context(), sql, a.ID)
		wantConstraint(t, err, "23514", constraint)
	}
}

// A waiting run is parked: no worker holds it, and it says since when.
func TestAWaitingRunHoldsNoWorker(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "parked")
	r := mustInsertRun(t, s, inState(f.root(), "running"))

	_, err := s.pool.Exec(t.Context(),
		`UPDATE runs SET status = 'waiting', waiting_since = now() WHERE id = $1`, r)
	wantConstraint(t, err, "23514", "runs_waiting_shape")

	_, err = s.pool.Exec(t.Context(),
		`UPDATE runs SET status = 'waiting', worker = NULL, heartbeat_at = NULL WHERE id = $1`, r)
	wantConstraint(t, err, "23514", "runs_waiting_shape")

	park(t, s, r)
}

func park(t *testing.T, s *Store, id uuid.UUID) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(), `
		UPDATE runs SET status = 'waiting', worker = NULL, heartbeat_at = NULL, waiting_since = now()
		WHERE id = $1`, id); err != nil {
		t.Fatalf("parking %s: %v", id, err)
	}
}

// A waiting run is unfinished: it is still the session's run, and cancelling
// it ends it at once, with whatever waits or runs beneath it. Nothing claims it but a decision.
func TestAWaitingRunIsUnfinished(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "unfinished")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	waitingChild := mustInsertRun(t, s, inState(f.child(root, "call_1"), "running"))
	runningChild := mustInsertRun(t, s, inState(f.child(root, "call_2"), "running"))
	park(t, s, waitingChild)
	park(t, s, root)

	if got, err := s.ActiveSessionRun(t.Context(), f.session); err != nil || got != root {
		t.Errorf("ActiveSessionRun = %s, %v; want the waiting run %s", got, err, root)
	}
	if _, err := s.ClaimRun(t.Context(), db.ClaimRunParams{ID: root, Worker: "worker-2"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("ClaimRun on a waiting run: err = %v, want pgx.ErrNoRows", err)
	}

	got, err := s.RequestRunCancel(t.Context(), root)
	if err != nil {
		t.Fatalf("RequestRunCancel: %v", err)
	}
	if got.Status != "cancelled" || got.FinishedAt == nil {
		t.Errorf("the waiting run is %s, finished %v; want cancelled at once", got.Status, got.FinishedAt)
	}
	for _, child := range []uuid.UUID{waitingChild, runningChild} {
		var status string
		if err := s.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id = $1`, child).Scan(&status); err != nil {
			t.Fatalf("reading %s: %v", child, err)
		}
		if status != "cancelled" {
			t.Errorf("a child left beneath the waiting run is %s, want cancelled with it", status)
		}
	}
}

// A team with nothing running but a run waiting for an answer is still busy.
func TestAWaitingRunKeepsItsTeamFromBeingDeleted(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "busy")
	r := mustInsertRun(t, s, inState(f.root(), "running"))
	park(t, s, r)

	if n, err := s.MarkTeamDeleted(t.Context(), f.team); err != nil || n != 0 {
		t.Errorf("MarkTeamDeleted with a waiting run = %d, %v; want 0", n, err)
	}
}

func TestDueRequestsExpireAndARunsRequestsAreCancelled(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "expire")
	r := mustInsertRun(t, s, inState(f.root(), "running"))

	dueP := asking(f, r, r, "call_1", "DUEA")
	dueP.ExpiresAt = time.Now().Add(-time.Minute)
	due := mustAsk(t, s, dueP)
	later := mustAsk(t, s, asking(f, r, r, "call_2", "LATR"))

	listed, err := s.ListDueApprovals(t.Context(), 10)
	if err != nil {
		t.Fatalf("ListDueApprovals: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != due.ID {
		t.Fatalf("due %v, want only %s", listed, due.ID)
	}
	if _, err := s.DecideApproval(t.Context(), db.DecideApprovalParams{
		ID: due.ID, TeamID: due.TeamID, Status: "expired", DecidedVia: ptr("sweep"),
	}); err != nil {
		t.Fatalf("expire: %v", err)
	}

	cancelled, err := s.CancelRunApprovals(t.Context(), r)
	if err != nil {
		t.Fatalf("CancelRunApprovals: %v", err)
	}
	if len(cancelled) != 1 || cancelled[0].ID != later.ID || cancelled[0].Status != "cancelled" {
		t.Errorf("cancelled %v, want only %s: an expired request stays expired", cancelled, later.ID)
	}
}

func TestPendingRequestsAreFoundByTheirConversation(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "by-session")
	other := newRunFixture(t, s, "by-session-other")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	child := mustInsertRun(t, s, inState(f.child(root, "call_d"), "running"))
	elsewhere := mustInsertRun(t, s, inState(other.root(), "running"))

	mine := mustAsk(t, s, asking(f, child, root, "call_1", "ABCD"))
	approve(t, s, mustAsk(t, s, asking(f, root, root, "call_2", "WXYZ")))
	mustAsk(t, s, asking(other, elsewhere, elsewhere, "call_1", "ABCD"))

	got, err := s.ListPendingApprovalsBySession(t.Context(), f.session)
	if err != nil {
		t.Fatalf("ListPendingApprovalsBySession: %v", err)
	}
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Errorf("pending in the session: %v, want only the child's %s", got, mine.ID)
	}
}

func TestARuleIsOnePerTargetWhateverItsCase(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "rules")
	rule := db.CreateApprovalRuleParams{TeamID: f.team, TargetKind: "tool", Target: "github__create_issue", Mode: "ask"}
	if _, err := s.CreateApprovalRule(t.Context(), rule); err != nil {
		t.Fatalf("CreateApprovalRule: %v", err)
	}
	rule.Target, rule.Mode = "GitHub__Create_Issue", "allow"
	_, err := s.CreateApprovalRule(t.Context(), rule)
	wantConstraint(t, err, "23505", "approval_rules_target_key")

	rule.TargetKind = "server"
	if _, err := s.CreateApprovalRule(t.Context(), rule); err != nil {
		t.Errorf("a server rule beside a tool rule of the same name: %v", err)
	}
	rule.Mode = "sometimes"
	rule.Target = "slack"
	_, err = s.CreateApprovalRule(t.Context(), rule)
	wantConstraint(t, err, "23514", "approval_rules_mode_known")
}

// Saving the rest of a team's settings leaves its approval settings as they
// were, and a team that never saved any gets the defaults for the rest.
func TestApprovalSettingsAreTheirOwnWrite(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "approval-settings")

	set, err := s.SetTeamApproval(t.Context(), db.SetTeamApprovalParams{TeamID: f.team, ApprovalMode: "writes", ApprovalTtlMinutes: 30})
	if err != nil {
		t.Fatalf("SetTeamApproval: %v", err)
	}
	if set.SearchProvider != "none" || set.CompactionMessages != 100 {
		t.Errorf("a first save left search %q and compaction %d, want the defaults", set.SearchProvider, set.CompactionMessages)
	}

	if _, err := s.UpsertTeamSettings(t.Context(), db.UpsertTeamSettingsParams{
		TeamID: f.team, SearchProvider: "none", CompactionMessages: 40, CompactionTokensMin: 50_000, CompactionTokensMax: 60_000, SandboxProvider: "none", BrowserProvider: "none",
	}); err != nil {
		t.Fatalf("UpsertTeamSettings: %v", err)
	}
	got, err := s.GetTeamSettings(t.Context(), f.team)
	if err != nil {
		t.Fatalf("GetTeamSettings: %v", err)
	}
	if got.ApprovalMode != "writes" || got.ApprovalTtlMinutes != 30 {
		t.Errorf("after saving the rest: mode %q ttl %d, want writes, 30", got.ApprovalMode, got.ApprovalTtlMinutes)
	}

	for _, p := range []db.SetTeamApprovalParams{
		{TeamID: f.team, ApprovalMode: "sometimes", ApprovalTtlMinutes: 30},
		{TeamID: f.team, ApprovalMode: "all", ApprovalTtlMinutes: 4},
	} {
		if _, err := s.SetTeamApproval(t.Context(), p); err == nil {
			t.Errorf("SetTeamApproval(%q, %d) was accepted", p.ApprovalMode, p.ApprovalTtlMinutes)
		}
	}
}

// A server's hints are stored with its tool, and a change to them alone is a
// change to the tool.
func TestAToolsHintsAreKeptAndAChangeToThemRewritesIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "hints")
	server := insertServer(t, s, f.team, "github")

	upsert := func(hints string) db.McpTool {
		t.Helper()
		var raw []byte
		if hints != "" {
			raw = []byte(hints)
		}
		if err := s.UpsertMCPTool(t.Context(), db.UpsertMCPToolParams{
			McpServerID: server, TeamID: f.team, Name: "create_issue",
			Description: "make one", InputSchema: []byte(`{"type":"object"}`), Annotations: raw,
		}); err != nil {
			t.Fatalf("UpsertMCPTool(%s): %v", hints, err)
		}
		tools, err := s.ListMCPToolsByServer(t.Context(), server)
		if err != nil || len(tools) != 1 {
			t.Fatalf("ListMCPToolsByServer = %v, %v", tools, err)
		}
		return tools[0]
	}

	none := upsert("")
	if none.Annotations != nil {
		t.Errorf("no hints stored as %s, want NULL", none.Annotations)
	}
	hinted := upsert(`{"readOnlyHint":false,"destructiveHint":true}`)
	if !hinted.UpdatedAt.After(none.UpdatedAt) {
		t.Error("gaining hints did not rewrite the tool")
	}
	same := upsert(`{"destructiveHint": true, "readOnlyHint": false}`)
	if !same.UpdatedAt.Equal(hinted.UpdatedAt) {
		t.Error("the same hints, reordered, rewrote the tool")
	}
	changed := upsert(`{"readOnlyHint":true}`)
	if !changed.UpdatedAt.After(same.UpdatedAt) {
		t.Error("a hint that changed did not rewrite the tool")
	}
	var hints map[string]bool
	if err := json.Unmarshal(changed.Annotations, &hints); err != nil || !hints["readOnlyHint"] {
		t.Errorf("stored hints %s, %v; want readOnlyHint true", changed.Annotations, err)
	}

	n, err := s.SetServerTrustHints(t.Context(), db.SetServerTrustHintsParams{ID: server, TeamID: f.team, TrustHints: true})
	if err != nil || n != 1 {
		t.Fatalf("SetServerTrustHints = %d, %v", n, err)
	}
	other := newRunFixture(t, s, "hints-elsewhere")
	if n, err := s.SetServerTrustHints(t.Context(), db.SetServerTrustHintsParams{ID: server, TeamID: other.team, TrustHints: false}); err != nil || n != 0 {
		t.Errorf("another team's SetServerTrustHints = %d, %v; want 0", n, err)
	}
}

// Deciding is for admins and approvers; a member may read and answer for
// themselves; a guest, who only talks to the agents from a channel, holds
// none of it.
func TestTheApprovalPermissionsAreGrantedAsDesigned(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	rows, err := s.pool.Query(t.Context(),
		`SELECT role, action FROM role_permissions WHERE action LIKE 'approval:%' ORDER BY role, action`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var role, action string
		err := r.Scan(&role, &action)
		return role + " " + action, err
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := []string{
		"admin approval:decide", "admin approval:read", "admin approval:self",
		"approver approval:decide", "approver approval:read", "approver approval:self",
		"member approval:read", "member approval:self",
	}
	if !slices.Equal(got, want) {
		t.Errorf("grants\n got %v\nwant %v", got, want)
	}
}

// The approvals migration rolls back with a parked run, a request and a rule
// in it, and applies again. The parked run cannot resume without the gate, so
// it is cancelled rather than left in a status the old check refuses.
func TestTheApprovalsMigrationRollsBackWithDataInIt(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	f := newRunFixture(t, s, "rollback")
	r := mustInsertRun(t, s, inState(f.root(), "running"))
	mustAsk(t, s, asking(f, r, r, "call_1", "ABCD"))
	park(t, s, r)
	if _, err := s.CreateApprovalRule(t.Context(), db.CreateApprovalRuleParams{
		TeamID: f.team, TargetKind: "server", Target: "github", Mode: "deny",
	}); err != nil {
		t.Fatalf("CreateApprovalRule: %v", err)
	}

	// To the migration before them, by version: counting steps back breaks
	// as soon as a later migration lands above the four of approvals.
	downTo(t, s, 20261119090000)
	if tableExists(t, s, "approvals") || tableExists(t, s, "approval_rules") {
		t.Error("the approval tables survived the rollback")
	}
	var status string
	if err := s.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id = $1`, r).Scan(&status); err != nil {
		t.Fatalf("reading the run: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("the parked run is %q after the rollback, want cancelled", status)
	}
	var indexes int
	if err := s.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM pg_indexes WHERE indexname IN ('runs_unfinished_idx', 'runs_active_idx')`).Scan(&indexes); err != nil {
		t.Fatalf("reading indexes: %v", err)
	}
	if indexes != 1 {
		t.Errorf("%d of runs_unfinished_idx and runs_active_idx after the rollback, want runs_unfinished_idx alone", indexes)
	}

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate again: %v", err)
	}
	park(t, s, mustInsertRun(t, s, inState(f.root(), "running")))
}

// A run waiting for an approval is not finished, and every query that means
// "not finished" has to say so. That is the part of the approvals change most
// likely to be forgotten in a query written later: a status list naming
// queued and running must name waiting too, or be listed here with the reason
// it must not. A listed query that no longer has such a list fails, so the
// list cannot outlive its reasons.
func TestEveryUnfinishedClauseNamesWaiting(t *testing.T) {
	t.Parallel()

	notWaiting := map[string]string{}
	seen := map[string]bool{}

	list := regexp.MustCompile(`status\s+IN\s*\(([^)]*)\)`)
	files, err := os.ReadDir("queries")
	if err != nil {
		t.Fatalf("reading queries: %v", err)
	}
	for _, file := range files {
		raw, err := os.ReadFile("queries/" + file.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", file.Name(), err)
		}
		for _, q := range strings.Split(string(raw), "-- name: ")[1:] {
			name := strings.Fields(q)[0]
			for _, m := range list.FindAllStringSubmatch(q, -1) {
				live := strings.Contains(m[1], "'queued'") && strings.Contains(m[1], "'running'")
				if !live || strings.Contains(m[1], "'waiting'") {
					continue
				}
				if _, ok := notWaiting[name]; ok {
					seen[name] = true
					continue
				}
				t.Errorf("%s in %s has %s without waiting: name it, or list the query here with why not",
					name, file.Name(), strings.TrimSpace(m[0]))
			}
		}
	}
	for name := range notWaiting {
		if !seen[name] {
			t.Errorf("%s is listed as not naming waiting, and has no such status list any more", name)
		}
	}
}

// Time spent waiting for a person is not the run's: a run that waited an
// hour has its six hours, less only what it ran.
func TestWaitingDoesNotSpendTheWallClock(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "clock")
	r := mustInsertRun(t, s, inState(f.root(), "queued"))
	exec(t, s, `UPDATE runs SET started_at = now() - interval '2 hours', waited_seconds = 5400 WHERE id = '`+r.String()+`'`)

	row, err := s.ClaimRun(t.Context(), db.ClaimRunParams{ID: r, Worker: "worker-1", WallClockSeconds: 6 * 3600})
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	// Ran 30 minutes of the two hours: five and a half left.
	if left := row.RemainingSeconds; left < 5.4*3600 || left > 5.6*3600 {
		t.Errorf("remaining %.0fs, want about 5.5 hours", left)
	}
}
