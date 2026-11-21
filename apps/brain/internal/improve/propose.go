package improve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

var kinds = []string{
	"instructions", "description", "skill_grant", "skill_drop", "skill_new", "skill_edit",
	"server_grant", "server_drop", "tools_narrow", "builtin_tool", "tool_note",
	"limits", "delegation", "pattern",
}

const proposeDescription = `Propose one change to the agent's setup, for a person to approve. One pattern, one part, the smallest change. The edit's shape depends on the kind:
instructions {replace, with}: a passage of the instructions that occurs exactly once (empty to append) and its new text (empty to remove).
description {text}.
skill_grant {skill} · skill_drop {skill}: a skill by name.
skill_new {name, description, body, files: [{path, text}]}: files under references/ or scripts/, text only.
skill_edit {skill, description?, body?: {replace, with}, files?: [{path, text or null}]}: null removes a file.
server_grant {server, tools?} · server_drop {server} · tools_narrow {server, remove: [tools]}.
builtin_tool {tool: web or browser, on}.
tool_note {server, tool, note}: an empty note removes it.
limits {max_tokens?, max_steps?, steer_continuations?, token_budget?}.
delegation {mode?: none, listed or team, add?: [agents], remove?: [agents]}.
pattern {pattern}.
The answer says whether it was stored, or why not; a refusal can be corrected and proposed again.`

var proposeSchema = schemaJSON(obj{
	"type": "object",
	"properties": obj{
		"kind":       oneOfSet(kinds),
		"signature":  obj{"type": "string", "description": "the pattern's key, part:target:cause, or prune:<candidate>"},
		"runs":       obj{"type": "array", "items": text(), "description": "the ids, from Runs to read, it rests on"},
		"why":        obj{"type": "string", "description": "what the runs show, with the contrast; at most 1,000 characters"},
		"hypothesis": obj{"type": "string", "description": "what should change after it; at most 500 characters"},
		"scope":      oneOfSet([]string{"invariant", "domain"}),
		"edit":       obj{"type": "object", "description": "the change, in the kind's shape"},
	},
	"required": []string{"kind", "signature", "runs", "why", "hypothesis", "scope", "edit"},
})

type proposeArgs struct {
	Kind       string          `json:"kind"`
	Signature  string          `json:"signature"`
	Runs       []string        `json:"runs"`
	Why        string          `json:"why"`
	Hypothesis string          `json:"hypothesis"`
	Scope      string          `json:"scope"`
	Edit       json.RawMessage `json:"edit"`
}

// refusal is a proposal the brain will not store, said to the model so it
// may correct it.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

// refused is a refusal, said to the model.
func refused(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }

// asRefusal is err as a refusal when it is one: the improver's own, or a
// rule the proposals store holds every proposal to.
func asRefusal(err error) (refusal, bool) {
	var r refusal
	switch {
	case errors.As(err, &r):
		return r, true
	case proposals.Refused(err):
		return refusal{err.Error()}, true
	}
	return r, false
}

// The marks a proposal from a conversation an outsider spoke in may not
// carry.
var widening = []string{
	proposals.MarkWidensAccess, proposals.MarkWidensReach,
	proposals.MarkRaisesCost, proposals.MarkChangesCode,
}

// propose is the propose_change tool: the proposal built, held to every
// rule, screened and stored, or the reason it was not. Each refusal counts
// once towards the pass's refusalsPerPass.
func (t *passTools) propose(ctx context.Context, raw json.RawMessage) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proposed >= proposalsPerPass || t.refused >= refusalsPerPass {
		return "This pass is done proposing: finish with your report.", nil
	}
	stored, err := t.proposeOnce(ctx, raw)
	if r, ok := asRefusal(err); ok {
		t.refused++
		return "Not stored: " + r.msg, nil
	}
	if err != nil {
		return "", err
	}
	t.proposed++
	return fmt.Sprintf("Stored as proposal %s, waiting for a person.", stored), nil
}

// proposeOnce holds a proposal to each rule in turn, and stores it.
func (t *passTools) proposeOnce(ctx context.Context, raw json.RawMessage) (uuid.UUID, error) {
	in, runs, err := t.parse(raw)
	if err != nil {
		return uuid.UUID{}, err
	}
	// Still on: a team that turned improvement off mid-pass gets nothing
	// more from it.
	settings, on, err := t.im.improving(ctx, t.pass.TeamID)
	if err != nil {
		return uuid.UUID{}, err
	}
	if !on {
		t.refused = refusalsPerPass
		return uuid.UUID{}, refused("improvement was turned off: stop proposing")
	}
	n, err := t.build(ctx, in)
	if err != nil {
		return uuid.UUID{}, err
	}
	n.TeamID, n.AgentID, n.Why, n.Hypothesis, n.Scope = t.pass.TeamID, t.pass.AgentID, in.Why, in.Hypothesis, in.Scope
	n.RunIDs, n.PassID, n.Outsider = runs, &t.pass.ID, t.material.outsider
	sig := in.Signature
	n.Signature = &sig
	if err := proposals.Check(n.Kind, n.Before, n.After); err != nil {
		return uuid.UUID{}, err
	}
	if err := t.guard(ctx, n); err != nil {
		return uuid.UUID{}, err
	}
	if err := t.screenIt(ctx, settings, n, in); err != nil {
		return uuid.UUID{}, err
	}
	return t.store(ctx, n)
}

// parse reads a proposal's arguments and holds it to the pass it came from.
func (t *passTools) parse(raw json.RawMessage) (proposeArgs, []uuid.UUID, error) {
	var in proposeArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, nil, refused("the arguments are not the schema's shape: %v", err)
	}
	in.Why, in.Hypothesis = strings.TrimSpace(in.Why), strings.TrimSpace(in.Hypothesis)
	switch {
	case !slices.Contains(kinds, in.Kind):
		return in, nil, refused("%q is not a kind", in.Kind)
	case in.Why == "" || in.Hypothesis == "":
		return in, nil, refused("a proposal says why and what it expects")
	case utf8.RuneCountInString(in.Why) > 1000 || utf8.RuneCountInString(in.Hypothesis) > 500:
		return in, nil, refused("why is at most 1,000 characters and the hypothesis 500")
	}
	if err := t.citesThisPass(in); err != nil {
		return in, nil, err
	}
	runs := make([]uuid.UUID, 0, len(in.Runs))
	for _, r := range in.Runs {
		id, _ := uuid.Parse(strings.TrimSpace(r)) // checked by citesThisPass
		runs = append(runs, id)
	}
	return in, runs, nil
}

// guard holds a built proposal to what the pass may change: nothing that
// widens an agent after an outsider spoke in what the pass read, and one
// change to a target a pass.
func (t *passTools) guard(ctx context.Context, n proposals.Proposed) error {
	if n.Outsider {
		for _, m := range proposals.Marks(n.Kind, n.Before, n.After) {
			if slices.Contains(widening, m) {
				return refused("someone outside the team spoke in a run this pass read, and it %s: such a change is never proposed from those runs", strings.ReplaceAll(m, "_", " "))
			}
		}
	}
	on, err := t.im.store.CountPassProposalsOnTarget(ctx, db.CountPassProposalsOnTargetParams{PassID: &t.pass.ID, AgentID: t.pass.AgentID, SkillID: n.SkillID})
	if err != nil {
		return fmt.Errorf("counting pass %s's proposals: %w", t.pass.ID, err)
	}
	if on > 0 {
		return refused("this pass has already proposed a change to that target: approving one withdraws the others")
	}
	return nil
}

// screenIt is the screen's verdict: nil to store it, a refusal when it
// found one run's specifics or could make nothing of the answer. A screen
// that cannot be reached stops the pass, for its step to try again: no
// proposal reaches a person unscreened, and an outage is not a refusal.
func (t *passTools) screenIt(ctx context.Context, settings db.GetImprovementSettingsRow, n proposals.Proposed, in proposeArgs) error {
	if t.haltIfOverLocked() {
		// A screen it cannot pay for would be a proposal it cannot keep.
		return refused("the pass has spent its tokens: finish with your report")
	}
	verdict, usage, err := t.im.screen(ctx, settings, n, in)
	t.screenSpent.InputTokens += usage.InputTokens
	t.screenSpent.OutputTokens += usage.OutputTokens
	t.haltIfOverLocked()
	switch {
	case err != nil && !final(err):
		err = fmt.Errorf("screening a proposal: %w", err)
		t.halt(err)
		return err
	case err != nil:
		return refused("it could not be screened (%v), so it is not stored", err)
	case verdict.Verdict != screenOK:
		t.screenedOut++
		return refused("the screen found one run's specifics in it: %s", verdict.Reason)
	}
	return nil
}

// store creates the proposal, replacing what an earlier pass left waiting
// on the same pattern with the same kind of change to the same target. It
// is screened and paid for, so a limit reached meanwhile does not undo it;
// a pass failed as lost meanwhile does, since another may have begun.
func (t *passTools) store(ctx context.Context, n proposals.Proposed) (uuid.UUID, error) {
	ctx, release := lasting(ctx)
	defer release()
	var stored db.ImprovementProposal
	err := t.im.store.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunningImproverPass(ctx, t.pass.ID); errors.Is(err, pgx.ErrNoRows) {
			t.halt(errPassLost)
			return refused("%s: stop proposing", errPassLost)
		} else if err != nil {
			return err
		}
		var err error
		if stored, err = proposals.Create(ctx, q, n); err != nil {
			return err
		}
		return q.WithdrawSupersededProposals(ctx, db.WithdrawSupersededProposalsParams{
			TeamID: t.pass.TeamID, AgentID: t.pass.AgentID, NewerID: &stored.ID,
			Kind: string(n.Kind), Signature: n.Signature, SkillID: n.SkillID,
		})
	})
	if _, ok := asRefusal(err); ok {
		return uuid.UUID{}, err
	}
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("storing the proposal: %w", err)
	}
	return stored.ID, nil
}

// citesThisPass holds a proposal to the pass it came from: one of its
// pattern keys, or one of its prune candidates answered by removing it, and
// only runs it was given.
func (t *passTools) citesThisPass(in proposeArgs) error {
	switch {
	case strings.HasPrefix(in.Signature, prunePrefix):
		c := strings.TrimPrefix(in.Signature, prunePrefix)
		if !slices.Contains(t.material.prune, c) {
			return refused("%q is not one of this pass's prune candidates", c)
		}
		if err := pruneAnswers(c, in.Kind, in.Edit); err != nil {
			return err
		}
	case !slices.Contains(t.material.keys, in.Signature):
		return refused("signature %q is not one of this pass's patterns", in.Signature)
	case len(in.Runs) == 0:
		return refused("a proposal cites at least one of the runs it rests on")
	}
	for _, r := range in.Runs {
		id, err := uuid.Parse(strings.TrimSpace(r))
		if err != nil || !t.material.runs[id] {
			return refused("run %q is not one of the Runs to read", r)
		}
	}
	return nil
}
