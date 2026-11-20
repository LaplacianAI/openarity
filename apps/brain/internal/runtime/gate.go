package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// ApprovalStore is what the gate reads and writes: the team's rules, who the
// run is for, and the requests themselves.
type ApprovalStore interface {
	ListApprovalRules(ctx context.Context, teamID uuid.UUID) ([]db.ApprovalRule, error)
	FindTeamMember(ctx context.Context, arg db.FindTeamMemberParams) (string, error)
	ListRolePermissions(ctx context.Context, role string) ([]string, error)
	InsertApproval(ctx context.Context, arg db.InsertApprovalParams) (db.Approval, error)
	FindPendingRequest(ctx context.Context, arg db.FindPendingRequestParams) (db.Approval, error)
	FindRunScopedApproval(ctx context.Context, arg db.FindRunScopedApprovalParams) (db.Approval, error)
}

// WithApprovals puts every MCP and web tool a run is given behind the team's
// approval policy. Without it, or for a team whose mode is off, nothing is
// gated.
func (b *Builder) WithApprovals(s ApprovalStore) *Builder {
	b.approvals = s
	return b
}

const (
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLength   = 4
	codeTries    = 5
	reasonChars  = 200

	declined = "A person declined this call. It was not made. Do not try again unless they ask; " +
		"say it was not done, or suggest another way."
	expiredCall   = "Nobody answered in time, so the call was not made."
	cancelledCall = "The request to make this call was cancelled, so it was not made."
	usedCall      = "The run stopped during this call after it was approved; it is not known whether it was made."
	// Superseded: the person wrote again instead of answering. At the top
	// the model reads their message next; a delegate never sees it, so it
	// hands the turn back up to where it is read.
	supersededCall = "Not made: the person wrote again before answering. Read what they said, and act on that."
	supersededDeep = "Not made: the person wrote again to the agent that asked you, before answering. " +
		"Stop here: report what you did and what you did not, so it can act on what they said."
	// Asked: the model says it in its own words, then ends its turn, and the
	// run waits. A delegate's words reach the person through its parent.
	askedTop = "Not made yet: this call needs a person's approval, and they have been asked. " +
		"Tell them in your own words what you want to do and why, then end your turn. " +
		"Do not tell them how to answer or give them a code: that is added for you. " +
		"Do not try another way meanwhile: you will be told their answer, and an approved call is made for you."
	askedDeep = "Not made yet: this call needs a person's approval, and they have been asked. " +
		"Say in your own words what you want to do and why, then end your turn: it reaches them through " +
		"the agent that asked you. Do not try another way meanwhile: you will be told their answer, " +
		"and an approved call is made for you."
	askedAgain = "This call, with these arguments, already waits for a person's approval. " +
		"End your turn: you will be told their answer."
)

// policy is what a run's calls are decided by, read once when it is built.
// Nil when the team's mode is off: nothing is gated.
type policy struct {
	store   ApprovalStore
	note    func(ctx context.Context, runID uuid.UUID, worker, kind string, payload any) error
	run     db.Run
	worker  string
	mode    approval.Mode
	rules   []approval.Rule
	actor   approval.Actor
	ttl     time.Duration
	pattern agent.PatternName
	now     func() time.Time
}

func (b *Builder) policyFor(ctx context.Context, run db.Run, s db.TeamSetting, pattern agent.PatternName, l Lineage) (*policy, error) {
	mode := approval.Mode(s.ApprovalMode)
	if b.approvals == nil || mode == approval.Off || mode == "" {
		return nil, nil
	}
	rows, err := b.approvals.ListApprovalRules(ctx, run.TeamID)
	if err != nil {
		return nil, fmt.Errorf("reading the team's approval rules: %w", err)
	}
	rules := make([]approval.Rule, len(rows))
	for i, r := range rows {
		rules[i] = approval.Rule{Kind: r.TargetKind, Target: r.Target, Verdict: approval.Verdict(r.Mode), RequireOther: r.RequireOther}
	}
	canSelf, err := b.canApproveOwn(ctx, run)
	if err != nil {
		return nil, err
	}
	return &policy{
		store: b.approvals, note: b.note, run: run, worker: l.Worker,
		mode: mode, rules: rules, ttl: time.Duration(s.ApprovalTtlMinutes) * time.Minute,
		actor:   approval.Actor{CanSelf: canSelf, Unattended: run.Unattended},
		pattern: pattern, now: time.Now,
	}, nil
}

// canApproveOwn is whether the run's person may approve the calls made for
// them. A run with nobody behind it, or someone no longer in the team, may
// not.
func (b *Builder) canApproveOwn(ctx context.Context, run db.Run) (bool, error) {
	if run.UserID == nil {
		return false, nil
	}
	role, err := b.approvals.FindTeamMember(ctx, db.FindTeamMemberParams{TeamID: run.TeamID, UserID: *run.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the run's person's role: %w", err)
	}
	actions, err := b.approvals.ListRolePermissions(ctx, role)
	if err != nil {
		return false, fmt.Errorf("reading what the %s role may do: %w", role, err)
	}
	return slices.Contains(actions, "approval:self"), nil
}

// A target is what a rule names a tool by, and what a request records.
type target struct {
	name   string // <server>__<tool on the server>, or a built-in's own name
	server string // empty for a built-in
	class  approval.Class
	// also is what approving the call approves besides, shown under its
	// arguments.
	also string
}

// actorOf is who the run acts for; none when there is no policy.
func (p *policy) actorOf() approval.Actor {
	if p == nil {
		return approval.Actor{}
	}
	return p.actor
}

// gate is t as the policy has it. The verdict rests only on what is known
// when the run is built, so it is decided once: a tool that is allowed is
// returned as it was, one that is denied says why, and one that asks is
// wrapped to ask. A tool this run cannot be given at all comes back as why.
func (p *policy) gate(t agent.Tool, at target) (agent.Tool, *Unavailable) {
	if p == nil {
		return t, nil
	}
	return p.wrap(t, at, p.decide(at))
}

func (p *policy) decide(at target) approval.Decision {
	return approval.Decide(approval.Input{
		Tool: at.name, Server: at.server, Class: at.class, Mode: p.mode, Rules: p.rules, Actor: p.actor,
	})
}

// wrap is t as decision d has it.
func (p *policy) wrap(t agent.Tool, at target, d approval.Decision) (agent.Tool, *Unavailable) {
	switch d.Verdict {
	case approval.Allow:
		return t, nil
	case approval.Deny:
		return refusing(t, denial(t.Name, d.Why)), nil
	case approval.Ask:
	}

	if p.pattern == agent.PatternReWOO {
		// ReWOO plans every call before it makes one: the steps after an ask
		// would run on a call that was never made.
		return agent.Tool{}, &Unavailable{Name: t.Name, Reason: "it needs a person's approval, which this pattern cannot wait for"}
	}

	asked := t
	call := t.Call
	asked.Invoke = nil
	asked.Result = func(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
		return p.ask(ctx, at, d.RequireOther, args, func() (agent.ToolResult, error) {
			r, _, err := call(ctx, args)
			return r, err
		})
	}
	return asked, nil
}

func denial(name string, why approval.Why) string {
	if why == approval.ByUnattended {
		return fmt.Sprintf("Nobody is present in this run to approve %s, so it was not made. "+
			"An admin can allow it for runs like this one.", name)
	}
	return fmt.Sprintf("A team rule does not allow %s, so it was not made.", name)
}

func refusing(t agent.Tool, said string) agent.Tool {
	t.Invoke = nil
	t.Result = func(context.Context, json.RawMessage) (agent.ToolResult, error) {
		return agent.ToolResult{}, toModel(said)
	}
	return t
}

// toModel is what the model reads when a call was not made. It is a sentence
// for the model, full stop included, not an error for a log.
type toModel string

func (t toModel) Error() string { return string(t) }

// ask is one call of a tool that needs a person. Nothing is made here
// unless the tool is approved for the rest of the run: the request is
// recorded, and the model is told to say so in its own words and end its
// turn, after which the run waits (hold). The answer is acted on when the run
// resumes (answers.go), so the call made is exactly the one approved. The
// same call asked again while it waits is the same request.
func (p *policy) ask(ctx context.Context, at target, requireOther bool, args json.RawMessage,
	call func() (agent.ToolResult, error),
) (agent.ToolResult, error) {
	callID := agent.ToolCallID(ctx)
	if callID == "" {
		return agent.ToolResult{}, errors.New("the call has no id to be approved by, so it was not made")
	}

	granted, err := p.store.FindRunScopedApproval(ctx, db.FindRunScopedApprovalParams{RootRunID: p.run.RootRunID, Tool: at.name})
	switch {
	case err == nil && granted.ID != uuid.Nil:
		return call()
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return agent.ToolResult{}, fmt.Errorf("reading whether %s is approved for this run: %w", at.name, err)
	}

	if _, err := p.store.FindPendingRequest(ctx, db.FindPendingRequestParams{
		RunID: p.run.ID, Tool: at.name, Arguments: args,
	}); err == nil {
		return agent.ToolResult{}, toModel(askedAgain)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return agent.ToolResult{}, fmt.Errorf("reading whether %s is already asked about: %w", at.name, err)
	}
	if err := p.request(ctx, at, requireOther, callID, args); err != nil {
		return agent.ToolResult{}, err
	}
	if p.run.ParentRunID != nil {
		return agent.ToolResult{}, toModel(askedDeep)
	}
	return agent.ToolResult{}, toModel(askedTop)
}

type approvalRequestedPayload struct {
	ApprovalID uuid.UUID `json:"approval_id"`
	CallID     string    `json:"call_id"`
	Tool       string    `json:"tool"`
	Class      string    `json:"class"`
	Summary    string    `json:"summary"`
	Code       string    `json:"code"`
	ExpiresAt  time.Time `json:"expires_at"`
	Run        uuid.UUID `json:"run"`
}

// request records a call for a person to answer, with a code they can type
// back into a chat, and says so in the run's events.
func (p *policy) request(ctx context.Context, at target, requireOther bool, callID string, args json.RawMessage) error {
	fields := approval.Summary(args)
	if at.also != "" {
		fields = append(fields, approval.Field{Name: "and", Value: at.also})
	}
	summary := approvalSummary(fields)
	for range codeTries {
		code, err := newCode()
		if err != nil {
			return err
		}
		a, err := p.store.InsertApproval(ctx, db.InsertApprovalParams{
			TeamID: p.run.TeamID, RunID: p.run.ID, RootRunID: p.run.RootRunID, CallID: callID,
			Tool: at.name, Class: string(at.class), Arguments: args, Summary: summary, Code: code,
			RequestedFor: p.run.UserID, ExpiresAt: p.now().Add(p.ttl), RequireOther: requireOther,
		})
		var pg *pgconn.PgError
		switch {
		case errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "approvals_pending_code_key":
			continue
		case errors.Is(err, pgx.ErrNoRows):
			// Asked already, by a gate that did not live to suspend: that
			// request stands.
			return nil
		case err != nil:
			return fmt.Errorf("recording the request to approve %s: %w", at.name, err)
		}
		if p.note == nil {
			return nil
		}
		return p.note(ctx, p.run.ID, p.worker, "approval_requested", approvalRequestedPayload{
			ApprovalID: a.ID, CallID: callID, Tool: at.name, Class: string(at.class),
			Summary: summary, Code: code, ExpiresAt: a.ExpiresAt, Run: p.run.ID,
		})
	}
	return fmt.Errorf("could not draw an unused code for the request to approve %s", at.name)
}

func newCode() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(codeAlphabet)))
	for range codeLength {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("drawing a code: %w", err)
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func approvalSummary(fields []approval.Field) string {
	lines := make([]string, len(fields))
	for i, f := range fields {
		lines[i] = f.Name + ": " + f.Value
	}
	return strings.Join(lines, "\n")
}

// declinedWith is the denial the model reads, with the person's own words
// when they gave any, quoted and cut short: they are a person's, not an
// instruction from the run.
func declinedWith(reason *string) string {
	if reason == nil || strings.TrimSpace(*reason) == "" {
		return declined
	}
	said := strings.Join(strings.Fields(*reason), " ")
	if r := []rune(said); len(r) > reasonChars {
		said = string(r[:reasonChars-1]) + "…"
	}
	return fmt.Sprintf("%s They said: %q", declined, said)
}
