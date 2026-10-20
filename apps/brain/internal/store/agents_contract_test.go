package store

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The agents handler answers some refusals by the name of the constraint that
// raised them, so those names are contract. These tests pin them: renaming one
// in a migration fails here, which points at the string in
// internal/api/agents/agents.go that has to move with it.

// A grant naming a server or skill outside the agent's team — or one that
// does not exist — is refused by the composite foreign keys the handler names.
func TestAGrantOutsideTheTeamIsRefusedByTheConstraintsTheHandlerNames(t *testing.T) {
	s := queryStore(t)
	mine := mustCreate(t, s, "platform")
	theirs := mustCreate(t, s, "other")
	a := mustCreateAgent(t, s, mine.ID, "triage")

	for what, id := range map[string]uuid.UUID{
		"another team's server": mustCreateServer(t, s, urlServer(theirs.ID, "github")).ID,
		"a server that is not":  uuid.New(),
	} {
		t.Run(what, func(t *testing.T) {
			err := s.AddAgentMCPServer(t.Context(), db.AddAgentMCPServerParams{AgentID: a.ID, McpServerID: id, TeamID: mine.ID})
			wantConstraint(t, err, "23503", "agent_mcp_servers_server_in_team")
		})
	}

	for what, id := range map[string]uuid.UUID{
		"another team's skill": mustCreateSkill(t, s, theirs.ID, "pdf").ID,
		"a skill that is not":  uuid.New(),
	} {
		t.Run(what, func(t *testing.T) {
			err := s.AddAgentSkills(t.Context(), db.AddAgentSkillsParams{AgentID: a.ID, SkillIds: []uuid.UUID{id}, TeamID: mine.ID})
			wantConstraint(t, err, "23503", "agent_skills_skill_in_team")
		})
	}
}

// A grant with no allow list grants every tool, and is stored as NULL rather
// than as an empty array, which the column refuses. pgx has to send a nil
// slice as NULL for that to hold.
func TestAnUnrestrictedGrantIsStoredAsNull(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	a := mustCreateAgent(t, s, team.ID, "triage")
	open := mustCreateServer(t, s, urlServer(team.ID, "open"))
	narrow := mustCreateServer(t, s, urlServer(team.ID, "narrow"))

	for _, g := range []db.AddAgentMCPServerParams{
		{AgentID: a.ID, McpServerID: open.ID, TeamID: team.ID},
		{AgentID: a.ID, McpServerID: narrow.ID, TeamID: team.ID, Allow: []string{"search", "create"}},
	} {
		if err := s.AddAgentMCPServer(t.Context(), g); err != nil {
			t.Fatalf("AddAgentMCPServer: %v", err)
		}
	}

	rows, err := s.ListAgentMCPServers(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("ListAgentMCPServers: %v", err)
	}
	for _, r := range rows {
		switch r.McpServerID {
		case open.ID:
			if r.Allow != nil {
				t.Errorf("the open grant reads back as %#v, want nil", r.Allow)
			}
		case narrow.ID:
			if !slices.Equal(r.Allow, []string{"search", "create"}) {
				t.Errorf("the narrow grant reads back as %v, want the order it was written in", r.Allow)
			}
		}
	}
}

// What the handler's replace does, in the order it does it, against the real
// database: the row edit and the cleared grants roll back with the refused
// grant, so the agent is never left half on its old grants and half on none.
func TestAReplaceThatFailsPartWayKeepsTheOldGrants(t *testing.T) {
	s := queryStore(t)
	mine := mustCreate(t, s, "platform")
	theirs := mustCreate(t, s, "other")
	a := mustCreateAgent(t, s, mine.ID, "triage")
	kept := mustCreateServer(t, s, urlServer(mine.ID, "github"))
	skill := mustCreateSkill(t, s, mine.ID, "pdf")
	foreign := mustCreateServer(t, s, urlServer(theirs.ID, "theirs"))

	if err := s.AddAgentMCPServer(t.Context(), db.AddAgentMCPServerParams{AgentID: a.ID, McpServerID: kept.ID, TeamID: mine.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgentSkills(t.Context(), db.AddAgentSkillsParams{AgentID: a.ID, SkillIds: []uuid.UUID{skill.ID}, TeamID: mine.ID}); err != nil {
		t.Fatal(err)
	}

	err := s.InTx(t.Context(), func(q *db.Queries) error {
		if _, err := q.UpdateAgent(t.Context(), db.UpdateAgentParams{
			ID: a.ID, Name: "renamed", ModelName: a.ModelName, MaxTokens: a.MaxTokens, Pattern: a.Pattern, MaxSteps: a.MaxSteps,
		}); err != nil {
			return err
		}
		if err := q.ClearAgentMCPServers(t.Context(), a.ID); err != nil {
			return err
		}
		if err := q.ClearAgentSkills(t.Context(), a.ID); err != nil {
			return err
		}
		return q.AddAgentMCPServer(t.Context(), db.AddAgentMCPServerParams{AgentID: a.ID, McpServerID: foreign.ID, TeamID: mine.ID})
	})
	wantConstraint(t, err, "23503", "agent_mcp_servers_server_in_team")

	got, err := s.GetAgent(t.Context(), a.ID)
	if err != nil || got.Name != "triage" {
		t.Errorf("name = %q, %v; want triage", got.Name, err)
	}
	servers, _ := s.ListAgentMCPServers(t.Context(), a.ID)
	if len(servers) != 1 || servers[0].McpServerID != kept.ID {
		t.Errorf("servers = %+v, want the one it had", servers)
	}
	skills, _ := s.ListAgentSkills(t.Context(), a.ID)
	if !slices.Equal(skills, []uuid.UUID{skill.ID}) {
		t.Errorf("skills = %v, want the one it had", skills)
	}
}

// A name is unique per team regardless of case, and the handler answers the
// 23505 as a conflict.
func TestAnAgentNameIsUniqueRegardlessOfCase(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	mustCreateAgent(t, s, team.ID, "Triage")

	_, err := s.CreateAgent(t.Context(), agentParams(team.ID, "TRIAGE"))
	wantConstraint(t, err, "23505", "agents_team_name_key")
}
