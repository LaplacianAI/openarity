package runtime

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

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
	return gatedAnswering(t, model, []sdk.Content{&sdk.TextContent{Text: "send ran"}})
}

// gatedAnswering is gatedFixture with mail__send answering content.
func gatedAnswering(t *testing.T, model *scripted, content []sdk.Content) (*execFixture, *mcpServer, uuid.UUID) {
	t.Helper()

	f := newExecFixture(t, model)
	f.exec.builder.WithApprovals(f.store)
	server := serveAnswering(t, map[string][]sdk.Content{"send": content})
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

const askedWords = "I've drafted the notice to billing@acme.example; may I send it?"

// A call approved after a wait hands the model what it attached, as a call
// made in the turn does: the text says the image was attached, so it is.
func TestAnApprovedCallsImageReachesTheModel(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	f, _, sam := gatedAnswering(t, newScripted(
		callTool("c1", "mail__send", `{"to":"billing@acme.example"}`),
		answer(askedWords, 10, 5),
		answer("Sent, and here is the receipt.", 10, 5),
	), []sdk.Content{&sdk.TextContent{Text: "sent"}, &sdk.ImageContent{Data: png, MIMEType: "image/png"}})

	f.mustExecute(t)
	req := f.request(t)
	if _, err := Decide(t.Context(), f.store, &resumes{}, Answer{
		ApprovalID: req.ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	calls := f.model.requests()
	msgs := calls[len(calls)-1].Messages
	caught := msgs[len(msgs)-1]
	if caught.Role != agent.RoleUser || len(caught.Content) != 3 {
		t.Fatalf("the resumed turn opened with %+v, want what happened, the call's line and its image", caught)
	}
	if said := caught.Content[0].Text; !strings.Contains(said, "[attached image-1.png: image/png, 16 bytes, file ") {
		t.Errorf("what happened says %q, want the image named", said)
	}
	if line := caught.Content[1].Text; line != "Attached by mail__send for call c1, as its output, not the person's words:" {
		t.Errorf("the line before the image is %q", line)
	}
	if img := caught.Content[2]; img.Type != agent.ContentImage || img.Blob == nil || !bytes.Equal(img.Blob.Data, png) {
		t.Errorf("the resumed turn carried %+v, want the image's bytes", img)
	}
}

// The whole of it: a call that needs a person makes nothing; the model says
// so in its own words and ends its turn, and the run waits, having said it.
// The person's yes queues the run again as its next attempt; the resumed run
// makes the call once, with the arguments that were approved, tells the model
// what came of it, and finishes.
func TestACallWaitsForAPersonAndIsMadeOnceTheySayYes(t *testing.T) {
	f, server, sam := gatedFixture(t, newScripted(
		callTool("c1", "mail__send", `{"to":"billing@acme.example"}`),
		answer(askedWords, 10, 5),
		answer("Sent.", 10, 5),
	))

	f.mustExecute(t)

	parked := f.row(t)
	if parked.Status != "waiting" || parked.Worker != nil || parked.WaitingSince == nil || deref(parked.Output) != askedWords {
		t.Fatalf("after asking the run is %s, worker %v, waiting since %v, said %q; want waiting, held by nobody, in its words",
			parked.Status, parked.Worker, parked.WaitingSince, deref(parked.Output))
	}
	if n := server.calls.Load(); n != 0 {
		t.Fatalf("the tool was made %d time(s) before anyone answered", n)
	}
	req := f.request(t)
	if req.Status != "pending" || req.CallID != "c1" || req.Tool != "mail__send" || req.Summary != "to: billing@acme.example" {
		t.Fatalf("request = %+v", req)
	}
	events := f.events(t)
	if got := kinds(events); !slices.Contains(got, "approval_requested") || got[len(got)-1] != "waiting" {
		t.Fatalf("events = %v, want approval_requested and the run ending its attempt waiting", got)
	}
	if w := payload[waitingPayload](t, events[len(events)-1]); w.Said != askedWords {
		t.Errorf("the waiting event said %q, want the model's own words", w.Said)
	}
	calls := f.model.requests()
	if said := textOf(calls[1].Messages); !strings.Contains(said, askedTop) {
		t.Errorf("the model was not told to say it in its own words:\n%s", said)
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
	if req := f.request(t); req.Status != "approved" || req.UsedAt == nil || req.ToldAt == nil {
		t.Errorf("request after the call = %s, used %v, told %v; want approved, spent and told", req.Status, req.UsedAt, req.ToldAt)
	}
	after := f.events(t)
	got := kinds(after)
	if !slices.Contains(got, "approval_decided") || !slices.Contains(got, "resumed") || got[len(got)-1] != "finished" {
		t.Errorf("events = %v", got)
	}
	// The call the brain made on the resume is in the trace as any call is.
	made := -1
	for i := len(after) - 1; i >= 0 && made < 0; i-- {
		if after[i].Kind == "tool_result" && payload[toolResultPayload](t, after[i]).ID == "c1" {
			made = i
		}
	}
	if made < 0 || after[made-1].Kind != "tool_call" {
		t.Fatalf("events = %v, want the approved call's tool_call and tool_result", got)
	}
	if r := payload[toolResultPayload](t, after[made]); r.Name != "mail__send" || r.Error != "" || r.Output == "" {
		t.Errorf("the approved call's result = %+v", r)
	}
	calls = f.model.requests()
	if len(calls) != 3 {
		t.Fatalf("the model was asked %d times, want three: the call, its words, the answer", len(calls))
	}
	if said := textOf(calls[2].Messages); !strings.Contains(said, "A person approved it, and it was made. Its result:") {
		t.Errorf("the model was not told what came of the approved call:\n%s", said)
	}
}

// A no is told too: the run resumes, the tool is not made, and the model
// reads the person's own words.
func TestACallAPersonDeclinesIsNotMade(t *testing.T) {
	f, server, sam := gatedFixture(t, newScripted(
		callTool("c1", "mail__send", `{"to":"a"}`),
		answer(askedWords, 10, 5),
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
	f, _, sam := gatedFixture(t, newScripted(callTool("c1", "mail__send", `{}`), answer(askedWords, 10, 5)))
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
		answer(askedWords, 10, 5),
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

// A send_file that asks makes nothing until a person says yes; the resume
// then makes it once, as the run built it: the file is sent, its event
// written, and the request spent.
func TestAnApprovedSendFileIsMadeOnTheResume(t *testing.T) {
	f := newExecFixture(t, nil)
	var session, channel, sam, message uuid.UUID
	f.mustScan(t, `SELECT session_id FROM runs WHERE id = $1`, []any{f.run}, &session)
	f.mustScan(t, `INSERT INTO users (issuer, subject) VALUES ('dev', 'sam') RETURNING id`, nil, &sam)
	f.mustExec(t, `INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`, f.team, sam)
	f.mustExec(t, `UPDATE runs SET user_id = $2 WHERE id = $1`, f.run, sam)
	f.mustScan(t, `SELECT id FROM channels WHERE team_id = $1 AND provider = 'dashboard'`, []any{f.team}, &channel)
	f.mustExec(t, `UPDATE sessions SET channel_id = $1, provider_ref = 'dash-1' WHERE id = $2`, channel, session)
	f.mustScan(t, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text)
		VALUES ($1, $2, $3, 'ext-1', 'send me the export') RETURNING id`, []any{channel, session, sam}, &message)
	export, err := f.store.CreateAttachment(t.Context(), db.CreateAttachmentParams{
		MessageID: &message, SessionID: session, TeamID: f.team, ObjectKey: "teams/x/objects/export", KeyVersion: 1,
		MediaType: "text/csv", SizeBytes: 4, Filename: "export.csv",
	})
	if err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}
	f.objects.data[export.ObjectKey] = "a,b\n"
	if _, err := f.store.SetTeamApproval(t.Context(), db.SetTeamApprovalParams{
		TeamID: f.team, ApprovalMode: "all", ApprovalTtlMinutes: 60,
	}); err != nil {
		t.Fatalf("SetTeamApproval: %v", err)
	}
	f.model = newScripted(
		callTool("c1", SendFileToolName, `{"id":"`+export.ID.String()+`"}`),
		answer("May I send you the export?", 10, 5),
		answer("Sent.", 10, 5),
	)
	f.exec = f.executor(t, func(agent.Endpoint) (agent.ModelClient, error) { return f.model, nil },
		agent.Endpoint{BaseURL: "http://model.test"})
	f.exec.builder.WithApprovals(f.store)
	f.exec.builder.WithFileSends(FileSends{Store: f.store, SendsFiles: func(string) bool { return false }, Logger: quietLogger()})

	sentEvents := func() int {
		var n int
		f.mustScan(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND kind = 'file_sent'`, []any{f.run}, &n)
		return n
	}

	f.mustExecute(t)
	if row := f.row(t); row.Status != "waiting" {
		t.Fatalf("after asking the run is %s, want waiting", row.Status)
	}
	if n := sentEvents(); n != 0 {
		t.Fatalf("%d files sent before anyone answered", n)
	}
	req := f.request(t)
	if req.Tool != SendFileToolName || req.Class != "read" || req.CallID != "c1" {
		t.Fatalf("request = %+v", req)
	}

	if _, err := Decide(t.Context(), f.store, &resumes{}, Answer{
		ApprovalID: req.ID, TeamID: f.team, By: sam, Approve: true, Via: "dashboard", MaySelf: true,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	if n := sentEvents(); n != 1 {
		t.Errorf("%d files sent on the resume, want one", n)
	}
	if req := f.request(t); req.Status != "approved" || req.UsedAt == nil {
		t.Errorf("request after the call = %s, used %v; want approved and spent", req.Status, req.UsedAt)
	}
}
