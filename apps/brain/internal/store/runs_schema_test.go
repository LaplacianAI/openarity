package store

import (
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

// run is one row for insertRun. The zero value of every optional field is the
// root run a person started: queued, its own root, at depth 0.
type run struct {
	id, team, session uuid.UUID
	root              uuid.UUID // zero means id
	parent            *uuid.UUID
	toolCall          *string
	depth             int
	agent             *uuid.UUID
	user              *uuid.UUID
	status            string // empty means queued
	worker            *string
	heartbeat         bool
	finished          bool
	err               *string
}

func insertRun(t *testing.T, s *Store, r run) error {
	t.Helper()

	if r.root == uuid.Nil {
		r.root = r.id
	}
	if r.status == "" {
		r.status = "queued"
	}

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, parent_run_id,
		                  parent_tool_call, depth, agent_id, agent_name, user_id,
		                  status, input, worker, heartbeat_at, finished_at, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'triage', $9, $10, 'do the thing', $11,
		        CASE WHEN $12 THEN now() END, CASE WHEN $13 THEN now() END, $14)`,
		r.id, r.team, r.session, r.root, r.parent, r.toolCall, r.depth, r.agent, r.user,
		r.status, r.worker, r.heartbeat, r.finished, r.err)
	return err
}

func mustInsertRun(t *testing.T, s *Store, r run) uuid.UUID {
	t.Helper()

	if err := insertRun(t, s, r); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return r.id
}

func insertSession(t *testing.T, s *Store, teamID uuid.UUID) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`,
		teamID).Scan(&id); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

// runFixture is a team with a session and an agent, which is what every run needs.
type runFixture struct {
	team, session, agent uuid.UUID
}

func newRunFixture(t *testing.T, s *Store, team string) runFixture {
	t.Helper()

	id := mustCreate(t, s, team).ID
	return runFixture{team: id, session: insertSession(t, s, id), agent: insertAgent(t, s, id, "triage")}
}

func (f runFixture) root() run {
	return run{id: uuid.New(), team: f.team, session: f.session, agent: &f.agent}
}

// child is a delegate of parent, shaped as runs_root_shape requires.
func (f runFixture) child(parent uuid.UUID, call string) run {
	r := f.root()
	r.root, r.parent, r.toolCall, r.depth = parent, &parent, &call, 1
	return r
}

func saveTranscript(t *testing.T, s *Store, runID uuid.UUID) {
	t.Helper()

	sessions := postgres.New(s.pool)
	msgs := []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "hi"}}}}
	if err := sessions.Save(t.Context(), runID.String(), msgs); err != nil {
		t.Fatalf("save transcript: %v", err)
	}
	if err := sessions.AddSteer(t.Context(), runID.String(), "and then"); err != nil {
		t.Fatalf("add steer: %v", err)
	}
}

func transcriptRows(t *testing.T, s *Store, runID uuid.UUID) int {
	t.Helper()

	return count(t, s, `SELECT count(*) FROM agent_messages WHERE session = $1`, runID.String()) +
		count(t, s, `SELECT count(*) FROM agent_steers WHERE session = $1`, runID.String())
}

// --- the shape of a run ---

// root_run_id, depth and parent_tool_call each say "this run has a parent" in
// their own way. One that disagrees with parent_run_id is a tree no query can
// walk correctly, so every disagreement is refused, one at a time.
func TestARootRunIsItsOwnRootAtDepthZero(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	parent := mustInsertRun(t, s, f.root())

	notOwnRoot := f.root()
	notOwnRoot.root = parent

	deep := f.root()
	deep.depth = 1

	withCall := f.root()
	withCall.toolCall = ptr("call_1")

	childAtRoot := f.child(parent, "call_2")
	childAtRoot.root = childAtRoot.id

	childAtZero := f.child(parent, "call_3")
	childAtZero.depth = 0

	childWithoutCall := f.child(parent, "call_4")
	childWithoutCall.toolCall = nil

	for name, r := range map[string]run{
		"a root that names another root":  notOwnRoot,
		"a root below depth zero":         deep,
		"a root made by a tool call":      withCall,
		"a delegate that is its own root": childAtRoot,
		"a delegate at depth zero":        childAtZero,
		"a delegate made by no tool call": childWithoutCall,
	} {
		t.Run(name, func(t *testing.T) {
			wantConstraint(t, insertRun(t, s, r), "23514", "runs_root_shape")
		})
	}

	if err := insertRun(t, s, f.child(parent, "call_5")); err != nil {
		t.Errorf("a well-formed delegate was refused: %v", err)
	}
}

// The composite key is the only thing stopping a run being filed in another
// team's conversation, where that team's members would read it.
func TestARunCannotNameASessionInAnotherTeam(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")

	r := f.root()
	r.session = other.session
	wantConstraint(t, insertRun(t, s, r), "23503", "runs_session_in_team")
}

// The worker builds a run from its agent: that agent's instructions, its
// grants and its servers' tokens. Another team's agent would run inside this
// team's run, so the database refuses the pair rather than trusting the insert.
func TestARunCannotUseAnotherTeamsAgent(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")

	r := f.root()
	r.agent = &other.agent
	wantConstraint(t, insertRun(t, s, r), "23503", "runs_agent_in_team")
}

// Delegation writes children. A child filed under another team's parent would
// show up in that team's tree and be cancelled by its cancels.
func TestADelegateCannotHangUnderAnotherTeamsRun(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")
	theirs := mustInsertRun(t, s, other.root())

	r := f.child(theirs, "call_1")
	wantConstraint(t, insertRun(t, s, r), "23503", "runs_parent_in_team")
}

// root_run_id is how "every run in this tree" is asked. A root in another
// team would pull this run into that team's answer.
func TestARunCannotNameARootInAnotherTeam(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")
	theirs := mustInsertRun(t, s, other.root())
	ours := mustInsertRun(t, s, f.root())

	// A delegate of our own run whose root claims to be theirs: the parent
	// is in the team, so only the root's key can refuse it.
	r := f.child(ours, "call_1")
	r.root = theirs
	wantConstraint(t, insertRun(t, s, r), "23503", "runs_root_in_team")
}

// Deleting a root takes its whole tree, including a grandchild whose parent
// is not the root. In a consistent tree either key's cascade does this alone,
// so this fails only when neither cascades; the keys' team checks have their
// own tests above.
func TestDeletingARootDeletesItsWholeTree(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := mustInsertRun(t, s, f.root())
	child := mustInsertRun(t, s, f.child(root, "call_1"))

	grandchild := f.child(child, "call_2")
	grandchild.root, grandchild.depth = root, 2
	mustInsertRun(t, s, grandchild)
	kept := mustInsertRun(t, s, f.root())

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM runs WHERE id = $1`, root); err != nil {
		t.Fatalf("delete root: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM runs WHERE root_run_id = $1`, root); n != 0 {
		t.Errorf("%d runs of the tree outlived its root", n)
	}
	if n := count(t, s, `SELECT count(*) FROM runs WHERE id = $1`, kept); n != 1 {
		t.Error("deleting one tree took another run with it")
	}
}

// A running row with no worker or no heartbeat can never be judged stale, so
// the sweep would never take it over and it would run forever on paper.
func TestARunningRunNeedsAWorkerAndAHeartbeat(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")

	noWorker := f.root()
	noWorker.status, noWorker.heartbeat = "running", true
	wantConstraint(t, insertRun(t, s, noWorker), "23514", "runs_held_while_running")

	noHeartbeat := f.root()
	noHeartbeat.status, noHeartbeat.worker = "running", ptr("w1")
	wantConstraint(t, insertRun(t, s, noHeartbeat), "23514", "runs_held_while_running")

	held := f.root()
	held.status, held.worker, held.heartbeat = "running", ptr("w1"), true
	if err := insertRun(t, s, held); err != nil {
		t.Errorf("a held running run was refused: %v", err)
	}
}

// finished_at is what tells a reader the run is over. A finished run without
// one, or a queued run with one, makes "how long did it take" a lie.
func TestOnlyAFinishedRunHasAFinishTime(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")

	for _, status := range []string{"succeeded", "cancelled"} {
		r := f.root()
		r.status = status
		wantConstraint(t, insertRun(t, s, r), "23514", "runs_finished_when_terminal")
	}

	queued := f.root()
	queued.finished = true
	wantConstraint(t, insertRun(t, s, queued), "23514", "runs_finished_when_terminal")

	done := f.root()
	done.status, done.finished = "succeeded", true
	if err := insertRun(t, s, done); err != nil {
		t.Errorf("a finished run with a finish time was refused: %v", err)
	}
}

// A failed run with no error is a failure nobody can explain; an error on a
// run that succeeded is one somebody will chase for nothing.
func TestOnlyAFailedRunHasAnError(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")

	silent := f.root()
	silent.status, silent.finished = "failed", true
	wantConstraint(t, insertRun(t, s, silent), "23514", "runs_error_when_failed")

	noisy := f.root()
	noisy.status, noisy.finished, noisy.err = "succeeded", true, ptr("boom")
	wantConstraint(t, insertRun(t, s, noisy), "23514", "runs_error_when_failed")

	failed := f.root()
	failed.status, failed.finished, failed.err = "failed", true, ptr("the model endpoint refused")
	if err := insertRun(t, s, failed); err != nil {
		t.Errorf("a failed run with an error was refused: %v", err)
	}
}

func TestAnUnknownRunStatusIsRefused(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")

	r := f.root()
	r.status = "paused"
	wantConstraint(t, insertRun(t, s, r), "23514", "runs_status_known")
}

// A resumed parent that repeats a delegate call must find the child it already
// made. Two children for one call would run the same brief twice.
func TestAParentCannotMakeTwoChildrenFromOneToolCall(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	parent := mustInsertRun(t, s, f.root())

	mustInsertRun(t, s, f.child(parent, "call_1"))
	wantConstraint(t, insertRun(t, s, f.child(parent, "call_1")), "23505", "runs_parent_tool_call_key")

	other := mustInsertRun(t, s, f.root())
	if err := insertRun(t, s, f.child(other, "call_1")); err != nil {
		t.Errorf("another parent's call_1 was refused: %v", err)
	}
}

// --- what outlives what ---

// The run is a record of what happened. Deleting the agent must not delete
// the record, and must not leave it unreadable.
func TestDeletingTheAgentKeepsTheRunAndItsName(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	id := mustInsertRun(t, s, f.root())

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1`, f.agent); err != nil {
		t.Fatalf("delete agent: %v", err)
	}

	var agentID *uuid.UUID
	var name string
	if err := s.pool.QueryRow(t.Context(),
		`SELECT agent_id, agent_name FROM runs WHERE id = $1`, id).Scan(&agentID, &name); err != nil {
		t.Fatalf("the run went with its agent: %v", err)
	}
	if agentID != nil {
		t.Errorf("agent_id = %s, want null once the agent is gone", agentID)
	}
	if name != "triage" {
		t.Errorf("agent_name = %q, want the name it ran under", name)
	}
}

func TestDeletingAUserKeepsTheRunsTheyAskedFor(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	user := insertUser(t, s, "https://idp.example.com", "alice")

	r := f.root()
	r.user = &user
	id := mustInsertRun(t, s, r)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM runs WHERE id = $1 AND user_id IS NULL`, id); n != 1 {
		t.Error("the run did not survive its user with user_id cleared")
	}
}

// A transcript is keyed by a string, so no foreign key removes it. Without the
// trigger a deleted run leaves the whole conversation behind.
func TestDeletingARunForgetsItsTranscript(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	gone := mustInsertRun(t, s, f.root())
	kept := mustInsertRun(t, s, f.root())
	saveTranscript(t, s, gone)
	saveTranscript(t, s, kept)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM runs WHERE id = $1`, gone); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	if n := transcriptRows(t, s, gone); n != 0 {
		t.Errorf("%d transcript rows outlived their run", n)
	}
	if n := transcriptRows(t, s, kept); n != 2 {
		t.Errorf("another run's transcript has %d rows, want its 2 untouched", n)
	}
}

// A delegate is deleted by the parent's cascade, which is a statement of its
// own: the trigger has to fire there too, or every delegate's conversation
// outlives the tree.
func TestDeletingARunForgetsItsDelegatesTranscripts(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	parent := mustInsertRun(t, s, f.root())
	child := mustInsertRun(t, s, f.child(parent, "call_1"))
	saveTranscript(t, s, child)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM runs WHERE id = $1`, parent); err != nil {
		t.Fatalf("delete parent: %v", err)
	}

	if n := count(t, s, `SELECT count(*) FROM runs WHERE id = $1`, child); n != 0 {
		t.Error("the delegate outlived its parent")
	}
	if n := transcriptRows(t, s, child); n != 0 {
		t.Errorf("%d of the delegate's transcript rows outlived it", n)
	}
}

// Deleting a team is the commonest reason a conversation must go, and it
// reaches runs through two cascades without any Go code seeing a row.
func TestDeletingATeamForgetsEveryTranscriptInIt(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	other := newRunFixture(t, s, "billing")
	gone := mustInsertRun(t, s, f.root())
	kept := mustInsertRun(t, s, other.root())
	saveTranscript(t, s, gone)
	saveTranscript(t, s, kept)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, f.team); err != nil {
		t.Fatalf("delete team: %v", err)
	}

	if n := transcriptRows(t, s, gone); n != 0 {
		t.Errorf("%d transcript rows outlived their team", n)
	}
	if n := transcriptRows(t, s, kept); n != 2 {
		t.Errorf("another team's transcript has %d rows, want its 2 untouched", n)
	}
}

// The tables are a copy of the SDK's schema rather than created by it, so this
// is what proves the copy is right: the SDK's own conformance suite, against
// the migrated tables.
func TestTheTranscriptTablesPassTheSDKsOwnStoreSuite(t *testing.T) {
	s := queryStore(t)

	storetest.Run(t, func(t *testing.T) agent.Store {
		// One schema for every subtest, so each starts by emptying it: the
		// suite reuses session ids and requires a store with nothing in it.
		if _, err := s.pool.Exec(t.Context(), `TRUNCATE agent_messages, agent_steers`); err != nil {
			t.Fatalf("empty the transcript tables: %v", err)
		}
		return postgres.New(s.pool)
	})
}

// --- events ---

func insertEvent(t *testing.T, s *Store, runID uuid.UUID, seq int, kind, payload string) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO run_events (run_id, seq, kind, payload) VALUES ($1, $2, $3, $4)`,
		runID, seq, kind, payload)
	return err
}

func TestARunEventMustBeOneTheRuntimeWrites(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	id := mustInsertRun(t, s, f.root())

	wantConstraint(t, insertEvent(t, s, id, 1, "text", `{}`), "23514", "run_events_kind_known")
	wantConstraint(t, insertEvent(t, s, id, 0, "step", `{}`), "23514", "run_events_seq_positive")
	wantConstraint(t, insertEvent(t, s, id, 1, "step", `[]`), "23514", "run_events_payload_object")

	if err := insertEvent(t, s, id, 1, "step", `{"step": 1}`); err != nil {
		t.Errorf("a well-formed event was refused: %v", err)
	}
	wantConstraint(t, insertEvent(t, s, id, 1, "usage", `{}`), "23505", "run_events_pkey")
}

func TestDeletingARunDeletesItsEvents(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	id := mustInsertRun(t, s, f.root())
	if err := insertEvent(t, s, id, 1, "started", `{}`); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM runs WHERE id = $1`, id); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM run_events`); n != 0 {
		t.Errorf("%d events outlived their run", n)
	}
}
