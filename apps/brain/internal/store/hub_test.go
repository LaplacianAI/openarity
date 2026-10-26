package store

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func setHub(t *testing.T, s *Store, teamID uuid.UUID, hub *uuid.UUID) {
	t.Helper()

	if _, err := s.UpsertTeamSettings(t.Context(), db.UpsertTeamSettingsParams{
		TeamID: teamID, SearchProvider: "none", CompactionMessages: 100, HubAgentID: hub,
	}); err != nil {
		t.Fatalf("UpsertTeamSettings: %v", err)
	}
}

func hubRun(t *testing.T, s *Store, f runFixture) db.Run {
	t.Helper()

	r, err := s.CreateHubRun(t.Context(), db.CreateHubRunParams{
		ID: uuid.New(), TeamID: f.team, SessionID: f.session, Input: "what is due this week?",
	})
	if err != nil {
		t.Fatalf("CreateHubRun: %v", err)
	}
	return r
}

// A team that chose no hub agent has the built-in orchestrator: a hub run
// with no agent row, named for it, on the deployment's default budget.
func TestAHubRunWithNoChosenAgentIsTheOrchestrator(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")

	r := hubRun(t, s, f)
	if !r.Hub || r.AgentID != nil || r.AgentName != "orchestrator" || r.TokenBudget != nil || r.Depth != 0 {
		t.Errorf("hub %v agent %v %q budget %v depth %d; want the orchestrator at the root",
			r.Hub, r.AgentID, r.AgentName, r.TokenBudget, r.Depth)
	}
	if r.Status != "queued" || r.RootRunID != r.ID || r.ParentRunID != nil {
		t.Errorf("status %s root %s parent %v; want a queued root", r.Status, r.RootRunID, r.ParentRunID)
	}
}

func TestAHubRunIsTheTeamsChosenAgent(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	setTokenBudget(t, s, f.agent, 5000)
	setHub(t, s, f.team, &f.agent)

	r := hubRun(t, s, f)
	if !r.Hub || r.AgentID == nil || *r.AgentID != f.agent || r.AgentName != "triage" ||
		r.TokenBudget == nil || *r.TokenBudget != 5000 {
		t.Errorf("hub %v agent %v %q budget %v; want triage with its 5000", r.Hub, r.AgentID, r.AgentName, r.TokenBudget)
	}
}

// Nothing breaks when the hub agent goes: the setting clears, and the next
// hub run is the orchestrator again.
func TestDeletingTheHubAgentReturnsTheTeamToTheOrchestrator(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	setHub(t, s, f.team, &f.agent)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1`, f.agent); err != nil {
		t.Fatalf("delete the hub agent: %v", err)
	}

	got, err := s.GetTeamSettings(t.Context(), f.team)
	if err != nil {
		t.Fatalf("GetTeamSettings: %v", err)
	}
	if got.HubAgentID != nil {
		t.Errorf("hub_agent_id = %v after the agent was deleted, want none", got.HubAgentID)
	}
	if r := hubRun(t, s, f); r.AgentID != nil || r.AgentName != "orchestrator" {
		t.Errorf("next hub run is %v %q, want the orchestrator", r.AgentID, r.AgentName)
	}
}

func TestARunNamingAnAgentIsNotTheHubs(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")

	if r := createRun(t, s, f); r.Hub {
		t.Error("a run started for an agent is the hub's")
	}
}

// The hub is a role at the root: a delegate's run is never the hub's.
func TestAChildIsNeverTheHub(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	root := createRun(t, s, f)
	claim(t, s, root.ID, "w1")
	child := mustStartChild(t, s, root, insertAgent(t, s, f.team, "clerk"), "c1")

	_, err := s.pool.Exec(t.Context(), `UPDATE runs SET hub = true WHERE id = $1`, child.ID)
	wantConstraint(t, err, checkViolation, "runs_hub_is_a_root")
}

// The hub reaches every agent, tool and skill in its team, and nothing in
// another; the hub agent itself is left out of the agents when named.
func TestTheTeamsReachIsEveryAgentToolAndSkillInIt(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	clerk := insertAgent(t, s, f.team, "clerk")
	server := mustCreateServer(t, s, urlServer(f.team, "github")).ID
	tool := insertTool(t, s, server, f.team, "search_code")
	skill := mustCreateSkill(t, s, f.team, "pdf").ID

	other := mustCreate(t, s, "support").ID
	insertAgent(t, s, other, "theirs")
	insertTool(t, s, mustCreateServer(t, s, urlServer(other, "linear")).ID, other, "create_issue")
	mustCreateSkill(t, s, other, "docx")

	whole, err := s.ListTeamReach(t.Context(), db.ListTeamReachParams{TeamID: f.team})
	if err != nil {
		t.Fatalf("ListTeamReach: %v", err)
	}
	if got, want := whole.AgentIds, sortedIDs(f.agent, clerk); !slices.Equal(got, want) {
		t.Errorf("agents = %v, want %v", got, want)
	}
	if !slices.Equal(whole.ToolIds, []uuid.UUID{tool}) || !slices.Equal(whole.SkillIds, []uuid.UUID{skill}) {
		t.Errorf("tools %v skills %v, want %s and %s", whole.ToolIds, whole.SkillIds, tool, skill)
	}

	asHub, err := s.ListTeamReach(t.Context(), db.ListTeamReachParams{TeamID: f.team, Exclude: &f.agent})
	if err != nil {
		t.Fatalf("ListTeamReach: %v", err)
	}
	if !slices.Equal(asHub.AgentIds, []uuid.UUID{clerk}) {
		t.Errorf("agents excluding the hub = %v, want clerk alone", asHub.AgentIds)
	}
}

// An empty team's reach is empty lists, not nulls, so a caller can range
// over it without a check.
func TestAnEmptyTeamReachesNothing(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "empty").ID

	got, err := s.ListTeamReach(t.Context(), db.ListTeamReachParams{TeamID: team})
	if err != nil {
		t.Fatalf("ListTeamReach: %v", err)
	}
	if got.AgentIds == nil || got.ToolIds == nil || got.SkillIds == nil ||
		len(got.AgentIds)+len(got.ToolIds)+len(got.SkillIds) != 0 {
		t.Errorf("reach = %+v, want three empty lists", got)
	}
}

func TestCountingOtherAgents(t *testing.T) {
	s := queryStore(t)
	empty := mustCreate(t, s, "empty").ID
	f := newRunFixture(t, s, "platform")

	count := func(team uuid.UUID, exclude *uuid.UUID) int64 {
		t.Helper()
		n, err := s.CountOtherAgents(t.Context(), db.CountOtherAgentsParams{TeamID: team, Exclude: exclude})
		if err != nil {
			t.Fatalf("CountOtherAgents: %v", err)
		}
		return n
	}
	if n := count(empty, nil); n != 0 {
		t.Errorf("an empty team counts %d", n)
	}
	if n := count(f.team, &f.agent); n != 0 {
		t.Errorf("a team of only the hub counts %d others", n)
	}
	insertAgent(t, s, f.team, "clerk")
	insertAgent(t, s, f.team, "billing")
	if n := count(f.team, &f.agent); n != 2 {
		t.Errorf("counted %d others, want 2", n)
	}
	if n := count(f.team, nil); n != 3 {
		t.Errorf("counted %d with nothing excluded, want 3", n)
	}
}

func TestAPlanEventIsOneTheRuntimeWrites(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	if _, err := s.AppendRunEvent(t.Context(), db.AppendRunEventParams{
		RunID: r.ID, Kind: "plan", Payload: []byte(`{"items":[]}`), Worker: "w1",
	}); err != nil {
		t.Errorf("a plan event was refused: %v", err)
	}
}

// A later save replaces the hub, and a save without one clears it.
func TestSavingTheSettingsAgainChangesTheHub(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	clerk := insertAgent(t, s, f.team, "clerk")

	setHub(t, s, f.team, &f.agent)
	setHub(t, s, f.team, &clerk)
	if r := hubRun(t, s, f); r.AgentID == nil || *r.AgentID != clerk {
		t.Errorf("hub run is %v, want clerk after the second save", r.AgentID)
	}

	setHub(t, s, f.team, nil)
	if r := hubRun(t, s, f); r.AgentID != nil || r.AgentName != "orchestrator" {
		t.Errorf("hub run is %v %q after clearing, want the orchestrator", r.AgentID, r.AgentName)
	}
}

// The API keeps the hub in its team; should a row ever name another team's
// agent, the run is still the team's own orchestrator, never that agent.
func TestAHubFromAnotherTeamIsNeverRun(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	theirs := insertAgent(t, s, mustCreate(t, s, "support").ID, "theirs")
	setHub(t, s, f.team, &theirs)

	if r := hubRun(t, s, f); r.AgentID != nil || r.AgentName != "orchestrator" || r.TeamID != f.team {
		t.Errorf("hub run is %v %q in %s, want this team's orchestrator", r.AgentID, r.AgentName, r.TeamID)
	}
}
