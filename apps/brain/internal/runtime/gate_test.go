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
	// scoped is what the run-scoped approval was last looked up by.
	scoped  db.FindRunScopedApprovalParams
	clashes int // how many inserts refuse their code first
	noted   []string
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

func (d *desk) FindPendingRequest(_ context.Context, a db.FindPendingRequestParams) (db.Approval, error) {
	for _, row := range d.byCall {
		if row.Tool == a.Tool && row.Status == "pending" && string(row.Arguments) == string(a.Arguments) {
			return row, nil
		}
	}
	return db.Approval{}, pgx.ErrNoRows
}

func (d *desk) FindRunScopedApproval(_ context.Context, a db.FindRunScopedApprovalParams) (db.Approval, error) {
	d.scoped = a
	if d.runScope == nil {
		return db.Approval{}, pgx.ErrNoRows
	}
	return *d.runScope, nil
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

// Asking makes nothing: a request is made, with a code, the run's events say
// so, and the model is told to say it in its own words and end its turn. The
// same call asked again while it waits, by another call id, is the same
// request. A delegate's words reach the person through the agent above it.
func TestACallThatAsksIsRequestedOnceAndTheModelSaysSo(t *testing.T) {
	t.Parallel()

	d := &desk{byCall: map[string]db.Approval{}, clashes: 2}
	var made int
	gated, refused := gatedBy(d, approval.Writes, approval.Actor{CanSelf: true}).gate(sending(&made), mailTarget)
	if refused != nil {
		t.Fatalf("refused: %+v", refused)
	}

	if _, err := callAs(t, gated, "c1", `{"to":"a@example.com"}`); err == nil || err.Error() != askedTop {
		t.Fatalf("err = %v, want the model told to say so", err)
	}
	if _, err := callAs(t, gated, "c2", `{"to":"a@example.com"}`); err == nil || err.Error() != askedAgain {
		t.Fatalf("asked again: %v, want the request already made", err)
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

	other := &desk{byCall: map[string]db.Approval{}}
	child := gatedBy(other, approval.Writes, approval.Actor{CanSelf: true})
	parent := uuid.New()
	child.run.ParentRunID = &parent
	delegated, _ := child.gate(sending(&made), mailTarget)
	if _, err := callAs(t, delegated, "c1", `{}`); err == nil || err.Error() != askedDeep {
		t.Errorf("delegated: %v, want its words sent up", err)
	}
	if made != 0 || len(other.byCall) != 1 {
		t.Errorf("made %d, requests %d; want one request and nothing made", made, len(other.byCall))
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

	if made != 0 {
		t.Errorf("made %d", made)
	}
}

// An answer that makes nothing reads as what it was: a no with the person's
// words, no answer in time, a request that ended with its run, or one its
// person gave up by writing again, which a delegate hands back up.
func TestAnAnswerThatMakesNothingSaysWhatItWas(t *testing.T) {
	t.Parallel()

	sam, parent := uuid.New(), uuid.New()
	top, deep := db.Run{}, db.Run{ParentRunID: &parent}
	said := "wrong customer"
	for _, c := range []struct {
		run  db.Run
		a    db.Approval
		want string
	}{
		{top, db.Approval{Status: "denied", Reason: &said}, declined + ` They said: "wrong customer"`},
		{top, db.Approval{Status: "expired"}, expiredCall},
		{top, db.Approval{Status: "cancelled"}, cancelledCall},
		{top, db.Approval{Status: "cancelled", DecidedBy: &sam}, supersededCall},
		{deep, db.Approval{Status: "cancelled", DecidedBy: &sam}, supersededDeep},
		{deep, db.Approval{Status: "cancelled"}, cancelledCall},
	} {
		got, err := (&Executor{}).answered(t.Context(), c.run, Built{}, worker, c.a)
		if err != nil || got != c.want {
			t.Errorf("%s by %v, delegated %v: %q, %v; want %q", c.a.Status, c.a.DecidedBy, c.run.ParentRunID != nil, got, err, c.want)
		}
	}
	if _, err := (&Executor{}).answered(t.Context(), top, Built{}, worker, db.Approval{Status: "pending"}); err == nil {
		t.Error("a pending request was acted on")
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
