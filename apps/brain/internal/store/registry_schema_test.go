package store

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// wantConstraint is wantPGCode that also names the rule. Two CHECKs on one
// table share a code, so a test asserting only 23514 passes when the wrong
// one fired — including when the one under test was dropped entirely.
func wantConstraint(t *testing.T, err error, code, constraint string) {
	t.Helper()

	if err == nil {
		t.Fatalf("%s: the database accepted it", constraint)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: error is not a PgError: %v", constraint, err)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Errorf("SQLSTATE %s on %q (%s), want %s on %q",
			pgErr.Code, pgErr.ConstraintName, pgErr.Message, code, constraint)
	}
}

func insertAgent(t *testing.T, s *Store, teamID uuid.UUID, name string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, $2, 'test/model', 1024, 'react', 5) RETURNING id`,
		teamID, name).Scan(&id); err != nil {
		t.Fatalf("insert agent %q: %v", name, err)
	}
	return id
}

func insertServer(t *testing.T, s *Store, teamID uuid.UUID, name string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `
		INSERT INTO mcp_servers (team_id, name, url)
		VALUES ($1, $2, 'https://mcp.example.com') RETURNING id`,
		teamID, name).Scan(&id); err != nil {
		t.Fatalf("insert server %q: %v", name, err)
	}
	return id
}

func insertSkill(t *testing.T, s *Store, teamID uuid.UUID, name string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `
		INSERT INTO skills (team_id, name, description, body)
		VALUES ($1, $2, 'Does a thing', '# body') RETURNING id`,
		teamID, name).Scan(&id); err != nil {
		t.Fatalf("insert skill %q: %v", name, err)
	}
	return id
}

func insertTool(t *testing.T, s *Store, serverID, teamID uuid.UUID, name string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `
		INSERT INTO mcp_tools (mcp_server_id, team_id, name)
		VALUES ($1, $2, $3) RETURNING id`,
		serverID, teamID, name).Scan(&id); err != nil {
		t.Fatalf("insert tool %q: %v", name, err)
	}
	return id
}

func grantServer(t *testing.T, s *Store, teamID, agentID, serverID uuid.UUID) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id)
		VALUES ($1, $2, $3)`, agentID, serverID, teamID)
	return err
}

func grantSkill(t *testing.T, s *Store, teamID, agentID, skillID uuid.UUID) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agent_skills (agent_id, skill_id, team_id)
		VALUES ($1, $2, $3)`, agentID, skillID, teamID)
	return err
}

func mustGrant(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
}

type outboxRow struct {
	TeamID   uuid.UUID
	Entity   string
	EntityID uuid.UUID
}

func (r outboxRow) String() string {
	return r.Entity + ":" + r.EntityID.String()
}

func outbox(t *testing.T, s *Store) []outboxRow {
	t.Helper()

	rows, err := s.pool.Query(t.Context(),
		`SELECT team_id, entity, entity_id FROM graph_outbox ORDER BY id`)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()

	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.TeamID, &r.Entity, &r.EntityID); err != nil {
			t.Fatalf("scan outbox: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	return out
}

func clearOutbox(t *testing.T, s *Store) {
	t.Helper()
	exec(t, s, `DELETE FROM graph_outbox`)
}

func count(t *testing.T, s *Store, sql string, args ...any) int {
	t.Helper()

	var n int
	if err := s.pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %.40s…: %v", sql, err)
	}
	return n
}

// --- what a row may hold ---

// mcp.Connect refuses a server with both or neither, so a row that has either
// is a server that can never be discovered or run.
func TestAnMCPServerNeedsExactlyOneTransport(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	for name, sql := range map[string]string{
		"both":    `INSERT INTO mcp_servers (team_id, name, url, command) VALUES ($1, 'x', 'https://a', '{npx}')`,
		"neither": `INSERT INTO mcp_servers (team_id, name) VALUES ($1, 'x')`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), sql, team.ID)
			wantConstraint(t, err, checkViolation, "mcp_servers_one_transport")
		})
	}

	for name, sql := range map[string]string{
		"url":     `INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'by-url', 'https://a')`,
		"command": `INSERT INTO mcp_servers (team_id, name, command) VALUES ($1, 'by-command', '{npx,server}')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.pool.Exec(t.Context(), sql, team.ID); err != nil {
				t.Errorf("a server with only a %s was refused: %v", name, err)
			}
		})
	}
}

// The SDK prefixes each tool as server__tool and refuses any name outside
// this set, so a server named like this has tools that never load.
func TestAServerNameThatCannotPrefixAToolIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	for _, name := range []string{"my server", "git.hub", strings.Repeat("a", 65), ""} {
		t.Run(name, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(),
				`INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, $2, 'https://a')`,
				team.ID, name)
			wantConstraint(t, err, checkViolation, "mcp_servers_name_is_prefix")
		})
	}

	insertServer(t, s, team.ID, "git-hub_2")
}

// The SDK refuses a parser with no schema when the run starts. Refusing it
// here means an agent that cannot run is never saved.
func TestAParserWithNoOutputSchemaIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps, parser)
		VALUES ($1, 'a', 'm', 1, 'react', 1, true)`, team.ID)
	wantConstraint(t, err, checkViolation, "agents_parser_needs_schema")
}

// A parser max_tokens with no parser model would be read by nothing.
func TestParserSettingsWithNoParserModelAreRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps, parser_max_tokens)
		VALUES ($1, 'a', 'm', 1, 'react', 1, 256)`, team.ID)
	wantConstraint(t, err, checkViolation, "agents_parser_settings_need_a_model")
}

func TestAnUnknownKindOrPatternIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps, kind)
		VALUES ($1, 'a', 'm', 1, 'react', 1, 'planner')`, team.ID)
	wantConstraint(t, err, checkViolation, "agents_kind_known")

	_, err = s.pool.Exec(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'a', 'm', 1, 'ReAct', 1)`, team.ID)
	wantConstraint(t, err, checkViolation, "agents_pattern_known")
}

// An empty list would grant a server and none of its tools, which is a
// grant that looks like access and is not.
func TestAnEmptyAllowListIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id, allow)
		VALUES ($1, $2, $3, '{}')`, agent, server, team.ID)
	wantConstraint(t, err, checkViolation, "agent_mcp_servers_allow_not_empty")
}

// A name is how a person refers to it on the command line, and "Triage" and
// "triage" being two agents is a trap. Another team may use the same name.
func TestNamesAreUniquePerTeamInAnyCase(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	other := mustCreate(t, s, "support")

	insertAgent(t, s, team.ID, "Triage")
	insertServer(t, s, team.ID, "GitHub")
	insertSkill(t, s, team.ID, "Review")

	for _, tc := range []struct {
		index, sql string
	}{
		{"agents_team_name_key", `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
			VALUES ($1, 'triage', 'm', 1, 'react', 1)`},
		{"mcp_servers_team_name_key", `INSERT INTO mcp_servers (team_id, name, url)
			VALUES ($1, 'github', 'https://a')`},
		{"skills_team_name_key", `INSERT INTO skills (team_id, name, description, body)
			VALUES ($1, 'review', 'd', 'b')`},
	} {
		t.Run(tc.index, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), tc.sql, team.ID)
			wantConstraint(t, err, uniqueViolation, tc.index)

			if _, err := s.pool.Exec(t.Context(), tc.sql, other.ID); err != nil {
				t.Errorf("another team could not use the name: %v", err)
			}
		})
	}
}

// --- what a row may point at ---

// The composite key is what stops a discovery for one team's server writing
// tools into another team's graph.
func TestAToolCannotNameATeamItsServerIsNotIn(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	other := mustCreate(t, s, "support")
	server := insertServer(t, s, team.ID, "github")

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO mcp_tools (mcp_server_id, team_id, name) VALUES ($1, $2, 'search')`,
		server, other.ID)
	wantConstraint(t, err, foreignKeyViolation, "mcp_tools_server_in_team")
}

// Postgres, not a handler, refuses a grant across teams. Whichever team the
// row claims, one of its two keys does not match.
func TestAGrantAcrossTeamsIsRefused(t *testing.T) {
	s := queryStore(t)
	a := mustCreate(t, s, "platform")
	b := mustCreate(t, s, "support")

	agent := insertAgent(t, s, a.ID, "triage")
	server := insertServer(t, s, b.ID, "github")
	skill := insertSkill(t, s, b.ID, "review")

	wantConstraint(t, grantServer(t, s, a.ID, agent, server),
		foreignKeyViolation, "agent_mcp_servers_server_in_team")
	wantConstraint(t, grantServer(t, s, b.ID, agent, server),
		foreignKeyViolation, "agent_mcp_servers_agent_in_team")
	wantConstraint(t, grantSkill(t, s, a.ID, agent, skill),
		foreignKeyViolation, "agent_skills_skill_in_team")
	wantConstraint(t, grantSkill(t, s, b.ID, agent, skill),
		foreignKeyViolation, "agent_skills_agent_in_team")

	if n := count(t, s, `SELECT count(*) FROM agent_mcp_servers`) +
		count(t, s, `SELECT count(*) FROM agent_skills`); n != 0 {
		t.Errorf("%d grants were written across teams", n)
	}
}

// --- what a delete does ---

// A silent detach would change what an agent can do without anyone changing
// the agent. RESTRICT's 23001, distinct from the 23503 a bad reference raises,
// is what lets the handler say which one happened.
func TestDeletingAGrantedServerOrSkillIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")
	skill := insertSkill(t, s, team.ID, "review")
	mustGrant(t, grantServer(t, s, team.ID, agent, server))
	mustGrant(t, grantSkill(t, s, team.ID, agent, skill))

	_, err := s.pool.Exec(t.Context(), `DELETE FROM mcp_servers WHERE id = $1`, server)
	wantConstraint(t, err, restrictViolation, "agent_mcp_servers_server_in_team")

	_, err = s.pool.Exec(t.Context(), `DELETE FROM skills WHERE id = $1`, skill)
	wantConstraint(t, err, restrictViolation, "agent_skills_skill_in_team")

	if n := count(t, s, `SELECT count(*) FROM mcp_servers WHERE id = $1`, server); n != 1 {
		t.Error("the refused delete removed the server anyway")
	}
	if n := count(t, s, `SELECT count(*) FROM skills WHERE id = $1`, skill); n != 1 {
		t.Error("the refused delete removed the skill anyway")
	}
}

func TestDeletingAnUngrantedServerTakesItsTools(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	server := insertServer(t, s, team.ID, "github")
	insertTool(t, s, server, team.ID, "search")

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM mcp_servers WHERE id = $1`, server); err != nil {
		t.Fatalf("delete an ungranted server: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM mcp_tools`); n != 0 {
		t.Errorf("%d tools outlived their server", n)
	}
}

func TestDeletingAnAgentDropsItsGrantsAndNothingElse(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")
	skill := insertSkill(t, s, team.ID, "review")
	mustGrant(t, grantServer(t, s, team.ID, agent, server))
	mustGrant(t, grantSkill(t, s, team.ID, agent, skill))

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1`, agent); err != nil {
		t.Fatalf("delete agent: %v", err)
	}

	if n := count(t, s, `SELECT count(*) FROM agent_mcp_servers`) +
		count(t, s, `SELECT count(*) FROM agent_skills`); n != 0 {
		t.Errorf("%d grants outlived their agent", n)
	}
	if n := count(t, s, `SELECT count(*) FROM mcp_servers`) +
		count(t, s, `SELECT count(*) FROM skills`); n != 2 {
		t.Errorf("deleting the agent took what it was granted with it")
	}
}

// A team delete reaches servers and grants through two cascades, and RESTRICT
// refuses if a server goes while a grant still names it. It passes only
// because agents was created before mcp_servers and skills, so Postgres runs
// the agents cascade — which takes the grants — first. This test is what
// fails if a later migration ever reorders them; NO ACTION would not help,
// since each cascade is checked when it finishes.
func TestDeletingATeamWithGrantsSucceeds(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")
	insertTool(t, s, server, team.ID, "search")
	skill := insertSkill(t, s, team.ID, "review")
	mustGrant(t, grantServer(t, s, team.ID, agent, server))
	mustGrant(t, grantSkill(t, s, team.ID, agent, skill))

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("a team whose agent holds grants could not be deleted: %v", err)
	}

	for _, table := range []string{"agents", "mcp_servers", "mcp_tools", "skills", "agent_mcp_servers", "agent_skills"} {
		if n := count(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%d rows left in %s", n, table)
		}
	}
}

// --- the outbox ---

// Every change the graph must follow leaves a row naming it, including the
// ones no Go code runs for.
func TestEveryWriteLeavesAnOutboxRow(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")
	tool := insertTool(t, s, server, team.ID, "search")
	skill := insertSkill(t, s, team.ID, "review")
	spare := insertSkill(t, s, team.ID, "spare")

	for _, tc := range []struct {
		name string
		sql  string
		arg  uuid.UUID
		want outboxRow
	}{
		{"agent update", `UPDATE agents SET description = 'x' WHERE id = $1`, agent, outboxRow{team.ID, "agent", agent}},
		{"server update", `UPDATE mcp_servers SET bare = true WHERE id = $1`, server, outboxRow{team.ID, "mcp_server", server}},
		{"tool update", `UPDATE mcp_tools SET description = 'x' WHERE id = $1`, tool, outboxRow{team.ID, "mcp_tool", tool}},
		{"skill update", `UPDATE skills SET description = 'x' WHERE id = $1`, skill, outboxRow{team.ID, "skill", skill}},
		{"server grant", `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id)
			SELECT $1, id, team_id FROM mcp_servers`, agent, outboxRow{team.ID, "agent", agent}},
		{"allow change", `UPDATE agent_mcp_servers SET allow = '{search}' WHERE agent_id = $1`, agent, outboxRow{team.ID, "agent", agent}},
		{"skill grant", `INSERT INTO agent_skills (agent_id, skill_id, team_id)
			SELECT $1, id, team_id FROM skills WHERE name = 'review'`, agent, outboxRow{team.ID, "agent", agent}},
		{"skill revoke", `DELETE FROM agent_skills WHERE agent_id = $1`, agent, outboxRow{team.ID, "agent", agent}},
		{"server revoke", `DELETE FROM agent_mcp_servers WHERE agent_id = $1`, agent, outboxRow{team.ID, "agent", agent}},
		{"tool delete", `DELETE FROM mcp_tools WHERE id = $1`, tool, outboxRow{team.ID, "mcp_tool", tool}},
		{"skill delete", `DELETE FROM skills WHERE id = $1`, spare, outboxRow{team.ID, "skill", spare}},
		{"agent delete", `DELETE FROM agents WHERE id = $1`, agent, outboxRow{team.ID, "agent", agent}},
		{"server delete", `DELETE FROM mcp_servers WHERE id = $1`, server, outboxRow{team.ID, "mcp_server", server}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearOutbox(t, s)

			if _, err := s.pool.Exec(t.Context(), tc.sql, tc.arg); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			got := outbox(t, s)
			if !slices.Contains(got, tc.want) {
				t.Errorf("outbox %v does not name %v", got, tc.want)
			}
		})
	}
}

// Inserts are covered separately: the rows above were inserted before the
// outbox was cleared, so they say nothing about the insert triggers.
func TestAnInsertLeavesAnOutboxRow(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")
	tool := insertTool(t, s, server, team.ID, "search")
	skill := insertSkill(t, s, team.ID, "review")

	got := outbox(t, s)
	for _, want := range []outboxRow{
		{team.ID, "agent", agent},
		{team.ID, "mcp_server", server},
		{team.ID, "mcp_tool", tool},
		{team.ID, "skill", skill},
	} {
		if !slices.Contains(got, want) {
			t.Errorf("outbox %v does not name %v", got, want)
		}
	}
}

// The cascade no Go code sees. Every entity the team owned is named, and so
// is the team, whose graph is dropped whole.
func TestDeletingATeamNamesEverythingItTookWithIt(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")
	tool := insertTool(t, s, server, team.ID, "search")
	skill := insertSkill(t, s, team.ID, "review")
	mustGrant(t, grantServer(t, s, team.ID, agent, server))
	clearOutbox(t, s)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("delete team: %v", err)
	}

	got := outbox(t, s)
	for _, want := range []outboxRow{
		{team.ID, "team", team.ID},
		{team.ID, "agent", agent},
		{team.ID, "mcp_server", server},
		{team.ID, "mcp_tool", tool},
		{team.ID, "skill", skill},
	} {
		if !slices.Contains(got, want) {
			t.Errorf("outbox %v does not name %v", got, want)
		}
	}
}

// The outbox commits with the change or not at all. A refused write that
// left a row would make the projector look for something that never existed.
func TestARefusedWriteLeavesNoOutboxRow(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := insertAgent(t, s, team.ID, "triage")
	server := insertServer(t, s, team.ID, "github")
	mustGrant(t, grantServer(t, s, team.ID, agent, server))
	clearOutbox(t, s)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM mcp_servers WHERE id = $1`, server); err == nil {
		t.Fatal("a granted server was deleted")
	}
	if _, err := s.pool.Exec(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'x', 'm', 0, 'react', 1)`, team.ID); err == nil {
		t.Fatal("an agent with max_tokens 0 was saved")
	}

	if got := outbox(t, s); len(got) != 0 {
		t.Errorf("refused writes left %v in the outbox", got)
	}
}

// One statement over many rows is one trigger call, and each row is named
// once — granting three servers to one agent is one agent to re-project.
func TestOneStatementNamesEachRowOnce(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	agent := insertAgent(t, s, team.ID, "triage")
	insertAgent(t, s, team.ID, "review")
	for _, name := range []string{"a", "b", "c"} {
		insertServer(t, s, team.ID, name)
	}
	clearOutbox(t, s)

	if _, err := s.pool.Exec(t.Context(), `
		INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id)
		SELECT $1, id, team_id FROM mcp_servers`, agent); err != nil {
		t.Fatalf("grant three: %v", err)
	}
	if got := outbox(t, s); len(got) != 1 || got[0] != (outboxRow{team.ID, "agent", agent}) {
		t.Errorf("three grants to one agent left %v, want the agent once", got)
	}

	clearOutbox(t, s)
	exec(t, s, `UPDATE agents SET description = 'x'`)
	if got := outbox(t, s); len(got) != 2 {
		t.Errorf("updating two agents left %v, want two rows", got)
	}
}
