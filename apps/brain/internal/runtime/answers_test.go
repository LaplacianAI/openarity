package runtime

import (
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

// AdoptOrphan takes only a child that is running and stale: not one that
// waits, not one that beats, not a run at the top.
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
	for id, want := range map[uuid.UUID]bool{stale.ID: true, beating.ID: false, waiting.ID: false, f.run: false} {
		got, err := f.store.AdoptOrphan(t.Context(), db.AdoptOrphanParams{ID: id, Worker: "w-new", StaleSeconds: 60})
		if (err == nil) != want {
			t.Errorf("AdoptOrphan(%s) = %v, want taken %v", id, err, want)
		}
		if want && (got.Worker == nil || *got.Worker != "w-new" || got.Status != "running") {
			t.Errorf("adopted %+v, want running under w-new", got)
		}
	}
}
