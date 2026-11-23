package replay

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// CandidateReads are what a candidate reads beyond a run's build: the grants
// and skills a proposal names that the agent does not hold yet.
type CandidateReads interface {
	ListCandidateMCPServers(ctx context.Context, arg db.ListCandidateMCPServersParams) ([]db.ListCandidateMCPServersRow, error)
	ListCandidateMCPTools(ctx context.Context, ids []uuid.UUID) ([]db.ListCandidateMCPToolsRow, error)
	GetSkill(ctx context.Context, id uuid.UUID) (db.Skill, error)
}

// candidate is the store a replay is built from: the proposing agent as the
// proposal would leave it, everything else as it is. It never writes; the
// stored agent, its grants and its skills are untouched by a replay.
type candidate struct {
	runtime.BuildStore
	reads   CandidateReads
	teamID  uuid.UUID
	agentID uuid.UUID
	// agent is the agent as tried; nil reads it as stored.
	agent *proposals.AgentState
	// skillID and skill are a skill as tried, for a skill_edit or a
	// skill_new; a skill_new's id names no row. Nil reads every skill as
	// stored.
	skillID  uuid.UUID
	skill    *proposals.SkillState
	newSkill bool
	// before cuts the conversation's history to what was said before the
	// past run began.
	before int64
	// texts are the tried skill's files that are not stored, by the key its
	// file rows name them with.
	texts map[string]string
}

// candidateOf is the store for trying p's after on its agent.
func candidateOf(base runtime.BuildStore, reads CandidateReads, p db.ImprovementProposal, before int64) (*candidate, error) {
	var after proposals.Snapshot
	if err := json.Unmarshal(p.After, &after); err != nil {
		return nil, fmt.Errorf("reading proposal %s's change: %w", p.ID, err)
	}
	c := &candidate{
		BuildStore: base, reads: reads, teamID: p.TeamID, agentID: p.AgentID,
		agent: after.Agent, skill: after.Skill, before: before, texts: map[string]string{},
	}
	switch {
	case after.Skill == nil:
	case p.SkillID != nil:
		c.skillID = *p.SkillID
	default:
		// A skill not made yet: it is tried under an id of its own.
		c.skillID, c.newSkill = uuid.New(), true
	}
	return c, nil
}

func (c *candidate) tried(agentID uuid.UUID) bool { return c.agent != nil && agentID == c.agentID }

func (c *candidate) GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error) {
	row, err := c.BuildStore.GetAgent(ctx, id)
	if err != nil || !c.tried(id) {
		return row, err
	}
	p, err := harness.Columns(c.agent.Agent)
	if err != nil {
		return db.Agent{}, fmt.Errorf("reading the tried agent: %w", err)
	}
	row.Name, row.Description, row.Instructions = p.Name, p.Description, p.Instructions
	row.ModelName, row.MaxTokens, row.Temperature = p.ModelName, p.MaxTokens, p.Temperature
	row.Pattern, row.MaxSteps, row.SteerContinuations = p.Pattern, p.MaxSteps, p.SteerContinuations
	row.OutputSchema, row.Parser = p.OutputSchema, p.Parser
	row.ParserModelName, row.ParserMaxTokens, row.ParserTemperature = p.ParserModelName, p.ParserMaxTokens, p.ParserTemperature
	row.TokenBudget, row.Delegation = p.TokenBudget, p.Delegation
	row.Web, row.Browser, row.Workspace = p.Web, p.Browser, p.Workspace
	return row, nil
}

func (c *candidate) grantIDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(c.agent.Agent.MCPServers))
	for i, g := range c.agent.Agent.MCPServers {
		ids[i] = g.ID
	}
	return ids
}

func (c *candidate) ListGrantedMCPServers(ctx context.Context, agentID uuid.UUID) ([]db.ListGrantedMCPServersRow, error) {
	if !c.tried(agentID) {
		return c.BuildStore.ListGrantedMCPServers(ctx, agentID)
	}
	rows, err := c.reads.ListCandidateMCPServers(ctx, db.ListCandidateMCPServersParams{TeamID: c.teamID, Ids: c.grantIDs()})
	if err != nil {
		return nil, err
	}
	allow := map[uuid.UUID][]string{}
	for _, g := range c.agent.Agent.MCPServers {
		allow[g.ID] = g.Allow
	}
	out := make([]db.ListGrantedMCPServersRow, len(rows))
	for i, r := range rows {
		out[i] = db.ListGrantedMCPServersRow{McpServer: r.McpServer, Allow: allow[r.McpServer.ID], OauthStatus: r.OauthStatus}
	}
	return out, nil
}

func (c *candidate) ListGrantedMCPTools(ctx context.Context, agentID uuid.UUID) ([]db.ListGrantedMCPToolsRow, error) {
	if !c.tried(agentID) {
		return c.BuildStore.ListGrantedMCPTools(ctx, agentID)
	}
	rows, err := c.reads.ListCandidateMCPTools(ctx, c.grantIDs())
	if err != nil {
		return nil, err
	}
	allow := map[uuid.UUID][]string{}
	for _, g := range c.agent.Agent.MCPServers {
		allow[g.ID] = g.Allow
	}
	out := make([]db.ListGrantedMCPToolsRow, 0, len(rows))
	for _, r := range rows {
		// A nil allow-list is every tool, as the grant's own query reads it.
		if a := allow[r.McpServerID]; a != nil && !slices.Contains(a, r.Name) {
			continue
		}
		out = append(out, db.ListGrantedMCPToolsRow(r))
	}
	return out, nil
}

func (c *candidate) ListAgentToolNotes(ctx context.Context, agentID uuid.UUID) ([]db.AgentToolNote, error) {
	if !c.tried(agentID) {
		return c.BuildStore.ListAgentToolNotes(ctx, agentID)
	}
	out := make([]db.AgentToolNote, len(c.agent.Notes))
	for i, n := range c.agent.Notes {
		out[i] = db.AgentToolNote{AgentID: agentID, TeamID: c.teamID, McpServerID: n.Server, Tool: n.Tool, Note: n.Note}
	}
	return out, nil
}

func (c *candidate) ListGrantedSkills(ctx context.Context, agentID uuid.UUID) ([]db.Skill, error) {
	var rows []db.Skill
	if c.tried(agentID) {
		for _, id := range c.agent.Agent.Skills {
			s, err := c.reads.GetSkill(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("reading skill %s: %w", id, err)
			}
			if s.TeamID != c.teamID {
				return nil, fmt.Errorf("skill %s is not the team's", id)
			}
			rows = append(rows, s)
		}
	} else {
		var err error
		if rows, err = c.BuildStore.ListGrantedSkills(ctx, agentID); err != nil {
			return nil, err
		}
	}
	if c.skill == nil || agentID != c.agentID {
		return rows, nil
	}
	tried := c.skillRow()
	for i := range rows {
		if rows[i].ID == c.skillID {
			tried.TeamID, tried.CreatedAt, tried.SourceRef, tried.SourceSha = rows[i].TeamID, rows[i].CreatedAt, rows[i].SourceRef, rows[i].SourceSha
			rows[i] = tried
			return rows, nil
		}
	}
	if c.newSkill {
		rows = append(rows, tried)
	}
	return rows, nil
}

func (c *candidate) skillRow() db.Skill {
	s := c.skill
	meta, _ := json.Marshal(s.Metadata)
	return db.Skill{
		ID: c.skillID, TeamID: c.teamID, Name: s.Name, Description: s.Description, License: s.License,
		Compatibility: s.Compatibility, Metadata: meta, AllowedTools: s.AllowedTools, Body: s.Body, Source: s.Source,
	}
}

func (c *candidate) ListSkillFiles(ctx context.Context, skillID uuid.UUID) ([]db.SkillFile, error) {
	if c.skill == nil || skillID != c.skillID {
		return c.BuildStore.ListSkillFiles(ctx, skillID)
	}
	stored := map[string]db.SkillFile{}
	if !c.newSkill {
		rows, err := c.BuildStore.ListSkillFiles(ctx, skillID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			stored[r.Path+"\x00"+hex.EncodeToString(r.Sha256)] = r
		}
	}
	var out []db.SkillFile
	for _, f := range c.skill.Files {
		sha, _ := hex.DecodeString(f.SHA256)
		row := db.SkillFile{SkillID: skillID, TeamID: c.teamID, Path: f.Path, Sha256: sha, MediaType: f.MediaType}
		switch kept, ok := stored[f.Path+"\x00"+f.SHA256]; {
		case f.Text != nil:
			// Not stored until the change is applied: read from its text.
			row.ObjectKey = "replay/" + skillID.String() + "/" + f.SHA256
			row.Size = int64(len(*f.Text))
			c.texts[row.ObjectKey] = *f.Text
		case ok:
			row = kept
		default:
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// ListSessionHistory reads only what was said before the past run began.
func (c *candidate) ListSessionHistory(ctx context.Context, arg db.ListSessionHistoryParams) ([]db.ListSessionHistoryRow, error) {
	arg.Before = min(arg.Before, c.before)
	return c.BuildStore.ListSessionHistory(ctx, arg)
}
