package improve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// agentEdit applies one kind's edit to a copy of the agent's state; a new
// skill it creates is returned with it.
type agentEdit func(t *passTools, ctx context.Context, raw json.RawMessage, after *improvements.AgentState) (*improvements.SkillState, error)

// agentEdits are the kinds that change the agent. skill_edit changes a
// skill, and is built by buildSkillEdit.
var agentEdits = map[improvements.Kind]agentEdit{
	improvements.KindInstructions: editInstructions,
	improvements.KindDescription:  editDescription,
	improvements.KindSkillGrant:   editSkillGrant,
	improvements.KindSkillDrop:    editSkillDrop,
	improvements.KindSkillNew:     editSkillNew,
	improvements.KindServerGrant:  editServerGrant,
	improvements.KindServerDrop:   editServerDrop,
	improvements.KindToolsNarrow:  editToolsNarrow,
	improvements.KindBuiltinTool:  editBuiltinTool,
	improvements.KindToolNote:     editNote,
	improvements.KindLimits:       editLimits,
	improvements.KindDelegation:   editDelegation,
	improvements.KindPattern:      editPattern,
}

// build turns an edit into the proposal's before and after, the agent's
// held to the rules every agent is.
func (t *passTools) build(ctx context.Context, in proposeArgs) (improvements.Proposed, error) {
	kind := improvements.Kind(in.Kind)
	n := improvements.Proposed{Kind: kind}
	if kind == improvements.KindSkillEdit {
		return t.buildSkillEdit(ctx, in, n)
	}
	edit, ok := agentEdits[kind]
	if !ok {
		return n, refused("%q is not a kind", in.Kind)
	}
	before := t.material.state
	after, err := cloneState(before)
	if err != nil {
		return n, err
	}
	skill, err := edit(t, ctx, in.Edit, &after)
	if err != nil {
		return n, err
	}
	if err := agents.Validate(after.Agent, t.im.loops); err != nil {
		return n, refusal{err.Error()}
	}
	n.Before, n.After = improvements.Snapshot{Agent: &before}, improvements.Snapshot{Agent: &after, Skill: skill}
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

func editInstructions(_ *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	var e replaceEdit
	if err := decodeEdit(raw, &e); err != nil {
		return nil, err
	}
	text, err := e.apply(st.Agent.Instructions, "the instructions")
	st.Agent.Instructions = text
	return nil, err
}

func editDescription(_ *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	var e struct {
		Text string `json:"text"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return nil, err
	}
	st.Agent.Description = strings.TrimSpace(e.Text)
	return nil, nil
}

// skillNamed is the team's skill an edit names, and whether the agent
// holds it.
func (t *passTools) skillNamed(raw json.RawMessage) (db.Skill, bool, error) {
	var e struct {
		Skill string `json:"skill"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return db.Skill{}, false, err
	}
	s, ok := t.material.skills[strings.TrimSpace(e.Skill)]
	if !ok {
		return s, false, refused("the team has no skill %q", e.Skill)
	}
	return s, slices.Contains(t.material.state.Agent.Skills, s.ID), nil
}

func editSkillGrant(t *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	s, held, err := t.skillNamed(raw)
	switch {
	case err != nil:
		return nil, err
	case held:
		return nil, refused("the agent already holds skill %s", s.Name)
	}
	st.Agent.Skills = append(st.Agent.Skills, s.ID)
	return nil, nil
}

func editSkillDrop(t *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	s, held, err := t.skillNamed(raw)
	switch {
	case err != nil:
		return nil, err
	case !held:
		return nil, refused("the agent does not hold skill %s", s.Name)
	}
	st.Agent.Skills = slices.DeleteFunc(st.Agent.Skills, func(id uuid.UUID) bool { return id == s.ID })
	return nil, nil
}

func editSkillNew(t *passTools, _ context.Context, raw json.RawMessage, _ *improvements.AgentState) (*improvements.SkillState, error) {
	sk, err := newSkill(raw, t.material.skills)
	if err != nil {
		return nil, err
	}
	return &sk, nil
}

func editBuiltinTool(_ *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	var e struct {
		Tool string `json:"tool"`
		On   bool   `json:"on"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return nil, err
	}
	on := e.On
	switch e.Tool {
	case "web":
		st.Agent.Web = &on
	case "browser":
		st.Agent.Browser = &on
	default:
		return nil, refused("tool is web or browser")
	}
	return nil, nil
}

func editLimits(_ *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	var e struct {
		MaxTokens          *int32 `json:"max_tokens"`
		MaxSteps           *int32 `json:"max_steps"`
		SteerContinuations *int32 `json:"steer_continuations"`
		TokenBudget        *int64 `json:"token_budget"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return nil, err
	}
	a := &st.Agent
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
	return nil, nil
}

func editPattern(_ *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	var e struct {
		Pattern string `json:"pattern"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return nil, err
	}
	st.Agent.Pattern = strings.TrimSpace(e.Pattern)
	return nil, nil
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
			return text, refused("an append adds some text")
		}
		return strings.TrimRight(text, "\n") + "\n\n" + strings.TrimSpace(e.With), nil
	}
	switch strings.Count(text, e.Replace) {
	case 0:
		return text, refused("the passage to replace is not in %s, word for word", what)
	case 1:
	default:
		return text, refused("the passage to replace occurs more than once in %s: quote more of it", what)
	}
	n := utf8.RuneCountInString(text)
	if n > wholeTextBelow && 2*utf8.RuneCountInString(e.Replace) > n {
		return text, refused("it replaces most of %s at once: propose a smaller edit", what)
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

type serverEdit struct {
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
	Remove []string `json:"remove"`
}

// serverNamed is the team's server an edit names, every tool it names
// checked, and where the agent's grant of it is (-1 for none).
func (t *passTools) serverNamed(raw json.RawMessage, st *improvements.AgentState) (serverEdit, db.ListTeamServersWithToolsRow, int, error) {
	var e serverEdit
	if err := decodeEdit(raw, &e); err != nil {
		return e, db.ListTeamServersWithToolsRow{}, -1, err
	}
	s, ok := t.material.servers[strings.TrimSpace(e.Server)]
	if !ok {
		return e, s, -1, refused("the team has no MCP server %q", e.Server)
	}
	for _, tool := range append(slices.Clone(e.Tools), e.Remove...) {
		if !slices.Contains(s.Tools, tool) {
			return e, s, -1, refused("server %s has no tool %q", s.Name, tool)
		}
	}
	i := slices.IndexFunc(st.Agent.MCPServers, func(g agents.ServerGrant) bool { return g.ID == s.ID })
	return e, s, i, nil
}

func editServerGrant(t *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	e, s, i, err := t.serverNamed(raw, st)
	switch {
	case err != nil:
		return nil, err
	case i >= 0:
		return nil, refused("the agent already holds %s", s.Name)
	}
	g := agents.ServerGrant{ID: s.ID}
	if len(e.Tools) > 0 {
		g.Allow = unique(e.Tools)
	}
	st.Agent.MCPServers = append(st.Agent.MCPServers, g)
	return nil, nil
}

func editServerDrop(t *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	_, s, i, err := t.serverNamed(raw, st)
	switch {
	case err != nil:
		return nil, err
	case i < 0:
		return nil, refused("the agent does not hold %s", s.Name)
	}
	st.Agent.MCPServers = slices.Delete(st.Agent.MCPServers, i, i+1)
	st.Notes = slices.DeleteFunc(st.Notes, func(n improvements.ToolNote) bool { return n.Server == s.ID })
	return nil, nil
}

func editToolsNarrow(t *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	e, s, i, err := t.serverNamed(raw, st)
	switch {
	case err != nil:
		return nil, err
	case i < 0:
		return nil, refused("the agent does not hold %s", s.Name)
	}
	g := &st.Agent.MCPServers[i]
	allow := g.Allow
	if allow == nil {
		allow = slices.Clone(s.Tools)
	}
	g.Allow = slices.DeleteFunc(slices.Clone(allow), func(tool string) bool { return slices.Contains(e.Remove, tool) })
	return nil, nil
}

func editNote(t *passTools, _ context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	var e struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
		Note   string `json:"note"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return nil, err
	}
	s, ok := t.material.servers[strings.TrimSpace(e.Server)]
	if !ok {
		return nil, refused("the team has no MCP server %q", e.Server)
	}
	st.Notes = slices.DeleteFunc(st.Notes, func(n improvements.ToolNote) bool { return n.Server == s.ID && n.Tool == e.Tool })
	if note := strings.TrimSpace(e.Note); note != "" {
		st.Notes = append(st.Notes, improvements.ToolNote{Server: s.ID, Tool: e.Tool, Note: note})
	}
	return nil, nil
}

func editDelegation(t *passTools, ctx context.Context, raw json.RawMessage, st *improvements.AgentState) (*improvements.SkillState, error) {
	var e struct {
		Mode   *string  `json:"mode"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := decodeEdit(raw, &e); err != nil {
		return nil, err
	}
	if e.Mode != nil {
		mode := strings.TrimSpace(*e.Mode)
		st.Agent.Delegation = &mode
	}
	for _, name := range e.Add {
		a, err := t.im.store.GetAgentByName(ctx, db.GetAgentByNameParams{TeamID: t.pass.TeamID, Name: strings.TrimSpace(name)})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, refused("the team has no agent %q", name)
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(st.Agent.Delegates, a.ID) {
			st.Agent.Delegates = append(st.Agent.Delegates, a.ID)
		}
	}
	for _, name := range e.Remove {
		id, ok := t.material.agents[strings.TrimSpace(name)]
		if !ok {
			return nil, refused("%q is not one of its delegates", name)
		}
		st.Agent.Delegates = slices.DeleteFunc(st.Agent.Delegates, func(x uuid.UUID) bool { return x == id })
	}
	return nil, nil
}
