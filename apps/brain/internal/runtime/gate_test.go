package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// desk is an ApprovalStore in memory: the requests, by call.
type desk struct {
	byCall   map[string]db.Approval
	runScope *db.Approval
	clashes  int // how many inserts refuse their code first
	claimed  int
	noted    []string
}

func (*desk) ListApprovalRules(context.Context, uuid.UUID) ([]db.ApprovalRule, error) {
	return nil, nil
}

func (*desk) FindTeamMember(context.Context, db.FindTeamMemberParams) (string, error) {
	return "", pgx.ErrNoRows
}
func (*desk) ListRolePermissions(context.Context, string) ([]string, error) { return nil, nil }

func (d *desk) InsertApproval(_ context.Context, a db.InsertApprovalParams) (db.Approval, error) {
	if d.clashes > 0 {
		d.clashes--
		return db.Approval{}, &pgconn.PgError{Code: "23505", ConstraintName: "approvals_pending_code_key"}
	}
	if _, ok := d.byCall[a.CallID]; ok {
		return db.Approval{}, pgx.ErrNoRows
	}
	row := db.Approval{
		ID: uuid.New(), RunID: a.RunID, RootRunID: a.RootRunID, CallID: a.CallID, Tool: a.Tool,
		Class: a.Class, Arguments: a.Arguments, Summary: a.Summary, Code: a.Code, Status: "pending",
		ExpiresAt: a.ExpiresAt, RequireOther: a.RequireOther,
	}
	d.byCall[a.CallID] = row
	return row, nil
}

func (d *desk) GetApprovalByCall(_ context.Context, a db.GetApprovalByCallParams) (db.Approval, error) {
	row, ok := d.byCall[a.CallID]
	if !ok {
		return db.Approval{}, pgx.ErrNoRows
	}
	return row, nil
}

func (d *desk) ClaimApprovalUse(_ context.Context, id uuid.UUID) (db.Approval, error) {
	for k, row := range d.byCall {
		if row.ID == id && row.Status == "approved" && row.UsedAt == nil {
			now := time.Now()
			row.UsedAt = &now
			d.byCall[k] = row
			d.claimed++
			return row, nil
		}
	}
	return db.Approval{}, pgx.ErrNoRows
}

func (d *desk) FindRunScopedApproval(context.Context, db.FindRunScopedApprovalParams) (db.Approval, error) {
	if d.runScope == nil {
		return db.Approval{}, pgx.ErrNoRows
	}
	return *d.runScope, nil
}

func (d *desk) set(call, status, args string, reason *string) {
	row := d.byCall[call]
	row.ID, row.CallID, row.Status, row.Reason = uuid.New(), call, status, reason
	row.Arguments = json.RawMessage(args)
	d.byCall[call] = row
}

// sending is a tool that counts the times it was really made.
func sending(made *int) agent.Tool {
	return agent.Tool{
		Name: "mail__send", Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) { *made++; return "sent", nil },
	}
}

func gatedBy(d *desk, mode approval.Mode, actor approval.Actor) *policy {
	return &policy{
		store: d, run: db.Run{ID: uuid.New(), RootRunID: uuid.New()}, mode: mode, actor: actor,
		ttl: time.Hour, pattern: agent.PatternReAct, now: time.Now,
		note: func(_ context.Context, _ uuid.UUID, _, kind string, _ any) error {
			d.noted = append(d.noted, kind)
			return nil
		},
	}
}

var mailTarget = target{name: "mail__send", server: "mail", class: approval.Destructive}

func callAs(t *testing.T, tool agent.Tool, call, args string) (agent.ToolResult, error) {
	t.Helper()
	r, _, err := tool.Call(agent.WithToolCallID(t.Context(), call), json.RawMessage(args))
	return r, err
}

// The first time, a request is made with a code, the run's events say so,
// and the run is suspended; asked again while it waits, it suspends again
// and asks nothing new. The tool is not made either time.
func TestACallThatAsksIsRequestedOnceAndSuspends(t *testing.T) {
	t.Parallel()

	d := &desk{byCall: map[string]db.Approval{}, clashes: 2}
	var made int
	gated, refused := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true}).gate(sending(&made), mailTarget)
	if refused != nil {
		t.Fatalf("refused: %+v", refused)
	}

	for range 2 {
		if _, err := callAs(t, gated, "c1", `{"to":"a@example.com"}`); !errors.Is(err, agent.ErrSuspended) {
			t.Fatalf("err = %v, want a suspension", err)
		}
	}
	if made != 0 || len(d.byCall) != 1 || len(d.noted) != 1 || d.noted[0] != "approval_requested" {
		t.Fatalf("made %d, requests %d, noted %v; want 0, 1 and one approval_requested", made, len(d.byCall), d.noted)
	}
	row := d.byCall["c1"]
	if len(row.Code) != codeLength || strings.ContainsAny(row.Code, "IO01") {
		t.Errorf("code %q is not four characters of the alphabet", row.Code)
	}
	if row.Summary != "to: a@example.com" || row.Tool != "mail__send" || row.Class != "destructive" {
		t.Errorf("request = %+v", row)
	}
}

// Each answer, acted on when the run returns: only an approval for exactly
// these arguments makes the call, and only once.
func TestAnAnsweredCallIsActedOnAsAnswered(t *testing.T) {
	t.Parallel()

	reason := "not\nto that customer, ever"
	for _, c := range []struct {
		status, args string
		reason       *string
		made         int
		says         string
	}{
		{"approved", `{"to":"a"}`, nil, 1, ""},
		{"approved", `{"to":"b"}`, nil, 0, "not the arguments that were approved"},
		{"denied", `{"to":"a"}`, &reason, 0, `They said: "not to that customer, ever"`},
		{"denied", `{"to":"a"}`, nil, 0, "A person declined this call"},
		{"expired", `{"to":"a"}`, nil, 0, "Nobody answered in time"},
		{"cancelled", `{"to":"a"}`, nil, 0, "was cancelled"},
	} {
		d := &desk{byCall: map[string]db.Approval{}}
		d.set("c1", c.status, `{"to": "a"}`, c.reason)
		var made int
		gated, _ := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true}).gate(sending(&made), mailTarget)
		r, err := callAs(t, gated, "c1", c.args)

		if made != c.made {
			t.Errorf("%s %s: made %d time(s), want %d", c.status, c.args, made, c.made)
		}
		if c.says == "" {
			if err != nil || r.Output != "sent" {
				t.Errorf("%s: %v, %q", c.status, err, r.Output)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.says) || errors.Is(err, agent.ErrSuspended) {
			t.Errorf("%s %s: err = %v, want one saying %q", c.status, c.args, err, c.says)
		}
	}
}

// An approval is spent by the call it was for. Made again, after a crash in
// the middle of it, the call is not made a second time.
func TestAnApprovalIsSpentOnce(t *testing.T) {
	t.Parallel()

	d := &desk{byCall: map[string]db.Approval{}}
	d.set("c1", "approved", `{}`, nil)
	var made int
	gated, _ := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true}).gate(sending(&made), mailTarget)

	if _, err := callAs(t, gated, "c1", `{}`); err != nil {
		t.Fatal(err)
	}
	_, err := callAs(t, gated, "c1", `{}`)
	if made != 1 || err == nil || !strings.Contains(err.Error(), "not known whether it was made") {
		t.Errorf("made %d, second call %v; want made once, and the second not known", made, err)
	}
}

// Approved for the rest of the run, the tool is made on its next call
// without asking, whatever the arguments.
func TestARunScopedApprovalCoversTheRunsLaterCalls(t *testing.T) {
	t.Parallel()

	d := &desk{byCall: map[string]db.Approval{}, runScope: &db.Approval{ID: uuid.New(), Status: "approved"}}
	var made int
	gated, _ := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true}).gate(sending(&made), mailTarget)
	if _, err := callAs(t, gated, "c9", `{"to":"anyone"}`); err != nil || made != 1 || len(d.byCall) != 0 {
		t.Errorf("err %v, made %d, requests %d; want made at once with no request", err, made, len(d.byCall))
	}
}

// What the policy decides when the run is built changes the tool: an allow
// is the tool itself, a deny says why, and an ask the run cannot wait for
// is not given at all, or is refused.
func TestTheVerdictShapesTheTool(t *testing.T) {
	t.Parallel()

	var made int
	tool := sending(&made)

	if got, _ := (*policy)(nil).gate(tool, mailTarget); got.Invoke == nil || got.Result != nil {
		t.Error("with no policy the tool was wrapped")
	}

	d := &desk{byCall: map[string]db.Approval{}}
	read := target{name: "mail__list", server: "mail", class: approval.Read}
	if got, _ := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true}).gate(tool, read); got.Invoke == nil {
		t.Error("an allowed read was wrapped")
	}

	unattended := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true, Unattended: true})
	denied, _ := unattended.gate(tool, mailTarget)
	if _, err := callAs(t, denied, "c1", `{}`); err == nil || !strings.Contains(err.Error(), "Nobody is present") {
		t.Errorf("unattended: %v", err)
	}

	rewoo := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true})
	rewoo.pattern = agent.PatternReWOO
	if _, refused := rewoo.gate(tool, mailTarget); refused == nil || !strings.Contains(refused.Reason, "cannot wait") {
		t.Errorf("ReWOO: %+v", refused)
	}

	child := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true})
	parent := uuid.New()
	child.run.ParentRunID = &parent
	delegated, _ := child.gate(tool, mailTarget)
	if _, err := callAs(t, delegated, "c1", `{}`); err == nil || errors.Is(err, agent.ErrSuspended) ||
		!strings.Contains(err.Error(), "delegated agent cannot wait") {
		t.Errorf("delegated: %v", err)
	}
	if made != 0 || len(d.byCall) != 0 {
		t.Errorf("made %d, requests %d; want neither", made, len(d.byCall))
	}
}

func TestOnlyThoseWhoMayAnswerARequestDo(t *testing.T) {
	t.Parallel()

	sam, ana := uuid.New(), uuid.New()
	mine := db.Approval{RequestedFor: &sam}
	second := db.Approval{RequestedFor: &sam, RequireOther: true}
	for _, c := range []struct {
		a    Answer
		req  db.Approval
		want error
	}{
		{Answer{By: sam, MaySelf: true}, mine, nil},
		{Answer{By: ana, MaySelf: true}, mine, ErrNotAllowed},
		{Answer{By: ana, MayDecide: true}, mine, nil},
		{Answer{By: sam, MayDecide: true, MaySelf: true}, second, ErrNotAllowed},
		{Answer{By: ana, MayDecide: true}, second, nil},
		{Answer{By: sam}, mine, ErrNotAllowed},
		{Answer{By: sam, MaySelf: true}, db.Approval{}, ErrNotAllowed},
	} {
		if got := mayAnswer(c.a, c.req); !errors.Is(got, c.want) {
			t.Errorf("%+v answering %+v = %v, want %v", c.a, c.req, got, c.want)
		}
	}
}

func TestAResumeMakesWhatAPersonAnsweredAndClosesTheRest(t *testing.T) {
	t.Parallel()

	turn := agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
		{ID: "c1", Name: "read"}, {ID: "c2", Name: "mail__send"}, {ID: "c3", Name: "post"},
	}}
	files := agent.Message{Role: agent.RoleUser, Attached: true, Content: []agent.Content{{Type: agent.ContentText, Text: "f"}}}
	result := agent.Message{Role: agent.RoleTool, ToolCallID: "c1", Content: []agent.Content{{Type: agent.ContentText, Text: "ok"}}}
	msgs := []agent.Message{{Role: agent.RoleUser}, turn, result, files}

	// Suspended at c2: c2 and the c3 it never reached stay open.
	unspent := []db.Approval{{CallID: "c2", Status: "approved"}}
	if got := closeCalls(msgs, keptOpen(msgs, unspent)); len(got) != len(msgs) {
		t.Errorf("a turn suspended at c2 became %d messages, want it left for the run to finish", len(got))
	}

	// Crashed inside c2 after its approval was spent: c3 is closed, before
	// the turn's files, and c2 is left to the gate, which knows.
	now := time.Now()
	spent := []db.Approval{{CallID: "c2", Status: "approved", UsedAt: &now}}
	got := closeCalls(msgs, keptOpen(msgs, spent))
	if len(got) != 5 || got[3].ToolCallID != "c3" || got[3].Text() != endedMidCall || !got[4].Attached {
		t.Errorf("after a crash in c2: %+v", got[2:])
	}

	// Nothing was asked: every open call is closed, as before approvals.
	if got := closeCalls(msgs, keptOpen(msgs, nil)); len(got) != 6 || !got[5].Attached {
		t.Errorf("with nothing asked: %d messages", len(got))
	}
}

func TestEachAttemptIsAWorkflowOfItsOwn(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("6f1c7b0a-8a0c-4d1a-9a52-0a3c6d2f1e11")
	if workflowID(id, 0) != id.String() || workflowID(id, 2) != id.String()+":2" {
		t.Errorf("workflow ids %q and %q", workflowID(id, 0), workflowID(id, 2))
	}
}

// broken is an ApprovalStore that fails every read.
type broken struct{ *desk }

func (broken) ListApprovalRules(context.Context, uuid.UUID) ([]db.ApprovalRule, error) {
	return nil, errors.New("the rules could not be read")
}

// A team with approvals off pays nothing for them: its rules are not read,
// so a run starts even when they cannot be.
func TestATeamWithApprovalsOffReadsNoRules(t *testing.T) {
	t.Parallel()

	b := (&Builder{}).WithApprovals(broken{&desk{}})
	for _, mode := range []string{"off", ""} {
		p, err := b.policyFor(t.Context(), db.Run{}, db.TeamSetting{ApprovalMode: mode}, agent.PatternReAct, Lineage{})
		if err != nil || p != nil {
			t.Errorf("mode %q: policy %v, %v; want none, and nothing read", mode, p, err)
		}
	}
	if _, err := b.policyFor(t.Context(), db.Run{}, db.TeamSetting{ApprovalMode: "writes"}, agent.PatternReAct, Lineage{}); err == nil {
		t.Error("with approvals on, a failure to read the rules was not reported")
	}
}
