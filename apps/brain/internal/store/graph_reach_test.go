package store

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	return out
}

func toolID(t *testing.T, s *Store, serverID uuid.UUID, name string) uuid.UUID {
	t.Helper()

	tools, err := s.ListMCPToolsByServer(t.Context(), serverID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == name {
			return tool.ID
		}
	}
	t.Fatalf("no tool %q on server %s", name, serverID)
	return uuid.Nil
}

func mustGrantServer(t *testing.T, s *Store, a db.Agent, srv db.McpServer, allow []string) {
	t.Helper()

	if err := s.AddAgentMCPServer(t.Context(), db.AddAgentMCPServerParams{
		AgentID: a.ID, McpServerID: srv.ID, TeamID: a.TeamID, Allow: allow,
	}); err != nil {
		t.Fatalf("AddAgentMCPServer: %v", err)
	}
}

// The graph tool's agent reach until slice C narrows it by delegation: the
// whole team, never the caller itself, never another team.
func TestReachableAgentsAreTheTeamWithoutTheCaller(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "reach-agents")
	other := mustCreate(t, s, "reach-agents-other")

	caller := mustCreateAgent(t, s, team.ID, "caller")
	second := mustCreateAgent(t, s, team.ID, "second")
	third := mustCreateAgent(t, s, team.ID, "third")
	mustCreateAgent(t, s, other.ID, "elsewhere")

	got, err := s.ListReachableAgentIDs(t.Context(), db.ListReachableAgentIDsParams{
		TeamID: team.ID, AgentID: caller.ID,
	})
	if err != nil {
		t.Fatalf("ListReachableAgentIDs: %v", err)
	}
	if want := sortedIDs(second.ID, third.ID); !slices.Equal(got, want) {
		t.Fatalf("reach = %v, want %v, ordered by id", got, want)
	}
}

func TestAnAgentAloneInItsTeamReachesNoAgent(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "reach-alone")
	caller := mustCreateAgent(t, s, team.ID, "only")

	got, err := s.ListReachableAgentIDs(t.Context(), db.ListReachableAgentIDsParams{
		TeamID: team.ID, AgentID: caller.ID,
	})
	if err != nil {
		t.Fatalf("ListReachableAgentIDs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("reach = %v, want none", got)
	}
}

// A grant's allow-list cuts the tools reachable through it; a grant with no
// list reaches every tool on the server.
func TestGrantedToolsHonourTheAllowList(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "reach-tools")
	srv := mustCreateServer(t, s, urlServer(team.ID, "files"))
	mustUpsertTool(t, s, srv, "search", "Find a file")
	mustUpsertTool(t, s, srv, "delete", "Remove a file")
	search, del := toolID(t, s, srv.ID, "search"), toolID(t, s, srv.ID, "delete")

	limited := mustCreateAgent(t, s, team.ID, "limited")
	whole := mustCreateAgent(t, s, team.ID, "whole")
	none := mustCreateAgent(t, s, team.ID, "none")
	mustGrantServer(t, s, limited, srv, []string{"search"})
	mustGrantServer(t, s, whole, srv, nil)

	for _, c := range []struct {
		agent db.Agent
		want  []uuid.UUID
	}{
		{limited, []uuid.UUID{search}},
		{whole, sortedIDs(search, del)},
		{none, nil},
	} {
		got, err := s.ListGrantedToolIDs(t.Context(), c.agent.ID)
		if err != nil {
			t.Fatalf("ListGrantedToolIDs(%s): %v", c.agent.Name, err)
		}
		if len(got) != len(c.want) || (len(got) > 0 && !slices.Equal(got, c.want)) {
			t.Errorf("%s reaches %v, want %v", c.agent.Name, got, c.want)
		}
	}
}

// Two servers both granted: the tools of each, and nothing of a server the
// agent was not granted.
func TestGrantedToolsSpanEveryGrantedServerAndNoOther(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "reach-servers")
	files := mustCreateServer(t, s, urlServer(team.ID, "files"))
	mail := mustCreateServer(t, s, urlServer(team.ID, "mail"))
	secret := mustCreateServer(t, s, urlServer(team.ID, "payroll"))
	mustUpsertTool(t, s, files, "read", "")
	mustUpsertTool(t, s, mail, "send", "")
	mustUpsertTool(t, s, secret, "pay", "")

	a := mustCreateAgent(t, s, team.ID, "clerk")
	mustGrantServer(t, s, a, files, nil)
	mustGrantServer(t, s, a, mail, nil)

	got, err := s.ListGrantedToolIDs(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("ListGrantedToolIDs: %v", err)
	}
	want := sortedIDs(toolID(t, s, files.ID, "read"), toolID(t, s, mail.ID, "send"))
	if !slices.Equal(got, want) {
		t.Fatalf("reach = %v, want %v, ordered by id", got, want)
	}
}
