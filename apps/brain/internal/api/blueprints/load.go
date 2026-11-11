package blueprints

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type Reads interface {
	ListTeamAgents(ctx context.Context, teamID uuid.UUID) ([]db.Agent, error)
	ListTeamAgentServers(ctx context.Context, teamID uuid.UUID) ([]db.AgentMcpServer, error)
	ListTeamAgentSkills(ctx context.Context, teamID uuid.UUID) ([]db.AgentSkill, error)
	ListTeamAgentDelegates(ctx context.Context, teamID uuid.UUID) ([]db.AgentDelegate, error)
	ListTeamMCPServers(ctx context.Context, teamID uuid.UUID) ([]db.McpServer, error)
	ListTeamSkills(ctx context.Context, teamID uuid.UUID) ([]db.Skill, error)
	ListTeamSkillFiles(ctx context.Context, teamID uuid.UUID) ([]db.ListTeamSkillFilesRow, error)
}

// load reads everything a plan compares against.
func load(ctx context.Context, s Reads, teamID uuid.UUID) (team, error) {
	t := team{
		id: teamID, agents: map[string]db.Agent{}, servers: map[string]db.McpServer{},
		skills: map[string]db.Skill{}, files: map[uuid.UUID]map[string][]byte{}, grants: map[uuid.UUID]held{},
	}
	nameOf := map[uuid.UUID]string{}

	agentRows, err := s.ListTeamAgents(ctx, teamID)
	if err != nil {
		return team{}, err
	}
	for _, a := range agentRows {
		t.agents[a.Name], nameOf[a.ID] = a, a.Name
		t.grants[a.ID] = held{servers: map[string][]string{}}
	}

	servers, err := s.ListTeamMCPServers(ctx, teamID)
	if err != nil {
		return team{}, err
	}
	for _, srv := range servers {
		t.servers[srv.Name], nameOf[srv.ID] = srv, srv.Name
	}

	skills, err := s.ListTeamSkills(ctx, teamID)
	if err != nil {
		return team{}, err
	}
	for _, sk := range skills {
		t.skills[sk.Name], nameOf[sk.ID] = sk, sk.Name
		t.files[sk.ID] = map[string][]byte{}
	}

	files, err := s.ListTeamSkillFiles(ctx, teamID)
	if err != nil {
		return team{}, err
	}
	for _, f := range files {
		if m, ok := t.files[f.SkillID]; ok {
			m[f.Path] = f.Sha256
		}
	}

	return t, t.loadGrants(ctx, s, nameOf)
}

func (t team) loadGrants(ctx context.Context, s Reads, nameOf map[uuid.UUID]string) error {
	servers, err := s.ListTeamAgentServers(ctx, t.id)
	if err != nil {
		return err
	}
	for _, g := range servers {
		t.grants[g.AgentID].servers[nameOf[g.McpServerID]] = g.Allow
	}

	skills, err := s.ListTeamAgentSkills(ctx, t.id)
	if err != nil {
		return err
	}
	for _, g := range skills {
		h := t.grants[g.AgentID]
		h.skills = append(h.skills, nameOf[g.SkillID])
		t.grants[g.AgentID] = h
	}

	delegates, err := s.ListTeamAgentDelegates(ctx, t.id)
	if err != nil {
		return err
	}
	for _, g := range delegates {
		h := t.grants[g.AgentID]
		h.delegates = append(h.delegates, nameOf[g.DelegateID])
		t.grants[g.AgentID] = h
	}
	return nil
}

// sortedSkills is the team's skills in name order.
func (t team) sortedSkills() []db.Skill {
	out := make([]db.Skill, 0, len(t.skills))
	for _, name := range slices.Sorted(maps.Keys(t.skills)) {
		out = append(out, t.skills[name])
	}
	return out
}
