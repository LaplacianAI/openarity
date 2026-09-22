package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ref is identity_test.go's ptr for any type; the optional columns here are
// floats and ints as well as strings.
func ref[T any](v T) *T { return &v }

func agentParams(teamID uuid.UUID, name string) db.CreateAgentParams {
	return db.CreateAgentParams{
		TeamID: teamID, Name: name, Kind: "custom",
		ModelName: "test/model", MaxTokens: 1024, Pattern: "react", MaxSteps: 5,
	}
}

func mustCreateAgent(t *testing.T, s *Store, teamID uuid.UUID, name string) db.Agent {
	t.Helper()

	a, err := s.CreateAgent(t.Context(), agentParams(teamID, name))
	if err != nil {
		t.Fatalf("CreateAgent(%q): %v", name, err)
	}
	return a
}

func urlServer(teamID uuid.UUID, name string) db.CreateMCPServerParams {
	return db.CreateMCPServerParams{
		TeamID: teamID, Name: name, Url: ref("https://mcp.example.com"), Env: []byte(`{}`),
	}
}

func mustCreateServer(t *testing.T, s *Store, p db.CreateMCPServerParams) db.McpServer {
	t.Helper()

	srv, err := s.CreateMCPServer(t.Context(), p)
	if err != nil {
		t.Fatalf("CreateMCPServer(%q): %v", p.Name, err)
	}
	return srv
}

func mustCreateSkill(t *testing.T, s *Store, teamID uuid.UUID, name string) db.Skill {
	t.Helper()

	sk, err := s.CreateSkill(t.Context(), db.CreateSkillParams{
		TeamID: teamID, Name: name, Description: "Does " + name, Body: "# " + name,
	})
	if err != nil {
		t.Fatalf("CreateSkill(%q): %v", name, err)
	}
	return sk
}

func mustUpsertTool(t *testing.T, s *Store, srv db.McpServer, name, desc string) {
	t.Helper()

	if err := s.UpsertMCPTool(t.Context(), db.UpsertMCPToolParams{
		McpServerID: srv.ID, TeamID: srv.TeamID, Name: name, Description: desc,
		InputSchema: []byte(`{"type":"object"}`),
	}); err != nil {
		t.Fatalf("UpsertMCPTool(%q): %v", name, err)
	}
}

func toolNames(t *testing.T, s *Store, serverID uuid.UUID) []string {
	t.Helper()

	tools, err := s.ListMCPToolsByServer(t.Context(), serverID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	names := make([]string, len(tools))
	for i, tl := range tools {
		names[i] = tl.Name
	}
	return names
}

// --- agents ---

// Every column the SDK reads has to come back as it went in, including the
// optional ones in both states — a nil that came back as 0 would be a
// temperature nobody set.
func TestAnAgentRoundTripsEveryField(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	for name, p := range map[string]db.CreateAgentParams{
		"optional fields empty": agentParams(team.ID, "bare"),
		"optional fields set": {
			TeamID: team.ID, Name: "full", Description: "Sorts issues", Kind: "custom",
			Instructions: "You sort issues.", ModelName: "anthropic/claude-opus-5",
			MaxTokens: 4096, Temperature: ref(0.2), Pattern: "plan", MaxSteps: 8,
			SteerContinuations: 2, OutputSchema: []byte(`{"name":"triage","json":{}}`),
			Parser: true, ParserModelName: ref("small/model"),
			ParserMaxTokens: ref(int32(256)), ParserTemperature: ref(0.0),
		},
	} {
		t.Run(name, func(t *testing.T) {
			created, err := s.CreateAgent(t.Context(), p)
			if err != nil {
				t.Fatalf("CreateAgent: %v", err)
			}

			got, err := s.GetAgent(t.Context(), created.ID)
			if err != nil {
				t.Fatalf("GetAgent: %v", err)
			}

			want := db.Agent{
				ID: created.ID, TeamID: p.TeamID, Name: p.Name, Description: p.Description,
				Kind: p.Kind, Instructions: p.Instructions, ModelName: p.ModelName,
				MaxTokens: p.MaxTokens, Temperature: p.Temperature, Pattern: p.Pattern,
				MaxSteps: p.MaxSteps, SteerContinuations: p.SteerContinuations,
				OutputSchema: p.OutputSchema, Parser: p.Parser,
				ParserModelName: p.ParserModelName, ParserMaxTokens: p.ParserMaxTokens,
				ParserTemperature: p.ParserTemperature,
				CreatedAt:         got.CreatedAt, UpdatedAt: got.UpdatedAt,
			}
			if !agentsEqual(got, want) {
				t.Errorf("round trip:\n got  %+v\n want %+v", got, want)
			}
		})
	}
}

// agentsEqual compares pointers by value and jsonb by meaning. jsonb stores
// parsed JSON, not the text sent: {"name":"t","json":{}} comes back as
// {"json": {}, "name": "t"} — keys reordered, spaces added.
func sameJSON(a, b []byte) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func agentsEqual(a, b db.Agent) bool {
	eqF := func(x, y *float64) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqI := func(x, y *int32) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqS := func(x, y *string) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }

	return a.ID == b.ID && a.TeamID == b.TeamID && a.Name == b.Name &&
		a.Description == b.Description && a.Kind == b.Kind &&
		a.Instructions == b.Instructions && a.ModelName == b.ModelName &&
		a.MaxTokens == b.MaxTokens && eqF(a.Temperature, b.Temperature) &&
		a.Pattern == b.Pattern && a.MaxSteps == b.MaxSteps &&
		a.SteerContinuations == b.SteerContinuations &&
		sameJSON(a.OutputSchema, b.OutputSchema) && a.Parser == b.Parser &&
		eqS(a.ParserModelName, b.ParserModelName) && eqI(a.ParserMaxTokens, b.ParserMaxTokens) &&
		eqF(a.ParserTemperature, b.ParserTemperature)
}

// A replace is whole: leaving temperature out clears it. With sqlc.arg in
// place of narg this would be a float64 and "unset" could not be said.
func TestReplacingAnAgentCanClearItsTemperature(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	p := agentParams(team.ID, "triage")
	p.Temperature = ref(0.7)
	a, err := s.CreateAgent(t.Context(), p)
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := s.UpdateAgent(t.Context(), db.UpdateAgentParams{
		ID: a.ID, Name: a.Name, ModelName: a.ModelName, MaxTokens: a.MaxTokens,
		Pattern: a.Pattern, MaxSteps: a.MaxSteps,
	})
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if got.Temperature != nil {
		t.Errorf("temperature = %v after a replace without one, want nil", *got.Temperature)
	}
}

// kind and team_id are not in UpdateAgent at all; this is what fails if
// someone adds them.
func TestReplacingAnAgentLeavesItsKindAndTeam(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	p := agentParams(team.ID, "entry")
	p.Kind = "orchestrator"
	a, err := s.CreateAgent(t.Context(), p)
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := s.UpdateAgent(t.Context(), db.UpdateAgentParams{
		ID: a.ID, Name: "renamed", ModelName: a.ModelName, MaxTokens: a.MaxTokens,
		Pattern: a.Pattern, MaxSteps: a.MaxSteps,
	})
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if got.Kind != "orchestrator" || got.TeamID != team.ID {
		t.Errorf("kind %q team %s after a replace, want orchestrator and %s", got.Kind, got.TeamID, team.ID)
	}
	if !got.UpdatedAt.After(a.UpdatedAt) {
		t.Errorf("updated_at did not move: %s then %s", a.UpdatedAt, got.UpdatedAt)
	}
}

func TestReplacingAMissingAgentReportsNoRows(t *testing.T) {
	s := queryStore(t)

	_, err := s.UpdateAgent(t.Context(), db.UpdateAgentParams{
		ID: uuid.New(), Name: "x", ModelName: "m", MaxTokens: 1, Pattern: "react", MaxSteps: 1,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("UpdateAgent of a missing id: %v, want pgx.ErrNoRows", err)
	}
}

// Rows sharing a timestamp are the case a created_at-only cursor gets wrong:
// it either repeats one or skips one at the page boundary.
func TestListAgentsPagesThroughASharedTimestampWithoutRepeats(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	other := mustCreate(t, s, "support")
	mustCreateAgent(t, s, other.ID, "not-mine")

	at := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	want := map[uuid.UUID]bool{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		a := mustCreateAgent(t, s, team.ID, n)
		if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET created_at = $1 WHERE id = $2`, at, a.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		want[a.ID] = true
	}

	seen := map[uuid.UUID]bool{}
	params := db.ListAgentsByTeamParams{TeamID: team.ID, PageSize: 2}
	for page := 0; page < 10; page++ {
		rows, err := s.ListAgentsByTeam(t.Context(), params)
		if err != nil {
			t.Fatalf("ListAgentsByTeam: %v", err)
		}
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatalf("%q appeared on two pages", r.Name)
			}
			if !want[r.ID] {
				t.Fatalf("%q is not this team's", r.Name)
			}
			seen[r.ID] = true
		}
		if len(rows) < 2 {
			break
		}
		last := rows[len(rows)-1]
		params.UseCursor, params.AfterCreatedAt, params.AfterID = true, last.CreatedAt, last.ID
	}

	if len(seen) != len(want) {
		t.Errorf("paged through %d agents, want %d", len(seen), len(want))
	}
}

// One statement for every skill: each id granted, sorted on the way out, and
// one outbox row for the agent rather than one per skill.
func TestGrantingSkillsIsOneStatement(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := mustCreateAgent(t, s, team.ID, "triage")

	var ids []uuid.UUID
	for _, n := range []string{"a", "b", "c"} {
		ids = append(ids, mustCreateSkill(t, s, team.ID, n).ID)
	}
	clearOutbox(t, s)

	if err := s.AddAgentSkills(t.Context(), db.AddAgentSkillsParams{
		AgentID: agent.ID, SkillIds: ids, TeamID: team.ID,
	}); err != nil {
		t.Fatalf("AddAgentSkills: %v", err)
	}

	got, err := s.ListAgentSkills(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("ListAgentSkills: %v", err)
	}
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if !slices.Equal(got, sorted) {
		t.Errorf("granted %v, want %v in id order", got, sorted)
	}

	if rows := outbox(t, s); len(rows) != 1 {
		t.Errorf("three skills granted in one call left %v, want the agent once", rows)
	}
}

// The handler calls this for an agent with no skills; it must be a no-op, not
// an error and not a NULL row.
func TestGrantingNoSkillsGrantsNothing(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := mustCreateAgent(t, s, team.ID, "triage")

	if err := s.AddAgentSkills(t.Context(), db.AddAgentSkillsParams{
		AgentID: agent.ID, SkillIds: []uuid.UUID{}, TeamID: team.ID,
	}); err != nil {
		t.Fatalf("AddAgentSkills with none: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM agent_skills`); n != 0 {
		t.Errorf("%d grants from an empty list", n)
	}
}

// A nil allow list is "every tool" and must stay distinct from a list; the
// schema refuses an empty one, so nil is the only way to say "all".
func TestAServerGrantKeepsItsAllowList(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := mustCreateAgent(t, s, team.ID, "triage")
	all := mustCreateServer(t, s, urlServer(team.ID, "all"))
	some := mustCreateServer(t, s, urlServer(team.ID, "some"))

	for _, g := range []db.AddAgentMCPServerParams{
		{AgentID: agent.ID, McpServerID: all.ID, TeamID: team.ID},
		{AgentID: agent.ID, McpServerID: some.ID, TeamID: team.ID, Allow: []string{"search", "read"}},
	} {
		if err := s.AddAgentMCPServer(t.Context(), g); err != nil {
			t.Fatalf("AddAgentMCPServer: %v", err)
		}
	}

	grants, err := s.ListAgentMCPServers(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("ListAgentMCPServers: %v", err)
	}
	for _, g := range grants {
		switch g.McpServerID {
		case all.ID:
			if g.Allow != nil {
				t.Errorf("an unrestricted grant came back with allow %v", g.Allow)
			}
		case some.ID:
			if !slices.Equal(g.Allow, []string{"search", "read"}) {
				t.Errorf("allow = %v, want [search read]", g.Allow)
			}
		}
	}
}

func TestClearingOneAgentsGrantsLeavesAnothers(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	a := mustCreateAgent(t, s, team.ID, "a")
	b := mustCreateAgent(t, s, team.ID, "b")
	srv := mustCreateServer(t, s, urlServer(team.ID, "github"))
	sk := mustCreateSkill(t, s, team.ID, "review")

	for _, agent := range []db.Agent{a, b} {
		mustGrant(t, s.AddAgentMCPServer(t.Context(), db.AddAgentMCPServerParams{
			AgentID: agent.ID, McpServerID: srv.ID, TeamID: team.ID,
		}))
		mustGrant(t, s.AddAgentSkills(t.Context(), db.AddAgentSkillsParams{
			AgentID: agent.ID, SkillIds: []uuid.UUID{sk.ID}, TeamID: team.ID,
		}))
	}

	if err := s.ClearAgentMCPServers(t.Context(), a.ID); err != nil {
		t.Fatalf("ClearAgentMCPServers: %v", err)
	}
	if err := s.ClearAgentSkills(t.Context(), a.ID); err != nil {
		t.Fatalf("ClearAgentSkills: %v", err)
	}

	if n := count(t, s, `SELECT count(*) FROM agent_mcp_servers WHERE agent_id = $1`, b.ID) +
		count(t, s, `SELECT count(*) FROM agent_skills WHERE agent_id = $1`, b.ID); n != 2 {
		t.Errorf("clearing a's grants left b with %d, want 2", n)
	}
}

// --- MCP servers ---

// A URL server has no command, and a nil slice must reach Postgres as NULL.
// If it arrived as '{}' every URL server would fail both the one-transport
// and the command-present checks.
func TestANilCommandIsStoredAsNull(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	srv := mustCreateServer(t, s, urlServer(team.ID, "github"))
	if srv.Command != nil {
		t.Errorf("command = %#v, want nil", srv.Command)
	}
	if n := count(t, s, `SELECT count(*) FROM mcp_servers WHERE command IS NULL`); n != 1 {
		t.Errorf("command is not NULL in the row")
	}
}

// env is NOT NULL and an explicit parameter bypasses the column default, so
// the handler must always send an object. This pins that the database, not
// the default, is what the handler is relying on.
func TestAServerWithNilEnvIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	p := urlServer(team.ID, "github")
	p.Env = nil
	_, err := s.CreateMCPServer(t.Context(), p)
	wantPGCode(t, err, notNullViolation, "a server with nil env")
}

func TestReplacingAServerKeepsItsDiscoveryWhileTheTransportIsTheSame(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	srv := mustCreateServer(t, s, urlServer(team.ID, "github"))
	if err := s.MarkMCPServerDiscovered(t.Context(), srv.ID); err != nil {
		t.Fatalf("MarkMCPServerDiscovered: %v", err)
	}

	got, err := s.UpdateMCPServer(t.Context(), db.UpdateMCPServerParams{
		ID: srv.ID, Name: "github-renamed", Url: srv.Url, Env: []byte(`{"TOKEN":"teams/x/mcp#token"}`),
	})
	if err != nil {
		t.Fatalf("UpdateMCPServer: %v", err)
	}
	if got.DiscoveredAt == nil {
		t.Error("renaming the server forgot that it had been discovered")
	}
}

// A server pointed somewhere else has not been discovered there. Both
// directions matter: a new URL, and a URL server turned into a command one —
// the second is the case plain = would get wrong, since command is NULL on
// one side.
func TestReplacingAServersTransportForgetsItsDiscovery(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	for name, change := range map[string]func(db.McpServer) db.UpdateMCPServerParams{
		"a new url": func(srv db.McpServer) db.UpdateMCPServerParams {
			return db.UpdateMCPServerParams{
				ID: srv.ID, Name: srv.Name,
				Url: ref("https://elsewhere.example.com"), Env: []byte(`{}`),
			}
		},
		"url to command": func(srv db.McpServer) db.UpdateMCPServerParams {
			return db.UpdateMCPServerParams{
				ID: srv.ID, Name: srv.Name,
				Command: []string{"npx", "server"}, Env: []byte(`{}`),
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := mustCreateServer(t, s, urlServer(team.ID, "s"+uuid.NewString()[:8]))
			if err := s.MarkMCPServerDiscovered(t.Context(), srv.ID); err != nil {
				t.Fatalf("MarkMCPServerDiscovered: %v", err)
			}

			got, err := s.UpdateMCPServer(t.Context(), change(srv))
			if err != nil {
				t.Fatalf("UpdateMCPServer: %v", err)
			}
			if got.DiscoveredAt != nil {
				t.Errorf("discovered_at survived %s", name)
			}
		})
	}
}

// --- tools ---

// Rediscovering a server that changed nothing must write nothing, or every
// discovery re-enqueues every tool and the projector hashes each one again.
func TestUpsertingAnUnchangedToolWritesNothing(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	srv := mustCreateServer(t, s, urlServer(team.ID, "github"))
	mustUpsertTool(t, s, srv, "search", "Search issues")

	var before time.Time
	if err := s.pool.QueryRow(t.Context(), `SELECT updated_at FROM mcp_tools`).Scan(&before); err != nil {
		t.Fatalf("read tool: %v", err)
	}
	clearOutbox(t, s)

	mustUpsertTool(t, s, srv, "search", "Search issues")

	var after time.Time
	if err := s.pool.QueryRow(t.Context(), `SELECT updated_at FROM mcp_tools`).Scan(&after); err != nil {
		t.Fatalf("read tool: %v", err)
	}
	if !after.Equal(before) {
		t.Error("an unchanged tool was rewritten")
	}
	if rows := outbox(t, s); len(rows) != 0 {
		t.Errorf("an unchanged tool left %v in the outbox", rows)
	}
}

func TestUpsertingAChangedToolUpdatesItInPlace(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	srv := mustCreateServer(t, s, urlServer(team.ID, "github"))
	mustUpsertTool(t, s, srv, "search", "Search issues")
	clearOutbox(t, s)

	mustUpsertTool(t, s, srv, "search", "Search issues and pull requests")

	tools, err := s.ListMCPToolsByServer(t.Context(), srv.ID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	if len(tools) != 1 || tools[0].Description != "Search issues and pull requests" {
		t.Errorf("tools = %+v, want one with the new description", tools)
	}
	if rows := outbox(t, s); len(rows) != 1 || rows[0].Entity != "mcp_tool" {
		t.Errorf("a changed tool left %v, want it once", rows)
	}
}

func TestDeletingToolsNotListedKeepsTheListedOnes(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	srv := mustCreateServer(t, s, urlServer(team.ID, "github"))
	other := mustCreateServer(t, s, urlServer(team.ID, "gitlab"))
	for _, n := range []string{"read", "search", "write"} {
		mustUpsertTool(t, s, srv, n, n)
	}
	mustUpsertTool(t, s, other, "search", "search")

	if err := s.DeleteMCPToolsNotIn(t.Context(), db.DeleteMCPToolsNotInParams{
		McpServerID: srv.ID, Names: []string{"search"},
	}); err != nil {
		t.Fatalf("DeleteMCPToolsNotIn: %v", err)
	}
	if got := toolNames(t, s, srv.ID); !slices.Equal(got, []string{"search"}) {
		t.Errorf("tools left = %v, want [search]", got)
	}

	if err := s.DeleteMCPToolsNotIn(t.Context(), db.DeleteMCPToolsNotInParams{
		McpServerID: srv.ID, Names: []string{},
	}); err != nil {
		t.Fatalf("DeleteMCPToolsNotIn with none: %v", err)
	}
	if got := toolNames(t, s, srv.ID); len(got) != 0 {
		t.Errorf("a server that lists nothing kept %v", got)
	}
	if got := toolNames(t, s, other.ID); !slices.Equal(got, []string{"search"}) {
		t.Errorf("another server's tools changed: %v", got)
	}
}

// --- skills ---

func TestReplacingASkillRewritesItsBody(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	sk := mustCreateSkill(t, s, team.ID, "review")

	got, err := s.UpdateSkill(t.Context(), db.UpdateSkillParams{
		ID: sk.ID, Name: "review", Description: "Review a pull request", Body: "# new",
	})
	if err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}
	if got.Body != "# new" || got.Description != "Review a pull request" {
		t.Errorf("after replace: %+v", got)
	}
}

func TestListingSkillsReturnsOnlyThisTeams(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	other := mustCreate(t, s, "support")
	mine := mustCreateSkill(t, s, team.ID, "review")
	mustCreateSkill(t, s, other.ID, "review")

	rows, err := s.ListSkillsByTeam(t.Context(), db.ListSkillsByTeamParams{TeamID: team.ID, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSkillsByTeam: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != mine.ID {
		t.Errorf("listed %+v, want only %s", rows, mine.ID)
	}
}

// --- the outbox ---

func claimIDs(rows []db.GraphOutbox) []int64 {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// A claim marks and commits; a claimed row is not offered again until
// retry_before passes it, and then comes back with its attempts counted.
func TestAClaimedRowWaitsForItsRetry(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	mustCreateAgent(t, s, team.ID, "triage")

	first, err := s.ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now().Add(-time.Minute)), BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("ClaimGraphOutbox: %v", err)
	}
	if len(first) != 1 || first[0].Attempts != 1 || first[0].LastAttemptAt == nil {
		t.Fatalf("first claim = %+v, want one row marked once", first)
	}

	again, err := s.ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now().Add(-time.Minute)), BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("ClaimGraphOutbox: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("a row claimed a moment ago was offered again: %+v", again)
	}

	later, err := s.ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now().Add(time.Minute)), BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("ClaimGraphOutbox: %v", err)
	}
	if len(later) != 1 || later[0].Attempts != 2 {
		t.Errorf("after its retry time = %+v, want the row with two attempts", later)
	}
}

// A row that keeps failing sinks behind work nobody has tried yet, so one
// poisoned entity cannot hold the rest of the queue back.
func TestNeverTriedRowsAreClaimedBeforeRetriedOnes(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	mustCreateAgent(t, s, team.ID, "poisoned")

	if _, err := s.ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now()), BatchSize: 10,
	}); err != nil {
		t.Fatalf("ClaimGraphOutbox: %v", err)
	}
	fresh := mustCreateAgent(t, s, team.ID, "fresh")

	rows, err := s.ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now().Add(time.Minute)), BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("ClaimGraphOutbox: %v", err)
	}
	if len(rows) != 1 || rows[0].EntityID != fresh.ID {
		t.Errorf("claimed %+v, want the never-tried row for %s first", rows, fresh.ID)
	}
}

// Two workers claiming at once get disjoint rows. The first claim is held
// open in a transaction so the second genuinely runs while its locks exist.
func TestConcurrentClaimsAreDisjoint(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	for _, n := range []string{"a", "b", "c", "d"} {
		mustCreateAgent(t, s, team.ID, n)
	}

	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	held, err := s.WithTx(tx).ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now()), BatchSize: 2,
	})
	if err != nil {
		t.Fatalf("claim in tx: %v", err)
	}

	other, err := s.ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now()), BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("concurrent claim: %v", err)
	}

	if len(held) != 2 || len(other) != 2 {
		t.Fatalf("claims of %d and %d, want 2 and 2", len(held), len(other))
	}
	for _, id := range claimIDs(held) {
		if slices.Contains(claimIDs(other), id) {
			t.Errorf("row %d was claimed by both", id)
		}
	}
}

// A change written after a claim has its own row, and forgetting the claimed
// ids must leave it — otherwise an edit made while the projector was reading
// is never projected.
func TestForgettingRemovesOnlyTheClaimedRows(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := mustCreateAgent(t, s, team.ID, "triage")

	claimed, err := s.ClaimGraphOutbox(t.Context(), db.ClaimGraphOutboxParams{
		RetryBefore: ref(time.Now()), BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("ClaimGraphOutbox: %v", err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET description = 'edited' WHERE id = $1`, agent.ID); err != nil {
		t.Fatalf("edit: %v", err)
	}

	if err := s.ForgetGraphOutbox(t.Context(), claimIDs(claimed)); err != nil {
		t.Fatalf("ForgetGraphOutbox: %v", err)
	}

	left := outbox(t, s)
	if len(left) != 1 || left[0].EntityID != agent.ID {
		t.Errorf("after forgetting the claim, outbox = %v, want the edit's row", left)
	}
}

func TestTheBacklogIsEmptyWhenNothingIsOwed(t *testing.T) {
	s := queryStore(t)

	rows, err := s.GraphOutboxBacklog(t.Context())
	if err != nil {
		t.Fatalf("GraphOutboxBacklog: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("backlog of an empty outbox = %+v, want no row", rows)
	}
}

func TestTheBacklogCountsEverythingAndNamesTheOldest(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	mustCreateAgent(t, s, team.ID, "a")
	mustCreateAgent(t, s, team.ID, "b")
	oldest := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE graph_outbox SET created_at = $1 WHERE id = (SELECT max(id) FROM graph_outbox)`, oldest); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rows, err := s.GraphOutboxBacklog(t.Context())
	if err != nil {
		t.Fatalf("GraphOutboxBacklog: %v", err)
	}
	if len(rows) != 1 || rows[0].Outstanding != 2 || !rows[0].Oldest.Equal(oldest) {
		t.Errorf("backlog = %+v, want 2 outstanding, oldest %s", rows, oldest)
	}
}

func TestListTeamIDsNamesEveryTeam(t *testing.T) {
	s := queryStore(t)
	a := mustCreate(t, s, "platform")
	b := mustCreate(t, s, "support")

	ids, err := s.ListTeamIDs(t.Context())
	if err != nil {
		t.Fatalf("ListTeamIDs: %v", err)
	}
	for _, want := range []uuid.UUID{a.ID, b.ID} {
		if !slices.Contains(ids, want) {
			t.Errorf("ListTeamIDs = %v, missing %s", ids, want)
		}
	}
}
