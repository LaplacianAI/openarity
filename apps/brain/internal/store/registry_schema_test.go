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

	for _, tc := range []struct {
		index, sql string
	}{
		{"agents_team_name_key", `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
			VALUES ($1, 'triage', 'm', 1, 'react', 1)`},
		{"mcp_servers_team_name_key", `INSERT INTO mcp_servers (team_id, name, url)
			VALUES ($1, 'github', 'https://a')`},
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

// --- skills, as the Agent Skills specification shapes them ---

// The spec's name rule, all of it: a skill named any other way is one that
// Claude Code, Codex and Agno would all refuse to load, so it is refused here
// before it is stored.
func TestASkillNameFollowsTheSpec(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	for _, name := range []string{
		"Review", "-review", "review-", "re--view", "rev_iew", "re view", "",
		strings.Repeat("a", 65),
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(),
				`INSERT INTO skills (team_id, name, description, body) VALUES ($1, $2, 'd', 'b')`,
				team.ID, name)
			wantConstraint(t, err, checkViolation, "skills_name_is_spec_shaped")
		})
	}

	for _, name := range []string{"a", "review-a-pull-request", "pdf2", strings.Repeat("a", 64)} {
		insertSkill(t, s, team.ID, name)
	}
}

// Lower case by rule, so plain uniqueness is enough — and another team may
// use the same name.
func TestSkillNamesAreUniquePerTeam(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	other := mustCreate(t, s, "support")
	insertSkill(t, s, team.ID, "review")

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO skills (team_id, name, description, body) VALUES ($1, 'review', 'd', 'b')`, team.ID)
	wantConstraint(t, err, uniqueViolation, "skills_team_name_key")

	insertSkill(t, s, other.ID, "review")
}

func TestTheSpecsFieldLimitsAreRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	for constraint, sql := range map[string]string{
		"skills_description_present": `INSERT INTO skills (team_id, name, description, body)
			VALUES ($1, 'a', repeat('d', 1537), 'b')`,
		"skills_compatibility_shaped": `INSERT INTO skills (team_id, name, description, body, compatibility)
			VALUES ($1, 'a', 'd', 'b', repeat('c', 501))`,
		"skills_metadata_object": `INSERT INTO skills (team_id, name, description, body, metadata)
			VALUES ($1, 'a', 'd', 'b', '["not","a","map"]')`,
		"skills_license_present": `INSERT INTO skills (team_id, name, description, body, license)
			VALUES ($1, 'a', 'd', 'b', '')`,
	} {
		t.Run(constraint, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), sql, team.ID)
			wantConstraint(t, err, checkViolation, constraint)
		})
	}

	if _, err := s.pool.Exec(t.Context(), `INSERT INTO skills (team_id, name, description, body)
		VALUES ($1, 'at-the-limit', repeat('d', 1536), 'b')`, team.ID); err != nil {
		t.Errorf("a 1536-character description was refused: %v", err)
	}
}

// An upload has no origin; an import always records both where it came from
// and exactly what was taken. Every mixed combination is refused, because a
// half-recorded import is one sync cannot repeat and a team cannot audit.
func TestASkillsOriginIsRecordedWholeOrNotAtAll(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	insert := func(name, source string, ref, sha *string) error {
		_, err := s.pool.Exec(t.Context(), `
			INSERT INTO skills (team_id, name, description, body, source, source_ref, source_sha)
			VALUES ($1, $2, 'd', 'b', $3, $4, $5)`, team.ID, name, source, ref, sha)
		return err
	}
	ref, sha := "github:anthropics/skills/skills/pdf@main", "3f2c9e1"

	for name, tc := range map[string]struct {
		source   string
		ref, sha *string
	}{
		"an upload with a ref":   {"upload", &ref, nil},
		"an upload with a sha":   {"upload", nil, &sha},
		"an import with no ref":  {"github", nil, &sha},
		"an import with no sha":  {"github", &ref, nil},
		"an import with neither": {"url", nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			// A name per case, so one wrongly accepted row cannot turn the
			// rest into unique violations and hide how many were accepted.
			slug := strings.ReplaceAll(name, " ", "-")
			wantConstraint(t, insert(slug, tc.source, tc.ref, tc.sha), checkViolation, "skills_source_recorded")
		})
	}

	wantConstraint(t, insert("unknown-hub", "clawhub", &ref, &sha), checkViolation, "skills_source_known")

	if err := insert("uploaded", "upload", nil, nil); err != nil {
		t.Errorf("a plain upload was refused: %v", err)
	}
	if err := insert("imported", "github", &ref, &sha); err != nil {
		t.Errorf("a complete import was refused: %v", err)
	}
}

// Today's CreateSkill names none of the three columns; the default is what
// keeps it an upload rather than a refused row.
func TestASkillWithNoOriginIsAnUpload(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	insertSkill(t, s, team.ID, "review")

	if got := scalar[string](t, s, `SELECT source FROM skills WHERE name = 'review'`); got != "upload" {
		t.Errorf("source = %q, want upload", got)
	}
}

func insertSkillFile(t *testing.T, s *Store, skillID, teamID uuid.UUID, path, key string) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO skill_files (skill_id, team_id, path, size, sha256, media_type, object_key)
		VALUES ($1, $2, $3, 3, sha256('abc'), 'text/markdown', $4)`,
		skillID, teamID, path, key)
	return err
}

// The handler checks paths first; this is what stops anything else — a bulk
// import, a later migration, psql — from storing one that looks like a
// traversal to the first caller that joins it onto a directory.
func TestASkillFilePathStaysInsideTheSkill(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	skill := insertSkill(t, s, team.ID, "review")

	for _, path := range []string{
		"", "/etc/passwd", "references/", "../secret", "references/../../x",
		"references//forms.md", `references\forms.md`, "a/..", "..",
	} {
		t.Run("refuses "+path, func(t *testing.T) {
			wantConstraint(t, insertSkillFile(t, s, skill, team.ID, path, "k-"+path),
				checkViolation, "skill_files_path_is_inside")
		})
	}

	wantConstraint(t, insertSkillFile(t, s, skill, team.ID, "SKILL.md", "k-manifest"),
		checkViolation, "skill_files_not_the_manifest")

	for _, path := range []string{"forms.md", "references/forms.md", "scripts/fill.py", "a..b.md", ".hidden"} {
		if err := insertSkillFile(t, s, skill, team.ID, path, "ok-"+path); err != nil {
			t.Errorf("%q was refused: %v", path, err)
		}
	}
}

// There is no CHECK for a NUL because text cannot hold one: Postgres refuses
// it on the way in. This pins that, so the missing clause stays a decision
// rather than a gap — a clause calling chr(0) broke every insert once.
func TestASkillFilePathWithANulIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	skill := insertSkill(t, s, team.ID, "review")

	if err := insertSkillFile(t, s, skill, team.ID, "forms\x00.md", "k"); err == nil {
		t.Error("a path with a NUL byte was stored")
	}
}

func TestASkillFileCannotNameATeamItsSkillIsNotIn(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	other := mustCreate(t, s, "support")
	skill := insertSkill(t, s, team.ID, "review")

	wantConstraint(t, insertSkillFile(t, s, skill, other.ID, "forms.md", "k"),
		foreignKeyViolation, "skill_files_skill_in_team")
}

func tombstoned(t *testing.T, s *Store, key string) (uuid.UUID, bool) {
	t.Helper()

	var team uuid.UUID
	err := s.pool.QueryRow(t.Context(), `SELECT team_id FROM deleted_objects WHERE object_key = $1`, key).Scan(&team)
	if err != nil {
		return uuid.Nil, false
	}
	return team, true
}

// A deleted skill's files are ciphertext in a bucket no cascade reaches. The
// tombstone is what makes the reaper delete them — for a skill deleted
// directly and for one taken with its team, which runs no Go at all.
func TestDeletingASkillTombstonesItsFiles(t *testing.T) {
	s := queryStore(t)

	for name, del := range map[string]string{
		"the skill": `DELETE FROM skills WHERE id = $1`,
		"the team":  `DELETE FROM teams WHERE id = (SELECT team_id FROM skills WHERE id = $1)`,
	} {
		t.Run(name, func(t *testing.T) {
			team := mustCreate(t, s, "t-"+uuid.NewString()[:8])
			skill := insertSkill(t, s, team.ID, "review")
			keys := []string{"teams/" + team.ID.String() + "/objects/1", "teams/" + team.ID.String() + "/objects/2"}
			for i, k := range keys {
				if err := insertSkillFile(t, s, skill, team.ID, "f"+string(rune('a'+i))+".md", k); err != nil {
					t.Fatalf("insert file: %v", err)
				}
			}

			if _, err := s.pool.Exec(t.Context(), del, skill); err != nil {
				t.Fatalf("delete %s: %v", name, err)
			}

			for _, k := range keys {
				got, ok := tombstoned(t, s, k)
				if !ok {
					t.Errorf("%s left no tombstone after deleting %s", k, name)
				} else if got != team.ID {
					t.Errorf("tombstone for %s names team %s, want %s", k, got, team.ID)
				}
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
