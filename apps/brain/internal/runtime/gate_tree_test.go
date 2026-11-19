package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// treeFixture is gatedFixture's triage leading billing, and billing, not
// triage, holding mail__send: the call that asks is a delegate's.
func treeFixture(t *testing.T, model *scripted) (*execFixture, *mcpServer, uuid.UUID) {
	t.Helper()

	f, server, sam := gatedFixture(t, model)
	billing := f.teamLead(t)
	f.mustExec(t, `DELETE FROM agent_mcp_servers WHERE agent_id = $1`, f.agent)
	f.mustExec(t, `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id)
		SELECT $1, id, team_id FROM mcp_servers WHERE team_id = $2 AND name = 'mail'`, billing, f.team)
	return f, server, sam
}

const (
	billingAsks = "I need a person's OK to send the refund notice to billing@acme.example."
	triageAsks  = "Billing has the refund notice ready; can you OK sending it?"
)

// The opening every tree test shares: triage hands billing the refund,
// billing's mail__send asks, billing says so to triage, and triage to the
// person.
func asksInTheTree(rest ...turn) *scripted {
	return newScripted(append([]turn{
		callTool("call_1", "delegate", delegateArgs),
		callTool("c1", "mail__send", `{"to":"billing@acme.example"}`),
		answer(billingAsks, 10, 5),
		answer(triageAsks, 10, 5),
	}, rest...)...)
}

func (f *execFixture) onlyChild(t *testing.T) db.Run {
	t.Helper()
	children := f.children(t, f.run)
	if len(children) != 1 {
		t.Fatalf("%d children, want one", len(children))
	}
	return children[0]
}

// A delegate's call that needs a person parks the whole tree, each run having
// said so in its own words: billing to triage, as its report, and triage to
// the person. None holds a worker. The yes queues the root; the resumed root
// takes up the same child rather than starting another, which makes the
// approved call once and reports what came of it.
func TestADelegatesAskParksTheTreeAndTheYesCarriesItOn(t *testing.T) {
	f, server, sam := treeFixture(t, asksInTheTree(
		answer("Sent: the refund notice.", 10, 5),
		answer("Billing sent it.", 10, 5),
	))

	f.mustExecute(t)

	root, child := f.row(t), f.onlyChild(t)
	for r, said := range map[*db.Run]string{&root: triageAsks, &child: billingAsks} {
		if r.Status != "waiting" || r.Worker != nil || r.WaitingSince == nil || deref(r.Output) != said {
			t.Fatalf("run %s is %s, worker %v, waiting since %v, said %q; want waiting, held by nobody, saying %q",
				r.AgentName, r.Status, r.Worker, r.WaitingSince, deref(r.Output), said)
		}
	}
	if said := textOf(f.model.requests()[3].Messages); !strings.Contains(said, "billing is waiting for a person's approval") ||
		!strings.Contains(said, billingAsks) {
		t.Fatalf("triage was not told billing waits, in billing's words:\n%s", said)
	}
	if n := server.calls.Load(); n != 0 {
		t.Fatalf("made %d time(s) before anyone answered", n)
	}
	req := f.requestOf(t, child.ID)
	if req.RootRunID != f.run || req.RequestedFor == nil || *req.RequestedFor != sam {
		t.Fatalf("request %+v, want billing's, under triage, for sam", req)
	}

	r := &resumes{}
	if _, err := Decide(t.Context(), f.store, r, Answer{
		ApprovalID: req.ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if want := []string{f.run.String() + ":1"}; !slices.Equal(r.got, want) {
		t.Fatalf("resumed %v, want the root: %v", r.got, want)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	again := f.onlyChild(t)
	wantStatus(t, again, "succeeded", "")
	if again.ID != child.ID {
		t.Errorf("the resume started billing anew (%s), want %s taken up", again.ID, child.ID)
	}
	if n := server.calls.Load(); n != 1 {
		t.Errorf("made %d time(s), want once", n)
	}
	if got := kinds(f.eventsOf(t, child.ID)); !slices.Contains(got, "waiting") || !slices.Contains(got, "resumed") {
		t.Errorf("billing's events = %v, want it waiting and resumed", got)
	}
	delegated := 0
	for _, e := range f.events(t) {
		if e.Kind == "delegated" {
			delegated++
		}
	}
	if delegated != 1 {
		t.Errorf("%d delegated events on triage, want one", delegated)
	}
	calls := f.model.requests()
	if len(calls) != 6 {
		t.Fatalf("the model was asked %d times, want six: triage's call, billing's, both their words, billing's answer, triage's", len(calls))
	}
	if said := textOf(calls[4].Messages); !strings.Contains(said, "A person approved it, and it was made.") {
		t.Errorf("billing was not told the call was made:\n%s", said)
	}
	if said := textOf(calls[5].Messages); !strings.Contains(said, "Sent: the refund notice.") {
		t.Errorf("triage did not read billing's report:\n%s", said)
	}
}

// A child that carries on and needs a person again waits again, and its
// parent hears it in its words and waits with it: one yes is one call.
func TestAChildThatAsksAgainOnTheResumeWaitsAgain(t *testing.T) {
	f, server, sam := treeFixture(t, asksInTheTree(
		callTool("c2", "mail__send", `{"to":"accounts@acme.example"}`),
		answer("Now I need an OK to copy accounts in.", 10, 5),
		answer("Billing needs one more OK, to copy accounts in.", 10, 5),
	))
	f.mustExecute(t)
	child := f.onlyChild(t)
	if _, err := Decide(t.Context(), f.store, &resumes{}, Answer{
		ApprovalID: f.requestOf(t, child.ID).ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	f.mustExecute(t)

	if root := f.row(t); root.Status != "waiting" || deref(root.Output) != "Billing needs one more OK, to copy accounts in." {
		t.Fatalf("triage is %s saying %q, want waiting again in its words", root.Status, deref(root.Output))
	}
	if got := f.onlyChild(t); got.Status != "waiting" {
		t.Fatalf("billing is %s, want waiting again", got.Status)
	}
	if n := server.calls.Load(); n != 1 {
		t.Errorf("made %d time(s), want once: the second call waits", n)
	}
	calls := f.model.requests()
	last := calls[len(calls)-1].Messages
	if said := last[len(last)-1].Text(); !strings.Contains(said, "billing is waiting for a person's approval") ||
		!strings.Contains(said, "Now I need an OK to copy accounts in.") {
		t.Errorf("what triage was told on the resume does not say billing waits again:\n%s", said)
	}
	if asked, _ := f.store.ListRunApprovals(t.Context(), child.ID); len(asked) != 2 {
		t.Errorf("%d requests, want the second as its own", len(asked))
	}
}

// A parent that ends while a child waits takes the child with it: nothing
// is left waiting under a run that will never resume.
func TestAParentThatEndsTakesItsWaitingChildWithIt(t *testing.T) {
	f := newExecFixture(t, newScripted())
	d := f.delegation(t, "call_7")
	child, err := f.store.StartChildRun(t.Context(), db.StartChildRunParams{
		ID: uuid.New(), ParentID: f.run, AgentID: d.Agent.ID, TeamID: f.team,
		ParentToolCall: "call_7#x", Input: childBrief, Worker: worker, TokenBudget: maxTokens,
	})
	if err != nil {
		t.Fatalf("StartChildRun: %v", err)
	}
	f.mustExec(t, `UPDATE runs SET status = 'waiting', worker = NULL, heartbeat_at = NULL, waiting_since = now() WHERE id = $1`, child.ID)

	if err := f.exec.finish(t.Context(), f.run, worker, "failed", "", "it broke"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	wantStatus(t, f.onlyChild(t), "failed", "its parent ended")
}

// The person writes again instead of answering: their request ends, the
// tree resumes, billing is told to stop and report, and triage reads what
// they wrote. Nothing is sent.
func TestWritingAgainInsteadOfAnsweringStopsTheDelegateAndReachesTheTop(t *testing.T) {
	f, server, sam := treeFixture(t, asksInTheTree(
		answer("I stopped before sending anything.", 10, 5),
		answer("Then to Bob.", 10, 5),
	))
	f.mustExecute(t)
	child := f.onlyChild(t)

	var resumed *db.RequeueAnsweredRow
	if err := f.store.InTx(t.Context(), func(q *db.Queries) error {
		if _, err := q.LockRunSession(t.Context(), f.run); err != nil {
			return err
		}
		if err := q.AddSteer(t.Context(), db.AddSteerParams{RunID: f.run, Text: "wait, send it to Bob instead"}); err != nil {
			return err
		}
		var err error
		resumed, err = gateway.Supersede(t.Context(), q, f.run, sam, "channel")
		return err
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if resumed == nil || resumed.ID != f.run || resumed.Attempt != 1 {
		t.Fatalf("resumed %+v, want the root at its next attempt", resumed)
	}
	if req := f.requestOf(t, child.ID); req.Status != "cancelled" || req.DecidedBy == nil || *req.DecidedBy != sam {
		t.Fatalf("request %s by %v, want cancelled by sam", req.Status, req.DecidedBy)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := server.calls.Load(); n != 0 {
		t.Errorf("a superseded call was made %d time(s)", n)
	}
	calls := f.model.requests()
	if said := textOf(calls[4].Messages); !strings.Contains(said, supersededDeep) {
		t.Errorf("billing was not told to stop and report:\n%s", said)
	}
	if said := textOf(calls[5].Messages); !strings.Contains(said, "send it to Bob instead") ||
		!strings.Contains(said, "I stopped before sending anything.") {
		t.Errorf("triage did not read both billing's report and the person's words:\n%s", said)
	}
}

// Cancelling a waiting tree ends it whole, with what it asked: a yes after
// that changes nothing and resumes nothing.
func TestCancellingAWaitingTreeEndsWhatItAsked(t *testing.T) {
	f, server, sam := treeFixture(t, asksInTheTree())
	f.mustExecute(t)
	child := f.onlyChild(t)

	if _, err := f.store.RequestRunCancel(t.Context(), f.run); err != nil {
		t.Fatalf("RequestRunCancel: %v", err)
	}
	wantStatus(t, f.row(t), "cancelled", "")
	wantStatus(t, f.onlyChild(t), "cancelled", "")
	req := f.requestOf(t, child.ID)
	if req.Status != "cancelled" {
		t.Fatalf("request is %s, want cancelled", req.Status)
	}
	if got := kinds(f.eventsOf(t, child.ID)); !slices.Contains(got, "approval_decided") {
		t.Errorf("billing's events = %v, want the request's end told", got)
	}

	r := &resumes{}
	if _, err := Decide(t.Context(), f.store, r, Answer{
		ApprovalID: req.ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true,
	}); !errors.Is(err, ErrNotPending) || len(r.got) != 0 {
		t.Errorf("a yes after the cancel: %v, resumed %v; want ErrNotPending and nothing", err, r.got)
	}
	if n := server.calls.Load(); n != 0 {
		t.Errorf("made %d time(s)", n)
	}
}

// dueNow is the store with one request listed as due whatever its time, as a
// brain whose clock is behind the database's sees it.
type dueNow struct {
	*store.Store
	listed db.Approval
}

func (d dueNow) ListDueApprovals(context.Context, int32) ([]db.ListDueApprovalsRow, error) {
	return []db.ListDueApprovalsRow{{ID: d.listed.ID, TeamID: d.listed.TeamID}}, nil
}

// Nobody answered in time: the sweep expires the request and resumes the
// tree, and billing is told nobody answered. One the sweep's own clock does
// not see as due is left as it was.
func TestTheSweepExpiresAnUnansweredRequestAndResumesTheTree(t *testing.T) {
	f, server, _ := treeFixture(t, asksInTheTree(
		answer("Nobody answered; nothing was sent.", 10, 5),
		answer("Billing could not send it.", 10, 5),
	))
	f.mustExecute(t)
	req := f.requestOf(t, f.onlyChild(t).ID)

	early := RunJob(f.exec, dueNow{Store: f.store, listed: req}, quietLogger())
	early.resume = func(uuid.UUID, int32) error { t.Error("a request not yet due resumed its run"); return nil }
	if failed := early.expire(t.Context()); len(failed) != 0 {
		t.Fatalf("expire: %v", failed)
	}
	if got := f.requestOf(t, req.RunID); got.Status != "pending" {
		t.Fatalf("a request not yet due is %s", got.Status)
	}

	f.mustExec(t, `UPDATE approvals SET expires_at = now() - interval '1 minute' WHERE id = $1`, req.ID)
	j := RunJob(f.exec, f.store, quietLogger())
	r := &resumes{}
	j.resume = func(id uuid.UUID, attempt int32) error { return r.Resume(t.Context(), id, attempt) }
	if failed := j.expire(t.Context()); len(failed) != 0 {
		t.Fatalf("expire: %v", failed)
	}
	if want := []string{f.run.String() + ":1"}; !slices.Equal(r.got, want) {
		t.Fatalf("resumed %v, want %v", r.got, want)
	}
	if got := f.requestOf(t, req.RunID); got.Status != "expired" || deref(got.DecidedVia) != "sweep" {
		t.Fatalf("request is %s via %v, want expired by the sweep", got.Status, got.DecidedVia)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := server.calls.Load(); n != 0 {
		t.Errorf("an expired call was made %d time(s)", n)
	}
	calls := f.model.requests()
	if said := textOf(calls[4].Messages); !strings.Contains(said, expiredCall) {
		t.Errorf("billing was not told nobody answered:\n%s", said)
	}
}

func (f *execFixture) requestOf(t *testing.T, run uuid.UUID) db.Approval {
	t.Helper()
	got, err := f.store.ListRunApprovals(t.Context(), run)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListRunApprovals(%s) = %v, %v; want one request", run, got, err)
	}
	return got[0]
}
