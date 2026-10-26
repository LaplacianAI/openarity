package store

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func listDelegate(t *testing.T, s *Store, teamID, agentID, delegateID uuid.UUID) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agent_delegates (agent_id, delegate_id, team_id)
		VALUES ($1, $2, $3)`, agentID, delegateID, teamID)
	return err
}

func delegationOf(t *testing.T, s *Store, agentID uuid.UUID) string {
	t.Helper()

	var d string
	if err := s.pool.QueryRow(t.Context(), `SELECT delegation FROM agents WHERE id = $1`, agentID).Scan(&d); err != nil {
		t.Fatalf("read delegation: %v", err)
	}
	return d
}

// --- the setting ---

func TestAnAgentWorksAloneByDefault(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID

	if got := delegationOf(t, s, insertAgent(t, s, team, "triage")); got != "none" {
		t.Errorf("delegation = %q, want none", got)
	}
}

func TestEveryKnownDelegationIsAccepted(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a := insertAgent(t, s, team, "triage")

	for _, d := range []string{"team", "listed", "none"} {
		if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET delegation = $2 WHERE id = $1`, a, d); err != nil {
			t.Errorf("%s refused: %v", d, err)
		}
	}
}

func TestAnUnknownDelegationIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a := insertAgent(t, s, team, "triage")

	for _, d := range []string{"some", "", "TEAM"} {
		_, err := s.pool.Exec(t.Context(), `UPDATE agents SET delegation = $2 WHERE id = $1`, a, d)
		wantConstraint(t, err, "23514", "agents_delegation_known")
	}
}

// --- the listing ---

func TestAnAgentCanListAnotherInItsTeam(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a, b := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "clerk")

	if err := listDelegate(t, s, team, a, b); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := listDelegate(t, s, team, a, b); err == nil {
		t.Error("the same pair was listed twice")
	}
}

func TestAnAgentCannotListItself(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a := insertAgent(t, s, team, "lead")

	wantConstraint(t, listDelegate(t, s, team, a, a), "23514", "agent_delegates_not_self")
}

// Both ends are in the row's team, whichever end is the stranger.
func TestADelegateMustBeInTheSameTeam(t *testing.T) {
	s := queryStore(t)
	mine, theirs := mustCreate(t, s, "platform").ID, mustCreate(t, s, "billing").ID
	a := insertAgent(t, s, mine, "lead")
	stranger := insertAgent(t, s, theirs, "clerk")

	wantConstraint(t, listDelegate(t, s, mine, a, stranger), "23503", "agent_delegates_delegate_in_team")
	wantConstraint(t, listDelegate(t, s, theirs, a, stranger), "23503", "agent_delegates_agent_in_team")
}

// Deleting either end removes the listing and nothing else: an agent is
// never kept alive by being somebody's delegate.
func TestDeletingEitherEndRemovesTheListing(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	lead, clerk, other := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "clerk"), insertAgent(t, s, team, "other")
	for _, p := range [][2]uuid.UUID{{lead, clerk}, {other, lead}, {other, clerk}} {
		if err := listDelegate(t, s, team, p[0], p[1]); err != nil {
			t.Fatalf("list: %v", err)
		}
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1`, lead); err != nil {
		t.Fatalf("delete an agent that lists and is listed: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM agent_delegates`); n != 1 {
		t.Errorf("%d listings left, want only other→clerk", n)
	}
	if n := count(t, s, `SELECT count(*) FROM agents WHERE team_id = $1`, team); n != 2 {
		t.Errorf("%d agents left, want clerk and other", n)
	}
}

// --- the graph is not told ---

// Nothing in the graph is drawn from a listing: the list is read from Postgres
// when a strategy's limit is checked, so writing or clearing it is no reason
// to reproject the agent.
func TestAListingLeavesTheGraphAlone(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	lead, a, b := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "a"), insertAgent(t, s, team, "b")
	clearOutbox(t, s)

	if err := s.AddAgentDelegates(t.Context(), db.AddAgentDelegatesParams{
		AgentID: lead, TeamID: team, DelegateIds: []uuid.UUID{a, b},
	}); err != nil {
		t.Fatalf("AddAgentDelegates: %v", err)
	}
	if got := outbox(t, s); len(got) != 0 {
		t.Errorf("after listing, outbox = %v, want nothing", got)
	}

	if err := s.ClearAgentDelegates(t.Context(), lead); err != nil {
		t.Fatalf("ClearAgentDelegates: %v", err)
	}
	if got := outbox(t, s); len(got) != 0 {
		t.Errorf("after clearing, outbox = %v, want nothing", got)
	}
}

// --- the delegates queries ---

func setDelegation(t *testing.T, s *Store, agentID uuid.UUID, d string) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET delegation = $2 WHERE id = $1`, agentID, d); err != nil {
		t.Fatalf("set delegation %s: %v", d, err)
	}
}

func TestDelegatesAreAddedListedAndCleared(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	lead, a, b := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "a"), insertAgent(t, s, team, "b")

	if err := s.AddAgentDelegates(t.Context(), db.AddAgentDelegatesParams{
		AgentID: lead, TeamID: team, DelegateIds: []uuid.UUID{b, a},
	}); err != nil {
		t.Fatalf("AddAgentDelegates: %v", err)
	}
	got, err := s.ListAgentDelegates(t.Context(), lead)
	if err != nil {
		t.Fatalf("ListAgentDelegates: %v", err)
	}
	if want := sortedIDs(a, b); !slices.Equal(got, want) {
		t.Errorf("delegates = %v, want %v by id", got, want)
	}

	if err := s.ClearAgentDelegates(t.Context(), lead); err != nil {
		t.Fatalf("ClearAgentDelegates: %v", err)
	}
	if got, _ := s.ListAgentDelegates(t.Context(), lead); len(got) != 0 {
		t.Errorf("delegates after clearing = %v", got)
	}
}

// The graph tool's agents follow the caller's setting: the rest of the team,
// only those listed, or none.
func TestReachFollowsTheSetting(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	other := mustCreate(t, s, "billing").ID
	lead, a, b := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "a"), insertAgent(t, s, team, "b")
	insertAgent(t, s, other, "elsewhere")
	if err := listDelegate(t, s, team, lead, b); err != nil {
		t.Fatalf("list: %v", err)
	}
	// a lists lead, which must not make lead reach a; and b lists a, which
	// is b's listing, not lead's.
	if err := listDelegate(t, s, team, a, lead); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := listDelegate(t, s, team, b, a); err != nil {
		t.Fatalf("list: %v", err)
	}

	reach := func() []uuid.UUID {
		got, err := s.ListReachableAgentIDs(t.Context(), db.ListReachableAgentIDsParams{TeamID: team, AgentID: lead})
		if err != nil {
			t.Fatalf("ListReachableAgentIDs: %v", err)
		}
		return got
	}

	setDelegation(t, s, lead, "team")
	if got, want := reach(), sortedIDs(a, b); !slices.Equal(got, want) {
		t.Errorf("team: reach = %v, want %v", got, want)
	}
	setDelegation(t, s, lead, "listed")
	if got := reach(); !slices.Equal(got, []uuid.UUID{b}) {
		t.Errorf("listed: reach = %v, want only b", got)
	}
	setDelegation(t, s, lead, "none")
	if got := reach(); len(got) != 0 {
		t.Errorf("none: reach = %v, want nobody", got)
	}
}

// --- a child run ---

func startChild(t *testing.T, s *Store, parent db.Run, agentID uuid.UUID, call string) (db.Run, error) {
	t.Helper()

	return s.StartChildRun(t.Context(), db.StartChildRunParams{
		ID: uuid.New(), ParentID: parent.ID, AgentID: agentID, TeamID: parent.TeamID,
		ParentToolCall: call, Input: "reconcile last month", Worker: "w1", TokenBudget: 1_000_000,
	})
}

func mustStartChild(t *testing.T, s *Store, parent db.Run, agentID uuid.UUID, call string) db.Run {
	t.Helper()

	c, err := startChild(t, s, parent, agentID, call)
	if err != nil {
		t.Fatalf("StartChildRun: %v", err)
	}
	return c
}

// A child is born running and held, so no sweep ever starts it on its own,
// and carries everything that says whose it is.
func TestAChildIsBornRunningUnderItsParent(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	alice := insertUser(t, s, testIssuer, "alice")
	f.session = insertDirectSession(t, s, f.team, alice)
	parent, err := s.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), SessionID: f.session, Input: "find who can help",
		AgentID: f.agent, TeamID: f.team, UserID: &alice,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	clerk := insertAgent(t, s, f.team, "clerk")
	setTokenBudget(t, s, clerk, 5000)

	c := mustStartChild(t, s, parent, clerk, "call_1#x")

	switch {
	case c.Status != "running" || c.Worker == nil || *c.Worker != "w1" || c.HeartbeatAt == nil || c.StartedAt == nil:
		t.Errorf("child is %s held by %v, want running and held by w1 with a heartbeat", c.Status, c.Worker)
	case c.ParentRunID == nil || *c.ParentRunID != parent.ID || c.RootRunID != parent.RootRunID:
		t.Errorf("parent %v root %s, want %s and %s", c.ParentRunID, c.RootRunID, parent.ID, parent.RootRunID)
	case c.SessionID != parent.SessionID || c.UserID == nil || *c.UserID != alice:
		t.Errorf("session %s user %v, want the parent's", c.SessionID, c.UserID)
	case c.Depth != parent.Depth+1:
		t.Errorf("depth %d, want %d", c.Depth, parent.Depth+1)
	case c.AgentName != "clerk" || c.AgentID == nil || *c.AgentID != clerk:
		t.Errorf("agent %q %v, want clerk", c.AgentName, c.AgentID)
	case c.ParentToolCall == nil || *c.ParentToolCall != "call_1#x" || c.Input != "reconcile last month":
		t.Errorf("call %v input %q", c.ParentToolCall, c.Input)
	case c.TokenBudget == nil || *c.TokenBudget != 1_000_000:
		t.Errorf("token budget %v, want the 1000000 it was given, not the agent's 5000", c.TokenBudget)
	}
}

func setTokenBudget(t *testing.T, s *Store, agentID uuid.UUID, n int64) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET token_budget = $2 WHERE id = $1`, agentID, n); err != nil {
		t.Fatalf("set token budget: %v", err)
	}
}

// A grandchild is one deeper again, under the same root.
func TestEachDelegationIsOneDeeper(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	clerk := insertAgent(t, s, f.team, "clerk")

	child := mustStartChild(t, s, root, clerk, "c1")
	grandchild := mustStartChild(t, s, child, f.agent, "c2")

	if grandchild.Depth != 2 || grandchild.RootRunID != root.ID || *grandchild.ParentRunID != child.ID {
		t.Errorf("grandchild depth %d root %s parent %v", grandchild.Depth, grandchild.RootRunID, grandchild.ParentRunID)
	}
}

// The agent is selected within the team as the row is written, as CreateRun
// does: another team's agent writes nothing.
func TestAChildOfAnotherTeamsAgentIsNotStarted(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	stranger := insertAgent(t, s, mustCreate(t, s, "billing").ID, "clerk")

	_, err := startChild(t, s, root, stranger, "c1")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("err = %v, want no row", err)
	}
	if n := count(t, s, `SELECT count(*) FROM runs WHERE parent_run_id = $1`, root.ID); n != 0 {
		t.Errorf("%d children written", n)
	}
}

// The unique key still holds: a call that somehow repeats its key does not
// start a second child.
func TestTheSameCallKeyStartsOneChild(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	clerk := insertAgent(t, s, f.team, "clerk")

	mustStartChild(t, s, root, clerk, "c1#same")
	if _, err := startChild(t, s, root, clerk, "c1#same"); err == nil {
		t.Error("a second child was started under the same call key")
	}
}

// --- a tree's measure ---

func TestTreeUsageCountsTheWholeTreeAndNoOther(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	claim(t, s, root.ID, "w1")
	age(t, s, root.ID, "started_at", 120)
	clerk := insertAgent(t, s, f.team, "clerk")
	child := mustStartChild(t, s, root, clerk, "c1")
	grandchild := mustStartChild(t, s, child, f.agent, "c2")
	sibling := createRun(t, s, f)

	for id, n := range map[uuid.UUID]int64{root.ID: 100, child.ID: 20, grandchild.ID: 3, sibling.ID: 9000} {
		exec(t, s, `UPDATE runs SET input_tokens = `+itoa(n)+`, output_tokens = 1 WHERE id = '`+id.String()+`'`)
	}

	u, err := s.TreeUsage(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("TreeUsage: %v", err)
	}
	if u.Tokens != 126 || u.Runs != 3 {
		t.Errorf("tokens %d runs %d, want 126 over 3 runs", u.Tokens, u.Runs)
	}
	if u.ElapsedSeconds < 119 || u.ElapsedSeconds > 180 {
		t.Errorf("elapsed %.0fs, want about 120 from the root's start", u.ElapsedSeconds)
	}
}

func itoa(n int64) string { return fmt.Sprint(n) }

// --- after a crash ---

// Everything running beneath the parent fails with the reason; a finished
// child, the parent itself, and another parent's children are untouched.
func TestReapFailsTheRunningSubtreeAndNothingElse(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	claim(t, s, root.ID, "w1")
	clerk := insertAgent(t, s, f.team, "clerk")

	running := mustStartChild(t, s, root, clerk, "c1")
	deeper := mustStartChild(t, s, running, f.agent, "c2")
	done := mustStartChild(t, s, root, clerk, "c3")
	finishAs(t, s, done.ID, "succeeded")
	other := createRun(t, s, f)
	claim(t, s, other.ID, "w1")
	theirs := mustStartChild(t, s, other, clerk, "c4")

	reaped, err := s.ReapChildren(t.Context(), db.ReapChildrenParams{ParentID: root.ID, Reason: "its parent was resumed"})
	if err != nil {
		t.Fatalf("ReapChildren: %v", err)
	}
	if got, want := sortedIDs(reaped...), sortedIDs(running.ID, deeper.ID); !slices.Equal(got, want) {
		t.Errorf("reaped %v, want the running child and its child", got)
	}
	for _, id := range []uuid.UUID{running.ID, deeper.ID} {
		r := statusOf(t, s, id)
		if r.Status != "failed" || r.Error == nil || *r.Error != "its parent was resumed" || r.FinishedAt == nil {
			t.Errorf("reaped run is %s (%v)", r.Status, r.Error)
		}
	}
	for id, want := range map[uuid.UUID]string{done.ID: "succeeded", root.ID: "running", theirs.ID: "running"} {
		if got := statusOf(t, s, id).Status; got != want {
			t.Errorf("%s is %s, want %s", id, got, want)
		}
	}
}

func finishAs(t *testing.T, s *Store, id uuid.UUID, status string) {
	t.Helper()

	n, err := s.FinishRun(t.Context(), db.FinishRunParams{ID: id, Worker: "w1", Status: status})
	if err != nil || n != 1 {
		t.Fatalf("FinishRun: %d rows, %v", n, err)
	}
}

// Only a parent decides what happens to its child, so the sweep leaves a
// stale child alone even when its heartbeat is long gone.
func TestTheSweepNeverTakesOverAChild(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	claim(t, s, root.ID, "w1")
	child := mustStartChild(t, s, root, insertAgent(t, s, f.team, "clerk"), "c1")
	age(t, s, root.ID, "heartbeat_at", 3600)
	age(t, s, child.ID, "heartbeat_at", 3600)

	taken, err := s.TakeOverStaleRuns(t.Context(), 60)
	if err != nil {
		t.Fatalf("TakeOverStaleRuns: %v", err)
	}
	if !slices.Equal(taken, []uuid.UUID{root.ID}) {
		t.Errorf("took over %v, want only the root", taken)
	}
	if got := statusOf(t, s, child.ID); got.Status != "running" || *got.Worker != "w1" {
		t.Errorf("child is %s held by %v, want still running under w1", got.Status, got.Worker)
	}
}

// A child's budget is what its parent's delegate call gave it, never its
// agent's own: the call has already taken what the tree has left into account.
func TestAChildStartsWithTheBudgetItIsGiven(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	claim(t, s, root.ID, "w1")
	clerk := insertAgent(t, s, f.team, "clerk")
	if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET token_budget = 500000 WHERE id = $1`, clerk); err != nil {
		t.Fatal(err)
	}

	child, err := s.StartChildRun(t.Context(), db.StartChildRunParams{
		ID: uuid.New(), ParentID: root.ID, AgentID: clerk, TeamID: root.TeamID,
		ParentToolCall: "c1", Input: "reconcile last month", Worker: "w1", TokenBudget: 120_000,
	})
	if err != nil {
		t.Fatalf("StartChildRun: %v", err)
	}
	if child.TokenBudget == nil || *child.TokenBudget != 120_000 {
		t.Errorf("token_budget = %v, want the 120000 it was given", child.TokenBudget)
	}
}

// A run cancelled while queued never runs again, so nothing would ever end
// the children it left running before its worker stopped. The cancel ends
// them with it, at any depth, and leaves finished ones and other runs alone.
func TestCancellingAQueuedRunCancelsWhatItLeftRunning(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	claim(t, s, root.ID, "w1")
	clerk := insertAgent(t, s, f.team, "clerk")
	running := mustStartChild(t, s, root, clerk, "c1")
	deeper := mustStartChild(t, s, running, f.agent, "c2")
	done := mustStartChild(t, s, root, clerk, "c3")
	finishAs(t, s, done.ID, "succeeded")
	other := createRun(t, s, f)
	claim(t, s, other.ID, "w1")
	theirs := mustStartChild(t, s, other, clerk, "c4")
	age(t, s, root.ID, "heartbeat_at", 3600)
	if _, err := s.TakeOverStaleRuns(t.Context(), 60); err != nil {
		t.Fatalf("TakeOverStaleRuns: %v", err)
	}

	got, err := s.RequestRunCancel(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.Status != "cancelled" {
		t.Fatalf("root is %s, want cancelled", got.Status)
	}
	for _, id := range []uuid.UUID{running.ID, deeper.ID} {
		if r := statusOf(t, s, id); r.Status != "cancelled" || r.FinishedAt == nil {
			t.Errorf("a child left running is %s (finished %v), want cancelled with the root", r.Status, r.FinishedAt)
		}
	}
	for id, want := range map[uuid.UUID]string{done.ID: "succeeded", theirs.ID: "running"} {
		if r := statusOf(t, s, id); r.Status != want {
			t.Errorf("%s is %s, want %s", id, r.Status, want)
		}
	}
}

// A running run's cancel reaches its children through its own context, child
// first; the request itself only flags the run.
func TestCancellingARunningRunLeavesItsChildrenToIt(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	claim(t, s, root.ID, "w1")
	child := mustStartChild(t, s, root, insertAgent(t, s, f.team, "clerk"), "c1")

	if _, err := s.RequestRunCancel(t.Context(), root.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if r := statusOf(t, s, child.ID); r.Status != "running" {
		t.Errorf("child is %s, want still running for its parent to end", r.Status)
	}
}
