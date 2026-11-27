package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// orphan is a child of parent started by call, running under a worker that
// stopped: its heartbeat stale seconds ago, and what it had done in its own
// transcript.
func (f *execFixture) orphan(t *testing.T, parent, agentID uuid.UUID, call string, stale int, did ...agent.Message) db.Run {
	t.Helper()
	id := uuid.New()
	r, err := f.store.StartChildRun(t.Context(), db.StartChildRunParams{
		ID: id, ParentID: parent, AgentID: agentID, TeamID: f.team,
		ParentToolCall: call + "#" + id.String(), Input: "find the bug in send.go", Worker: "w-dead", TokenBudget: maxTokens,
	})
	if err != nil {
		t.Fatalf("StartChildRun: %v", err)
	}
	f.mustExec(t, `UPDATE runs SET heartbeat_at = now() - make_interval(secs => $2) WHERE id = $1`, r.ID, stale)
	if len(did) > 0 {
		if err := f.sessions.Save(t.Context(), r.ID.String(), did); err != nil {
			t.Fatalf("seed the child's transcript: %v", err)
		}
	}
	return r
}

// delegated is a parent's turn that called delegate once per id, left open
// when its worker stopped.
func delegated(ids ...string) agent.Message {
	m := agent.Message{Role: agent.RoleAssistant}
	for _, id := range ids {
		m.ToolCalls = append(m.ToolCalls, agent.ToolCall{ID: id, Name: DelegateToolName, Arguments: []byte(delegateArgs)})
	}
	return m
}

// resultOf is the text the request gave call id's result.
func resultOf(msgs []agent.Message, id string) string {
	for _, m := range msgs {
		if m.Role == agent.RoleTool && m.ToolCallID == id {
			return m.Text()
		}
	}
	return ""
}

func (f *execFixture) get(t *testing.T, id uuid.UUID) db.Run {
	t.Helper()
	r, err := f.store.GetRun(t.Context(), id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return r
}

// agentNamed is the id of the fixture team's agent called name.
func (f *execFixture) agentNamed(t *testing.T, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.raw.QueryRow(t.Context(), `SELECT id FROM agents WHERE team_id = $1 AND name = $2`, f.team, name).Scan(&id); err != nil {
		t.Fatalf("agent %s: %v", name, err)
	}
	return id
}

// A deploy stopped the worker while a child worked: the resumed parent takes
// it up, the child carries on from what it had done rather than starting
// again, and its report is the result of the call that started it.
func TestAResumedParentCarriesOnTheChildItsWorkerLeft(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("the bug is on line 412", 1, 1), answer("carried on", 1, 1)))
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1")})
	child := f.orphan(t, f.run, f.agent, "call_1", 120,
		userSaid("find the bug in send.go"), asked("x1"),
		agent.Message{Role: agent.RoleTool, ToolCallID: "x1", Content: []agent.Content{{Type: agent.ContentText, Text: "send.go read: the lease is never held"}}},
	)

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	got := f.get(t, child.ID)
	wantStatus(t, got, "succeeded", "")
	if deref(got.Output) != "the bug is on line 412" {
		t.Errorf("the child answered %q", deref(got.Output))
	}
	reqs := f.model.requests()
	if len(reqs) != 2 {
		t.Fatalf("the model was asked %d times, want the child's and the parent's", len(reqs))
	}
	if !strings.Contains(textOf(reqs[0].Messages), "send.go read: the lease is never held") {
		t.Errorf("the child started again; its request does not hold what it had done:\n%s", textOf(reqs[0].Messages))
	}
	said := resultOf(reqs[1].Messages, "call_1")
	if !strings.Contains(said, "the bug is on line 412") || !strings.Contains(said, `run="`+child.ID.String()+`"`) || strings.Contains(said, endedMidCall) {
		t.Errorf("the parent's call_1 reads %q; want the child's report", said)
	}
	for _, e := range f.events(t) {
		if e.Kind == "delegated" {
			t.Errorf("the parent delegated again")
		}
	}
}

// Two children a turn started together are carried on together, each
// report the result of its own call.
func TestEachChildCarriedOnAnswersItsOwnCall(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("a report", 1, 1), answer("a report", 1, 1), answer("carried on", 1, 1)))
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1", "call_2")})
	one := f.orphan(t, f.run, f.agent, "call_1", 120, userSaid("one"))
	two := f.orphan(t, f.run, f.agent, "call_2", 120, userSaid("two"))

	f.mustExecute(t)

	reqs := f.model.requests()
	last := reqs[len(reqs)-1].Messages
	for call, child := range map[string]db.Run{"call_1": one, "call_2": two} {
		wantStatus(t, f.get(t, child.ID), "succeeded", "")
		if said := resultOf(last, call); !strings.Contains(said, `run="`+child.ID.String()+`"`) {
			t.Errorf("%s reads %q; want child %s's report", call, said, child.ID)
		}
	}
}

// A child that still beats is another worker's: it is reaped, as before, and
// its call stays closed as ended. So is one whose call is not open, and one
// of a run that was not resumed.
func TestOnlyAChildLeftByItsWorkerOnAnOpenCallIsCarriedOn(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("carried on", 1, 1)))
	f.resumes(t, []agent.Message{
		userSaid(runInput), delegated("call_1"),
		{Role: agent.RoleTool, ToolCallID: "call_1", Content: []agent.Content{{Type: agent.ContentText, Text: "an answer it already had"}}},
		delegated("call_2"),
	})
	beating := f.orphan(t, f.run, f.agent, "call_2", 5)
	answered := f.orphan(t, f.run, f.agent, "call_1", 120)

	f.mustExecute(t)

	for _, id := range []uuid.UUID{beating.ID, answered.ID} {
		wantStatus(t, f.get(t, id), "failed", reapReason)
	}
	reqs := f.model.requests()
	if len(reqs) != 1 {
		t.Fatalf("the model was asked %d times; no child should have run", len(reqs))
	}
	if said := resultOf(reqs[0].Messages, "call_2"); said != endedMidCall {
		t.Errorf("call_2 reads %q, want it closed as ended", said)
	}
}

// A child carried on that needs a person parks, and its parent with it: the
// call's result is the child's waiting report, as when it first asked.
func TestAChildCarriedOnThatAsksParksTheTree(t *testing.T) {
	f, server, _ := treeFixture(t, newScripted(
		callTool("c1", "mail__send", `{"to":"billing@acme.example"}`),
		answer(billingAsks, 10, 5),
		answer(triageAsks, 10, 5),
	))
	billing := f.agentNamed(t, "billing")
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1")})
	child := f.orphan(t, f.run, billing, "call_1", 120, userSaid("Refund the duplicate charge on order 4411."))

	f.mustExecute(t)

	if root := f.row(t); root.Status != "waiting" || deref(root.Output) != triageAsks {
		t.Fatalf("triage is %s saying %q (%s), want waiting in its words", root.Status, deref(root.Output), deref(root.Error))
	}
	if got := f.get(t, child.ID); got.Status != "waiting" || deref(got.Output) != billingAsks {
		t.Fatalf("billing is %s saying %q, want waiting", got.Status, deref(got.Output))
	}
	reqs := f.model.requests()
	if said := resultOf(reqs[len(reqs)-1].Messages, "call_1"); !strings.Contains(said, "billing is waiting for a person's approval") {
		t.Errorf("triage's call_1 reads %q; want billing's waiting report", said)
	}
	if n := server.calls.Load(); n != 0 {
		t.Errorf("made %d time(s) before anyone answered", n)
	}
}

// A grandchild its worker left is carried on by the child that is carried
// on: each level takes up its own.
func TestAGrandchildIsCarriedOnByItsCarriedOnParent(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("the grandchild's", 1, 1), answer("the child's", 1, 1), answer("carried on", 1, 1)))
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1")})
	child := f.orphan(t, f.run, f.agent, "call_1", 120, userSaid("find it"), delegated("call_9"))
	grandchild := f.orphan(t, child.ID, f.agent, "call_9", 120, userSaid("look deeper"))

	f.mustExecute(t)

	for id, said := range map[uuid.UUID]string{grandchild.ID: "the grandchild's", child.ID: "the child's"} {
		got := f.get(t, id)
		wantStatus(t, got, "succeeded", "")
		if deref(got.Output) != said {
			t.Errorf("run %s answered %q, want %q", id, deref(got.Output), said)
		}
	}
}

// AdoptOrphan takes only the parent's own child that is running and stale:
// not one that waits, not one that beats, not a run at the top, not another
// parent's.
func TestAnOrphanIsAdoptedOnlyWhenItsWorkerIsGone(t *testing.T) {
	f := newExecFixture(t, newScripted())
	stale := f.orphan(t, f.run, f.agent, "call_1", 120)
	beating := f.orphan(t, f.run, f.agent, "call_2", 5)
	waiting := f.orphan(t, f.run, f.agent, "call_3", 120)
	f.mustExec(t, `UPDATE runs SET status = 'waiting', worker = NULL, heartbeat_at = NULL, waiting_since = now() WHERE id = $1`, waiting.ID)
	f.mustExec(t, `UPDATE runs SET status = 'running', worker = 'w-dead', heartbeat_at = now() - interval '2 minutes' WHERE id = $1`, f.run)

	listed, err := f.store.ListOrphanedChildren(t.Context(), db.ListOrphanedChildrenParams{ParentID: f.run, StaleSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != stale.ID {
		t.Errorf("listed %d orphans, want only the stale running child", len(listed))
	}
	other := f.orphan(t, stale.ID, f.agent, "call_4", 120) // another parent's
	for id, want := range map[uuid.UUID]bool{stale.ID: true, beating.ID: false, waiting.ID: false, f.run: false, other.ID: false} {
		got, err := f.store.AdoptOrphan(t.Context(), db.AdoptOrphanParams{ID: id, ParentID: f.run, Worker: "w-new", StaleSeconds: 60})
		if (err == nil) != want {
			t.Errorf("AdoptOrphan(%s) = %v, want taken %v", id, err, want)
		}
		if want && (got.Worker == nil || *got.Worker != "w-new" || got.Status != "running") {
			t.Errorf("adopted %+v, want running under w-new", got)
		}
	}
}

// A real result in the last turn is never written over: only a call closed
// as ended is open, and the turn's results end at the first message that is
// not one.
func TestOpenCallsAreTheEndedOnesOfTheLastTurn(t *testing.T) {
	t.Parallel()

	ended := func(id string) agent.Message {
		return agent.Message{Role: agent.RoleTool, ToolCallID: id, Content: []agent.Content{{Type: agent.ContentText, Text: endedMidCall}}}
	}
	real := agent.Message{Role: agent.RoleTool, ToolCallID: "call_1", Content: []agent.Content{{Type: agent.ContentText, Text: "a real answer"}}}
	files := agent.Message{Role: agent.RoleUser, Attached: true}
	for name, c := range map[string]struct {
		msgs []agent.Message
		want map[string]int
	}{
		"one real, one ended":         {[]agent.Message{userSaid("go"), delegated("call_1", "call_2"), real, ended("call_2")}, map[string]int{"call_2": 3}},
		"past a files message":        {[]agent.Message{userSaid("go"), delegated("call_2"), ended("call_2"), files}, map[string]int{"call_2": 2}},
		"an earlier turn's":           {[]agent.Message{userSaid("go"), delegated("call_1"), ended("call_1"), userSaid("then")}, map[string]int{}},
		"an ended call before a real": {[]agent.Message{delegated("call_1"), ended("call_1"), userSaid("later"), delegated("call_2"), real}, map[string]int{}},
	} {
		got := openCalls(c.msgs)
		if len(got) != len(c.want) {
			t.Errorf("%s: open %v, want %v", name, got, c.want)
			continue
		}
		for id, at := range c.want {
			if got[id] != at {
				t.Errorf("%s: %s at %d, want %d", name, id, got[id], at)
			}
		}
	}
}

// A call id with a "#" of its own still finds its child: the run's id follows
// the last.
func TestTheStartingCallIsCutAtTheLastHash(t *testing.T) {
	t.Parallel()

	for stored, want := range map[string]string{
		"call_1#9b2c": "call_1",
		"#E1#9b2c":    "#E1",
		"a#b#9b2c":    "a#b",
		"call_legacy": "call_legacy",
	} {
		if got := startingCall(db.Run{ParentToolCall: &stored}); got != want {
			t.Errorf("startingCall(%q) = %q, want %q", stored, got, want)
		}
	}
	if got := startingCall(db.Run{}); got != "" {
		t.Errorf("no parent call gave %q", got)
	}
}

// failingGet is the store with one run's read failing, as a child that
// could not be read back after it ran.
type failingGet struct {
	Store
	id uuid.UUID
}

func (s failingGet) GetRun(ctx context.Context, id uuid.UUID) (db.Run, error) {
	if id == s.id {
		return db.Run{}, errors.New("connection reset")
	}
	return s.Store.GetRun(ctx, id)
}

// A child that cannot be carried on costs the run only its report: its call
// stays ended and it is reaped, while its sibling's report is kept and the
// run goes on.
func TestAChildThatFailsToCarryOnLeavesItsSiblingsReport(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("a report", 1, 1), answer("a report", 1, 1), answer("carried on", 1, 1)))
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1", "call_2")})
	one := f.orphan(t, f.run, f.agent, "call_1", 120, userSaid("one"))
	two := f.orphan(t, f.run, f.agent, "call_2", 120, userSaid("two"))
	f.exec.store = failingGet{Store: f.exec.store, id: two.ID}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	reqs := f.model.requests()
	last := reqs[len(reqs)-1].Messages
	if said := resultOf(last, "call_1"); !strings.Contains(said, `run="`+one.ID.String()+`"`) {
		t.Errorf("call_1 reads %q; want its child's report", said)
	}
	if said := resultOf(last, "call_2"); said != endedMidCall {
		t.Errorf("call_2 reads %q; want it still ended", said)
	}
}

// failingReap is the store with the reap failing, as a run that stops
// between carrying its children on and its next call.
type failingReap struct {
	Store
	parent uuid.UUID
}

func (s failingReap) ReapChildren(ctx context.Context, arg db.ReapChildrenParams) ([]uuid.UUID, error) {
	if arg.ParentID == s.parent {
		return nil, errors.New("connection reset")
	}
	return s.Store.ReapChildren(ctx, arg)
}

// The reports are kept as soon as they are written: a run that stops before
// its next call still finds them in its transcript, as its children, carried
// on to their end, are never listed again.
func TestACarriedOnReportIsKeptBeforeTheRunsNextCall(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("the child's report", 1, 1)))
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1")})
	child := f.orphan(t, f.run, f.agent, "call_1", 120, userSaid("find it"))
	f.exec.store = failingReap{Store: f.exec.store, parent: f.run}

	_ = f.execute(t, t.Context())

	if n := len(f.model.requests()); n != 1 {
		t.Fatalf("the model was asked %d times, want the child's alone", n)
	}
	stored, err := f.sessions.Messages(t.Context(), transcriptOf(f.row(t)))
	if err != nil {
		t.Fatal(err)
	}
	if said := resultOf(stored, "call_1"); !strings.Contains(said, `run="`+child.ID.String()+`"`) {
		t.Errorf("the stored call_1 reads %q; want the child's report kept", said)
	}
}

// lostGet is the store reading one run back as still running, as a child
// another worker took while this one carried it on.
type lostGet struct {
	Store
	id uuid.UUID
}

func (s lostGet) GetRun(ctx context.Context, id uuid.UUID) (db.Run, error) {
	r, err := s.Store.GetRun(ctx, id)
	if id == s.id {
		r.Status = "running"
	}
	return r, err
}

// A child lost to another worker reports there, not here: its call stays
// ended, never an empty report.
func TestALostChildLeavesItsCallEnded(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("a report", 1, 1), answer("carried on", 1, 1)))
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1")})
	child := f.orphan(t, f.run, f.agent, "call_1", 120, userSaid("find it"))
	f.exec.store = lostGet{Store: f.exec.store, id: child.ID}

	f.mustExecute(t)

	reqs := f.model.requests()
	if said := resultOf(reqs[len(reqs)-1].Messages, "call_1"); said != endedMidCall {
		t.Errorf("call_1 reads %q; want it still ended", said)
	}
}

// A child that fails as it carries on reports its failure, which is its
// call's result: the run reads what became of the work.
func TestAChildThatFailsReportsItsFailure(t *testing.T) {
	f := newExecFixture(t, newScripted(turn{err: errors.New("the model is down")}, answer("carried on", 1, 1)))
	f.resumes(t, []agent.Message{userSaid(runInput), delegated("call_1")})
	child := f.orphan(t, f.run, f.agent, "call_1", 120, userSaid("find it"))

	f.mustExecute(t)

	if got := f.get(t, child.ID); got.Status != "failed" {
		t.Errorf("the child is %s, want failed", got.Status)
	}
	reqs := f.model.requests()
	if said := resultOf(reqs[len(reqs)-1].Messages, "call_1"); !strings.Contains(said, `status="failed"`) {
		t.Errorf("call_1 reads %q; want the child's failed report", said)
	}
}
