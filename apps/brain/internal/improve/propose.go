package improve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/improvements"
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

func refused(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }

// The marks a proposal from a conversation an outsider spoke in may not
// carry.
var widening = []string{
	improvements.MarkWidensAccess, improvements.MarkWidensReach,
	improvements.MarkRaisesCost, improvements.MarkChangesCode,
}

// propose is the propose_change tool: the proposal built, held to every
// rule, screened and stored, or the reason it was not.
func (t *passTools) propose(ctx context.Context, raw json.RawMessage) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proposed >= proposalsPerPass || t.refused >= refusalsPerPass {
		return "This pass is done proposing: finish with your report.", nil
	}
	stored, err := t.proposeOnce(ctx, raw)
	var r refusal
	if errors.As(err, &r) {
		return "Not stored: " + r.msg, nil
	}
	if err != nil {
		return "", err
	}
	t.proposed++
	return fmt.Sprintf("Stored as proposal %s, waiting for a person.", stored), nil
}

func (t *passTools) proposeOnce(ctx context.Context, raw json.RawMessage) (uuid.UUID, error) {
	var in proposeArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return uuid.UUID{}, refused("the arguments are not the schema's shape: %v", err)
	}
	in.Why, in.Hypothesis = strings.TrimSpace(in.Why), strings.TrimSpace(in.Hypothesis)
	switch {
	case !slices.Contains(kinds, in.Kind):
		return uuid.UUID{}, refused("%q is not a kind", in.Kind)
	case in.Why == "" || in.Hypothesis == "":
		return uuid.UUID{}, refused("a proposal says why and what it expects")
	case utf8.RuneCountInString(in.Why) > 1000 || utf8.RuneCountInString(in.Hypothesis) > 500:
		return uuid.UUID{}, refused("why is at most 1,000 characters and the hypothesis 500")
	}

	// 1. Still on.
	settings, err := t.im.store.GetImprovementSettings(ctx, t.pass.TeamID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !settings.Improvement) {
		t.refused = refusalsPerPass
		return uuid.UUID{}, refused("improvement was turned off: stop proposing")
	}
	if err != nil {
		return uuid.UUID{}, err
	}

	// 4. Its pattern and its runs.
	if err := t.citesThisPass(in); err != nil {
		t.refused++
		return uuid.UUID{}, err
	}
	runs := make([]uuid.UUID, 0, len(in.Runs))
	for _, r := range in.Runs {
		id, _ := uuid.Parse(strings.TrimSpace(r)) // checked by citesThisPass
		runs = append(runs, id)
	}

	// 2 and 3. Built from the target now, and held to the kind's rules.
	n, err := t.build(ctx, in)
	if err != nil {
		if errors.As(err, new(refusal)) || improvements.Refused(err) {
			t.refused++
			return uuid.UUID{}, refusal{err.Error()}
		}
		return uuid.UUID{}, err
	}
	n.TeamID, n.AgentID, n.Why, n.Hypothesis, n.Scope = t.pass.TeamID, t.pass.AgentID, in.Why, in.Hypothesis, in.Scope
	n.RunIDs, n.PassID = runs, &t.pass.ID
	sig := in.Signature
	n.Signature = &sig
	if err := improvements.Check(n.Kind, n.Before, n.After); err != nil {
		t.refused++
		return uuid.UUID{}, refusal{err.Error()}
	}

	// 5. Outsider.
	if n.Outsider, err = t.im.store.AnyDigestOutsider(ctx, db.AnyDigestOutsiderParams{AgentID: t.pass.AgentID, RunIds: runs}); err != nil {
		return uuid.UUID{}, err
	}
	if n.Outsider {
		for _, m := range improvements.Marks(n.Kind, n.Before, n.After) {
			if slices.Contains(widening, m) {
				t.refused++
				return uuid.UUID{}, refused("someone outside the team spoke in a run it cites, and it %s: such a change is never proposed from those runs", strings.ReplaceAll(m, "_", " "))
			}
		}
	}

	// 6. One per target in a pass.
	target := db.CountPassProposalsOnTargetParams{PassID: &t.pass.ID, AgentID: t.pass.AgentID, SkillID: n.SkillID}
	if on, err := t.im.store.CountPassProposalsOnTarget(ctx, target); err != nil {
		return uuid.UUID{}, err
	} else if on > 0 {
		t.refused++
		return uuid.UUID{}, refused("this pass has already proposed a change to that target: approving one withdraws the others")
	}

	// 7. The screen.
	verdict, err := t.im.screen(ctx, settings, n, in)
	if err != nil {
		t.refused++
		return uuid.UUID{}, refused("it could not be screened (%v), so it is not stored", err)
	}
	if verdict.Verdict != screenOK {
		t.refused++
		t.screened++
		return uuid.UUID{}, refused("the screen found one run's specifics in it: %s", verdict.Reason)
	}

	// 8. Stored, replacing what earlier passes left waiting on the target.
	var stored db.ImprovementProposal
	err = t.im.store.InTx(ctx, func(q *db.Queries) error {
		var err error
		if stored, err = improvements.Create(ctx, q, n); err != nil {
			return err
		}
		return q.WithdrawImprovementProposals(ctx, db.WithdrawImprovementProposalsParams{
			TeamID: t.pass.TeamID, AppliedID: &stored.ID, AgentID: t.pass.AgentID, SkillID: n.SkillID,
		})
	})
	if improvements.Refused(err) {
		t.refused++
		return uuid.UUID{}, refusal{err.Error()}
	}
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("storing the proposal: %w", err)
	}
	return stored.ID, nil
}

// citesThisPass holds a proposal to the pass it came from: one of its
// pattern keys or prune candidates, and only runs it was given.
func (t *passTools) citesThisPass(in proposeArgs) error {
	switch {
	case strings.HasPrefix(in.Signature, prunePrefix):
		c := strings.TrimPrefix(in.Signature, prunePrefix)
		if !slices.Contains(t.material.prune, c) {
			return refused("%q is not one of this pass's prune candidates", c)
		}
		if in.Kind != "skill_drop" && in.Kind != "tools_narrow" {
			return refused("a prune candidate is answered with skill_drop or tools_narrow")
		}
	case !slices.Contains(t.material.keys, in.Signature):
		return refused("signature %q is not one of this pass's patterns", in.Signature)
	}
	if len(in.Runs) == 0 && !strings.HasPrefix(in.Signature, prunePrefix) {
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

// build turns an edit into the proposal's before and after.
func (t *passTools) build(ctx context.Context, in proposeArgs) (improvements.Proposed, error) {
	m := t.material
	kind := improvements.Kind(in.Kind)
	n := improvements.Proposed{Kind: kind}
	if kind == improvements.KindSkillEdit {
		return t.buildSkillEdit(ctx, in, n)
	}
	before := m.state
	after, err := cloneState(before)
	if err != nil {
		return n, err
	}
	a := &after.Agent
	switch kind {
	case improvements.KindInstructions:
		var e replaceEdit
		if err := decodeEdit(in.Edit, &e); err != nil {
			return n, err
		}
		if a.Instructions, err = e.apply(a.Instructions, "the instructions"); err != nil {
			return n, err
		}
	case improvements.KindDescription:
		var e struct {
			Text string `json:"text"`
		}
		if err := decodeEdit(in.Edit, &e); err != nil {
			return n, err
		}
		a.Description = strings.TrimSpace(e.Text)
	case improvements.KindSkillGrant, improvements.KindSkillDrop:
		var e struct {
			Skill string `json:"skill"`
		}
		if err := decodeEdit(in.Edit, &e); err != nil {
			return n, err
		}
		s, ok := m.skills[strings.TrimSpace(e.Skill)]
		if !ok {
			return n, refused("the team has no skill %q", e.Skill)
		}
		if kind == improvements.KindSkillGrant {
			a.Skills = append(a.Skills, s.ID)
		} else {
			a.Skills = slices.DeleteFunc(a.Skills, func(id uuid.UUID) bool { return id == s.ID })
		}
	case improvements.KindSkillNew:
		sk, err := newSkill(in.Edit, m.skills)
		if err != nil {
			return n, err
		}
		n.Before, n.After = improvements.Snapshot{Agent: &before}, improvements.Snapshot{Agent: &after, Skill: &sk}
		return n, nil
	case improvements.KindServerGrant, improvements.KindServerDrop, improvements.KindToolsNarrow:
		if err := m.editGrant(kind, in.Edit, &after); err != nil {
			return n, err
		}
	case improvements.KindBuiltinTool:
		var e struct {
			Tool string `json:"tool"`
			On   bool   `json:"on"`
		}
		if err := decodeEdit(in.Edit, &e); err != nil {
			return n, err
		}
		on := e.On
		switch e.Tool {
		case "web":
			a.Web = &on
		case "browser":
			a.Browser = &on
		default:
			return n, refused("tool is web or browser")
		}
	case improvements.KindToolNote:
		if err := m.editNote(in.Edit, &after); err != nil {
			return n, err
		}
	case improvements.KindLimits:
		var e struct {
			MaxTokens          *int32 `json:"max_tokens"`
			MaxSteps           *int32 `json:"max_steps"`
			SteerContinuations *int32 `json:"steer_continuations"`
			TokenBudget        *int64 `json:"token_budget"`
		}
		if err := decodeEdit(in.Edit, &e); err != nil {
			return n, err
		}
		if e.MaxTokens != nil {
			a.Model.MaxTokens = *e.MaxTokens
		}
		if e.MaxSteps != nil {
			a.MaxSteps = *e.MaxSteps
		}
		if e.SteerContinuations != nil {
			a.SteerContinuations = *e.SteerContinuations
		}
		if e.TokenBudget != nil {
			a.TokenBudget = e.TokenBudget
		}
	case improvements.KindDelegation:
		if err := t.editDelegation(ctx, in.Edit, &after); err != nil {
			return n, err
		}
	case improvements.KindPattern:
		var e struct {
			Pattern string `json:"pattern"`
		}
		if err := decodeEdit(in.Edit, &e); err != nil {
			return n, err
		}
		a.Pattern = strings.TrimSpace(e.Pattern)
	case improvements.KindSkillEdit: // built by buildSkillEdit, above
	}
	if err := agents.Validate(after.Agent, t.im.loops); err != nil {
		return n, refusal{err.Error()}
	}
	n.Before, n.After = improvements.Snapshot{Agent: &before}, improvements.Snapshot{Agent: &after}
	return n, nil
}

func decodeEdit(raw json.RawMessage, into any) error {
	if err := json.Unmarshal(raw, into); err != nil {
		return refused("the edit is not the kind's shape: %v", err)
	}
	return nil
}

// cloneState is a deep copy of an agent's state: its JSON round trip.
func cloneState(s improvements.AgentState) (improvements.AgentState, error) {
	var out improvements.AgentState
	b, err := json.Marshal(s)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(b, &out)
}

// replaceEdit swaps one passage of a text, or appends to it.
type replaceEdit struct {
	Replace string `json:"replace"`
	With    string `json:"with"`
}

// Below this many characters a text may be rewritten whole; above it, an
// edit may remove at most half of it at once.
const wholeTextBelow = 400

func (e replaceEdit) apply(text, what string) (string, error) {
	if e.Replace == "" {
		if strings.TrimSpace(e.With) == "" {
			return "", refused("an append adds some text")
		}
		return strings.TrimRight(text, "\n") + "\n\n" + strings.TrimSpace(e.With), nil
	}
	switch strings.Count(text, e.Replace) {
	case 0:
		return "", refused("the passage to replace is not in %s, word for word", what)
	case 1:
	default:
		return "", refused("the passage to replace occurs more than once in %s: quote more of it", what)
	}
	n := utf8.RuneCountInString(text)
	if n > wholeTextBelow && 2*utf8.RuneCountInString(e.Replace) > n {
		return "", refused("it replaces most of %s at once: propose a smaller edit", what)
	}
	return strings.Replace(text, e.Replace, e.With, 1), nil
}

type fileEdit struct {
	Path string  `json:"path"`
	Text *string `json:"text"`
}

func fileState(p, text string) improvements.FileState {
	sum := sha256.Sum256([]byte(text))
	media := "text/plain; charset=utf-8"
	if path.Ext(p) == ".md" {
		media = "text/markdown; charset=utf-8"
	}
	return improvements.FileState{Path: p, MediaType: media, SHA256: hex.EncodeToString(sum[:]), Text: &text}
}

func newSkill(raw json.RawMessage, team map[string]db.Skill) (improvements.SkillState, error) {
	var e struct {
		Name        string     `json:"name"`
		Description string     `json:"description"`
		Body        string     `json:"body"`
		Files       []fileEdit `json:"files"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return improvements.SkillState{}, err
	}
	name := strings.TrimSpace(e.Name)
	if _, taken := team[name]; taken {
		return improvements.SkillState{}, refused("the team already has a skill %q: grant or edit it instead", name)
	}
	s := improvements.SkillState{Name: name, Description: strings.TrimSpace(e.Description), Body: e.Body, Source: "upload"}
	for _, f := range e.Files {
		if f.Text == nil {
			return improvements.SkillState{}, refused("a new skill's file %q carries its text", f.Path)
		}
		s.Files = append(s.Files, fileState(f.Path, *f.Text))
	}
	return s, nil
}

func (t *passTools) buildSkillEdit(ctx context.Context, in proposeArgs, n improvements.Proposed) (improvements.Proposed, error) {
	var e struct {
		Skill       string       `json:"skill"`
		Description *string      `json:"description"`
		Body        *replaceEdit `json:"body"`
		Files       []fileEdit   `json:"files"`
	}
	if err := decodeEdit(in.Edit, &e); err != nil {
		return n, err
	}
	row, ok := t.material.skills[strings.TrimSpace(e.Skill)]
	if !ok || !slices.Contains(t.material.state.Agent.Skills, row.ID) {
		return n, refused("the agent holds no skill %q", e.Skill)
	}
	var before improvements.SkillState
	if err := t.im.store.InTx(ctx, func(q *db.Queries) error {
		var err error
		before, err = improvements.SkillNow(ctx, q, row)
		return err
	}); err != nil {
		return n, err
	}
	after := before
	after.Files = slices.Clone(before.Files)
	if e.Description != nil {
		after.Description = strings.TrimSpace(*e.Description)
	}
	if e.Body != nil {
		body, err := e.Body.apply(before.Body, "the skill's body")
		if err != nil {
			return n, err
		}
		after.Body = body
	}
	for _, f := range e.Files {
		i := slices.IndexFunc(after.Files, func(x improvements.FileState) bool { return x.Path == f.Path })
		switch {
		case f.Text == nil && i < 0:
			return n, refused("the skill has no file %q to remove", f.Path)
		case f.Text == nil:
			after.Files = slices.Delete(after.Files, i, i+1)
		case i < 0:
			after.Files = append(after.Files, fileState(f.Path, *f.Text))
		default:
			after.Files[i] = fileState(f.Path, *f.Text)
		}
	}
	id := row.ID
	n.SkillID = &id
	n.Before, n.After = improvements.Snapshot{Skill: &before}, improvements.Snapshot{Skill: &after}
	return n, nil
}

// editGrant grants, drops or narrows one server.
func (m material) editGrant(kind improvements.Kind, raw json.RawMessage, st *improvements.AgentState) error {
	var e struct {
		Server string   `json:"server"`
		Tools  []string `json:"tools"`
		Remove []string `json:"remove"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return err
	}
	s, ok := m.servers[strings.TrimSpace(e.Server)]
	if !ok {
		return refused("the team has no MCP server %q", e.Server)
	}
	for _, tool := range append(slices.Clone(e.Tools), e.Remove...) {
		if !slices.Contains(s.Tools, tool) {
			return refused("server %s has no tool %q", s.Name, tool)
		}
	}
	grants := st.Agent.MCPServers
	i := slices.IndexFunc(grants, func(g agents.ServerGrant) bool { return g.ID == s.ID })
	switch kind {
	case improvements.KindServerGrant:
		if i >= 0 {
			return refused("the agent already holds %s", s.Name)
		}
		g := agents.ServerGrant{ID: s.ID}
		if len(e.Tools) > 0 {
			g.Allow = unique(e.Tools)
		}
		grants = append(grants, g)
		st.Agent.MCPServers = grants
	case improvements.KindServerDrop:
		if i < 0 {
			return refused("the agent does not hold %s", s.Name)
		}
		st.Agent.MCPServers = slices.Delete(grants, i, i+1)
		st.Notes = slices.DeleteFunc(st.Notes, func(n improvements.ToolNote) bool { return n.Server == s.ID })
	case improvements.KindToolsNarrow:
		if i < 0 {
			return refused("the agent does not hold %s", s.Name)
		}
		allow := grants[i].Allow
		if allow == nil {
			allow = slices.Clone(s.Tools)
		}
		grants[i].Allow = slices.DeleteFunc(slices.Clone(allow), func(tool string) bool { return slices.Contains(e.Remove, tool) })
	case improvements.KindInstructions, improvements.KindDescription, improvements.KindSkillGrant,
		improvements.KindSkillDrop, improvements.KindSkillNew, improvements.KindSkillEdit,
		improvements.KindBuiltinTool, improvements.KindToolNote, improvements.KindLimits,
		improvements.KindDelegation, improvements.KindPattern:
		return refused("a %s is not a change to a server grant", kind)
	}
	return nil
}

func (m material) editNote(raw json.RawMessage, st *improvements.AgentState) error {
	var e struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
		Note   string `json:"note"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return err
	}
	s, ok := m.servers[strings.TrimSpace(e.Server)]
	if !ok {
		return refused("the team has no MCP server %q", e.Server)
	}
	st.Notes = slices.DeleteFunc(st.Notes, func(n improvements.ToolNote) bool { return n.Server == s.ID && n.Tool == e.Tool })
	if note := strings.TrimSpace(e.Note); note != "" {
		st.Notes = append(st.Notes, improvements.ToolNote{Server: s.ID, Tool: e.Tool, Note: note})
	}
	return nil
}

func (t *passTools) editDelegation(ctx context.Context, raw json.RawMessage, st *improvements.AgentState) error {
	var e struct {
		Mode   *string  `json:"mode"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return err
	}
	if e.Mode != nil {
		mode := strings.TrimSpace(*e.Mode)
		st.Agent.Delegation = &mode
	}
	for _, name := range e.Add {
		a, err := t.im.store.GetAgentByName(ctx, db.GetAgentByNameParams{TeamID: t.pass.TeamID, Name: strings.TrimSpace(name)})
		if errors.Is(err, pgx.ErrNoRows) {
			return refused("the team has no agent %q", name)
		}
		if err != nil {
			return err
		}
		if !slices.Contains(st.Agent.Delegates, a.ID) {
			st.Agent.Delegates = append(st.Agent.Delegates, a.ID)
		}
	}
	for _, name := range e.Remove {
		id, ok := t.material.agents[strings.TrimSpace(name)]
		if !ok {
			return refused("%q is not one of its delegates", name)
		}
		st.Agent.Delegates = slices.DeleteFunc(st.Agent.Delegates, func(x uuid.UUID) bool { return x == id })
	}
	return nil
}
