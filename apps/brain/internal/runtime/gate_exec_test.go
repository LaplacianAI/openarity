package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// resumes records what a decision handed back to the workers.
type resumes struct {
	mu  sync.Mutex
	got []string
}

func (r *resumes) Resume(_ context.Context, id uuid.UUID, attempt int32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, workflowID(id, attempt))
	return nil
}

// gatedFixture is a run of triage, for a member, holding mail__send from an
// untrusted server, in a team that asks before writes.
func gatedFixture(t *testing.T, model *scripted) (*execFixture, *mcpServer, uuid.UUID) {
	t.Helper()

	f := newExecFixture(t, model)
	f.exec.builder.WithApprovals(f.store)
	server := serve(t, "send")
	var id uuid.UUID
	if err := f.raw.QueryRow(t.Context(),
		`INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'mail', $2) RETURNING id`,
		f.team, server.url).Scan(&id); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	f.mustExec(t, `INSERT INTO mcp_tools (mcp_server_id, team_id, name) VALUES ($1, $2, 'send')`, id, f.team)
	f.mustExec(t, `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id) VALUES ($1, $2, $3)`, f.agent, id, f.team)

	var user uuid.UUID
	if err := f.raw.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('dev', 'sam') RETURNING id`).Scan(&user); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	f.mustExec(t, `INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`, f.team, user)
	f.mustExec(t, `UPDATE runs SET user_id = $2 WHERE id = $1`, f.run, user)
	if _, err := f.store.SetTeamApproval(t.Context(), db.SetTeamApprovalParams{
		TeamID: f.team, ApprovalMode: "writes", ApprovalTtlMinutes: 60,
	}); err != nil {
		t.Fatalf("SetTeamApproval: %v", err)
	}
	return f, server, user
}

func (f *execFixture) request(t *testing.T) db.Approval {
	t.Helper()
	got, err := f.store.ListRunApprovals(t.Context(), f.run)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListRunApprovals = %v, %v; want one request", got, err)
	}
	return got[0]
}

// The whole of it: a call that needs a person parks the run without making
// it; the person's yes queues the run again as its next attempt; the resumed
// run makes the call once, with the arguments that were approved, and
// finishes.
func TestACallWaitsForAPersonAndIsMadeOnceTheySayYes(t *testing.T) {
	f, server, sam := gatedFixture(t, newScripted(
		callTool("c1", "mail__send", `{"to":"billing@acme.example"}`),
		answer("Sent.", 10, 5),
	))

	f.mustExecute(t)

	parked := f.row(t)
	if parked.Status != "waiting" || parked.Worker != nil || parked.WaitingSince == nil {
		t.Fatalf("after asking the run is %s, worker %v, waiting since %v; want waiting, held by nobody",
			parked.Status, parked.Worker, parked.WaitingSince)
	}
	if n := server.calls.Load(); n != 0 {
		t.Fatalf("the tool was made %d time(s) before anyone answered", n)
	}
	req := f.request(t)
	if req.Status != "pending" || req.CallID != "c1" || req.Tool != "mail__send" || req.Summary != "to: billing@acme.example" {
		t.Fatalf("request = %+v", req)
	}
	if got := kinds(f.events(t)); !slices.Contains(got, "approval_requested") || got[len(got)-1] != "waiting" {
		t.Fatalf("events = %v, want approval_requested and the run ending its attempt waiting", got)
	}

	r := &resumes{}
	if _, err := Decide(t.Context(), f.store, r, Answer{
		ApprovalID: req.ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if want := []string{f.run.String() + ":1"}; !slices.Equal(r.got, want) {
		t.Fatalf("resumed %v, want %v", r.got, want)
	}
	if row := f.row(t); row.Status != "queued" || row.Attempt != 1 {
		t.Fatalf("after the yes the run is %s at attempt %d, want queued at 1", row.Status, row.Attempt)
	}

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := server.calls.Load(); n != 1 {
		t.Errorf("the tool was made %d time(s), want once", n)
	}
	if req := f.request(t); req.Status != "approved" || req.UsedAt == nil {
		t.Errorf("request after the call = %s, used %v; want approved and spent", req.Status, req.UsedAt)
	}
	got := kinds(f.events(t))
	if !slices.Contains(got, "approval_decided") || !slices.Contains(got, "resumed") || got[len(got)-1] != "finished" {
		t.Errorf("events = %v", got)
	}
	if calls := f.model.requests(); len(calls) != 2 {
		t.Errorf("the model was asked %d times, want twice: the call, then the answer", len(calls))
	}
}

// A no is acted on too: the run resumes, the tool is not made, and the model
// reads the person's own words.
func TestACallAPersonDeclinesIsNotMade(t *testing.T) {
	f, server, sam := gatedFixture(t, newScripted(
		callTool("c1", "mail__send", `{"to":"a"}`),
		answer("I did not send it.", 10, 5),
	))
	f.mustExecute(t)

	if _, err := Decide(t.Context(), f.store, &resumes{}, Answer{
		ApprovalID: f.request(t).ID, TeamID: f.team, By: sam, Approve: false, Reason: "wrong customer",
		Via: "dashboard", MaySelf: true,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := server.calls.Load(); n != 0 {
		t.Errorf("a declined call was made %d time(s)", n)
	}
	calls := f.model.requests()
	if said := textOf(calls[len(calls)-1].Messages); !strings.Contains(said, `They said: "wrong customer"`) {
		t.Errorf("the model was not told the person's reason:\n%s", said)
	}
}

// A second answer to one request is refused with what the first said, and
// someone who neither decides the team's requests nor made this one may not
// answer it.
func TestARequestIsAnsweredOnceAndOnlyByWhoMay(t *testing.T) {
	f, _, sam := gatedFixture(t, newScripted(callTool("c1", "mail__send", `{}`)))
	f.mustExecute(t)
	req := f.request(t)

	ana := uuid.New()
	if _, err := Decide(t.Context(), f.store, &resumes{}, Answer{
		ApprovalID: req.ID, TeamID: f.team, By: ana, Approve: true, Via: "dashboard", MaySelf: true,
	}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("someone else's own-approval: err = %v, want ErrNotAllowed", err)
	}
	if row := f.request(t); row.Status != "pending" {
		t.Fatalf("a refused answer changed the request to %s", row.Status)
	}

	first := Answer{ApprovalID: req.ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true}
	if _, err := Decide(t.Context(), f.store, &resumes{}, first); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	r := &resumes{}
	late := first
	late.Approve = false
	got, err := Decide(t.Context(), f.store, r, late)
	if !errors.Is(err, ErrNotPending) || got.Status != "approved" || len(r.got) != 0 {
		t.Errorf("a second answer: %v, the request %s, resumed %v; want ErrNotPending, approved, nothing", err, got.Status, r.got)
	}

	other := uuid.New()
	if _, err := Decide(t.Context(), f.store, &resumes{}, Answer{
		ApprovalID: req.ID, TeamID: other, By: sam, Approve: true, MayDecide: true,
	}); err == nil {
		t.Error("another team's id was answered")
	}
}

// Answered after its time, a request expires instead, and the run resumes
// to say so.
func TestALateAnswerExpiresTheRequestAndResumesTheRun(t *testing.T) {
	f, server, sam := gatedFixture(t, newScripted(
		callTool("c1", "mail__send", `{}`),
		answer("Nobody answered.", 10, 5),
	))
	f.mustExecute(t)
	req := f.request(t)
	f.mustExec(t, `UPDATE approvals SET expires_at = now() - interval '1 minute' WHERE id = $1`, req.ID)

	r := &resumes{}
	_, err := Decide(t.Context(), f.store, r, Answer{
		ApprovalID: req.ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true,
	})
	if !errors.Is(err, ErrExpired) || len(r.got) != 1 {
		t.Fatalf("err = %v, resumed %v; want ErrExpired and the run resumed", err, r.got)
	}
	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := server.calls.Load(); n != 0 {
		t.Errorf("an expired request's call was made %d time(s)", n)
	}
	if row := f.request(t); row.Status != "expired" || row.DecidedBy != nil {
		t.Errorf("request = %s by %v, want expired by nobody", row.Status, row.DecidedBy)
	}
}

// A team that has not turned approvals on sees nothing of them: the call is
// made at once and nothing is asked.
func TestATeamWithApprovalsOffMakesEveryCall(t *testing.T) {
	f, server, _ := gatedFixture(t, newScripted(callTool("c1", "mail__send", `{}`), answer("Sent.", 10, 5)))
	if _, err := f.store.SetTeamApproval(t.Context(), db.SetTeamApprovalParams{
		TeamID: f.team, ApprovalMode: "off", ApprovalTtlMinutes: 60,
	}); err != nil {
		t.Fatal(err)
	}
	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := server.calls.Load(); n != 1 {
		t.Errorf("made %d time(s), want once", n)
	}
	if got, _ := f.store.ListRunApprovals(t.Context(), f.run); len(got) != 0 {
		t.Errorf("requests %v, want none", got)
	}
}

// A pattern that cannot wait is not given a tool that would ask.
func TestAToolThatAsksIsNotGivenToAPlanThatCannotWait(t *testing.T) {
	f, server, _ := gatedFixture(t, newScripted(answer("I cannot send mail here.", 10, 5)))
	f.mustExec(t, `UPDATE agents SET pattern = 'rewoo' WHERE id = $1`, f.agent)
	f.mustExecute(t)

	if n := server.calls.Load(); n != 0 {
		t.Errorf("made %d time(s)", n)
	}
	var unavailable []string
	for _, e := range f.events(t) {
		if e.Kind == "tool_unavailable" {
			u := payload[unavailablePayload](t, e)
			unavailable = append(unavailable, u.Name+": "+u.Reason)
		}
	}
	if !slices.ContainsFunc(unavailable, func(s string) bool { return strings.HasPrefix(s, "mail__send: ") && strings.Contains(s, "cannot wait") }) {
		t.Errorf("unavailable = %v, want mail__send, as needing an approval the pattern cannot wait for", unavailable)
	}
	for _, req := range f.model.requests() {
		for _, tool := range req.Tools {
			if tool.Name == "mail__send" {
				t.Fatal("the planner was offered mail__send")
			}
		}
	}
	_ = agent.PatternReWOO
}
