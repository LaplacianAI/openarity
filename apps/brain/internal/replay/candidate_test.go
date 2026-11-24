package replay

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (w *world) candidateFor(t *testing.T, after proposals.Snapshot, skill *uuid.UUID) *candidate {
	t.Helper()
	raw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	c, err := candidateOf(w.store, w.store, w.objects, db.ImprovementProposal{ID: uuid.New(), TeamID: w.team, AgentID: w.agent, SkillID: skill, After: raw}, db.Run{CreatedAt: time.Now()}, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A narrowed grant, a granted server and a tool note are tried as the
// proposal leaves them; the agent's own grants are left as they are.
func TestACandidateTriesGrantsAndNotes(t *testing.T) {
	w := newWorld(t)
	var crm, mail uuid.UUID
	w.scan(t, &crm, `INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'crm', 'https://crm.example/mcp') RETURNING id`, w.team)
	w.scan(t, &mail, `INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'mail', 'https://mail.example/mcp') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO mcp_tools (mcp_server_id, team_id, name) VALUES ($1, $3, 'lookup'), ($1, $3, 'cancel'), ($2, $3, 'send')`, crm, mail, w.team)
	w.exec(t, `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id) VALUES ($1, $2, $3)`, w.agent, crm, w.team)

	c := w.candidateFor(t, proposals.Snapshot{Agent: &proposals.AgentState{
		Agent: harness.Request{
			Name: "support-bot", Kind: harness.KindCustom, Model: harness.ModelRef{Name: "test/model", MaxTokens: 1024},
			Pattern: "react", MaxSteps: 5,
			MCPServers: []harness.ServerGrant{{ID: crm, Allow: []string{"lookup"}}, {ID: mail}},
		},
		Notes: []proposals.ToolNote{{Server: crm, Tool: "lookup", Note: "One lookup is enough."}},
	}}, nil)

	servers, err := c.ListGrantedMCPServers(t.Context(), w.agent)
	if err != nil || len(servers) != 2 || servers[0].McpServer.Name != "crm" || !slices.Equal(servers[0].Allow, []string{"lookup"}) {
		t.Fatalf("servers %+v, %v", servers, err)
	}
	tools, err := c.ListGrantedMCPTools(t.Context(), w.agent)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, []string{"lookup", "send"}) && !slices.Equal(names, []string{"send", "lookup"}) {
		t.Errorf("tools %v, want lookup narrowed in and send granted", names)
	}
	notes, err := c.ListAgentToolNotes(t.Context(), w.agent)
	if err != nil || len(notes) != 1 || notes[0].Note != "One lookup is enough." {
		t.Errorf("notes %+v, %v", notes, err)
	}

	stored, err := w.store.ListGrantedMCPTools(t.Context(), w.agent)
	if err != nil || len(stored) != 2 {
		t.Errorf("the agent's own tools are %d, %v; want both of crm's still", len(stored), err)
	}
	// Another agent's reads are its own.
	other := uuid.New()
	if s, err := c.ListGrantedMCPServers(t.Context(), other); err != nil || len(s) != 0 {
		t.Errorf("another agent read %+v, %v", s, err)
	}
}

// A skill edit is tried from the proposal's text, its unchanged files read
// as stored; nothing is written.
func TestACandidateTriesASkillEdit(t *testing.T) {
	w := newWorld(t)
	var skill uuid.UUID
	w.scan(t, &skill, `INSERT INTO skills (team_id, name, description, body) VALUES ($1, 'refunds', 'd', 'old body') RETURNING id`, w.team)
	w.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, team_id) VALUES ($1, $2, $3)`, w.agent, skill, w.team)
	kept, _ := hex.DecodeString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	old, _ := hex.DecodeString("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	w.exec(t, `INSERT INTO skill_files (skill_id, team_id, path, size, sha256, media_type, object_key) VALUES
		($1, $2, 'policy.md', 3, $3, 'text/markdown', 'objects/kept'),
		($1, $2, 'old.md', 3, $4, 'text/markdown', 'objects/old')`, skill, w.team, kept, old)
	text := "Refund within 30 days."
	c := w.candidateFor(t, proposals.Snapshot{Skill: &proposals.SkillState{
		Name: "refunds", Description: "d", Body: "new body", Files: []proposals.FileState{
			{Path: "policy.md", MediaType: "text/markdown", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{Path: "window.md", MediaType: "text/markdown", SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Text: &text},
		},
	}}, &skill)

	skills, err := c.ListGrantedSkills(t.Context(), w.agent)
	if err != nil || len(skills) != 1 || skills[0].Body != "new body" {
		t.Fatalf("skills %+v, %v", skills, err)
	}
	files, err := c.ListSkillFiles(t.Context(), skill)
	if err != nil || len(files) != 2 {
		t.Fatalf("files %+v, %v", files, err)
	}
	if files[0].ObjectKey != "objects/kept" {
		t.Errorf("files %+v", files)
	}
	// The tried file reads from its text; the kept one from the store.
	for key, want := range map[string]string{files[1].ObjectKey: text, files[0].ObjectKey: "kept"} {
		if b, err := c.Get(t.Context(), w.team, key); err != nil || string(b) != want {
			t.Errorf("Get(%s) = %q, %v; want %q", key, b, err, want)
		}
	}
	if _, err := c.Get(t.Context(), w.team, "replay/"+skill.String()+"/dddd"); err == nil {
		t.Error("a tried key the skill has no file for was read")
	}
	var body string
	w.scan(t, &body, `SELECT body FROM skills WHERE id = $1`, skill)
	if body != "old body" {
		t.Errorf("the stored skill's body is %q", body)
	}
}

// A compaction in the conversation since a run began makes it unreplayable.
func TestACompactedConversationIsNotTried(t *testing.T) {
	w := newWorld(t)
	s := w.session(t)
	bad := w.past(t, s, "2 hours", "not_done")
	later := w.past(t, s, "1 hour", "done")
	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload) VALUES ($1, 9, 'compacted', '{}')`, later)
	p := w.proposal(t, bad)
	if err := w.replays.Choose(t.Context(), p, w.team); err != nil {
		t.Fatal(err)
	}
	got := w.tried(t, p)
	if len(got) != 1 || got[0].status != "skipped" || got[0].skipWhy == nil || *got[0].skipWhy != "its conversation was compacted since it began, so it cannot be rebuilt as it was" {
		t.Errorf("tried %+v", got)
	}
}

// Every column an agent is made with but its team and kind reaches the tried
// agent: a new one that GetAgent does not copy fails here.
func TestEveryTriedColumnIsCopied(t *testing.T) {
	copied := map[string]bool{
		"Name": true, "Description": true, "Instructions": true, "ModelName": true, "MaxTokens": true,
		"Temperature": true, "Pattern": true, "MaxSteps": true, "SteerContinuations": true,
		"OutputSchema": true, "Parser": true, "ParserModelName": true, "ParserMaxTokens": true,
		"ParserTemperature": true, "TokenBudget": true, "Delegation": true, "Web": true,
		"Browser": true, "Workspace": true,
		// Not moved by a change.
		"TeamID": true, "Kind": true,
	}
	ty := reflect.TypeFor[db.CreateAgentParams]()
	for i := range ty.NumField() {
		if !copied[ty.Field(i).Name] {
			t.Errorf("CreateAgentParams.%s is not copied to the tried agent", ty.Field(i).Name)
		}
	}
}

// The files a replay can list and read are those sent before the past run
// began.
func TestACandidateSeesOnlyFilesSentBeforeTheRun(t *testing.T) {
	w := newWorld(t)
	began := time.Now().Add(-time.Hour)
	session, before, after := w.sessionWithFiles(t, began)
	c, err := candidateOf(w.store, w.store, w.objects, db.ImprovementProposal{ID: uuid.New(), TeamID: w.team, AgentID: w.agent, After: []byte(`{}`)},
		db.Run{CreatedAt: began.Add(-time.Minute), StartedAt: &began}, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.ListAttachmentsBySession(t.Context(), db.ListAttachmentsBySessionParams{SessionID: session, PageSize: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != before {
		t.Errorf("listed %+v, %v; want only the file sent before", rows, err)
	}
	if _, err := c.GetAttachmentInSession(t.Context(), db.GetAttachmentInSessionParams{ID: after, SessionID: session}); err == nil {
		t.Error("a file sent after the run began was read")
	}
	if _, err := c.GetAttachmentInSession(t.Context(), db.GetAttachmentInSessionParams{ID: before, SessionID: session}); err != nil {
		t.Errorf("the file sent before: %v", err)
	}
}

// sessionWithFiles is a conversation with one file sent an hour before
// began and one sent after it.
func (w *world) sessionWithFiles(t *testing.T, began time.Time) (session, before, after uuid.UUID) {
	t.Helper()
	session = w.session(t)
	var msg uuid.UUID
	w.scan(t, &msg, `INSERT INTO messages (channel_id, session_id, user_id, external_id, text, sender_name)
		VALUES ($1, $2, $3, $4, 'files', 'Asha') RETURNING id`, w.channel, session, w.user, uuid.NewString())
	for _, f := range []struct {
		id *uuid.UUID
		at time.Time
	}{{&before, began.Add(-time.Hour)}, {&after, began.Add(time.Minute)}} {
		w.scan(t, f.id, `INSERT INTO attachments (message_id, session_id, team_id, object_key, media_type, size_bytes, filename, created_at)
			VALUES ($1, $2, $3, $4, 'text/plain', 1, 'a.txt', $5) RETURNING id`, msg, session, w.team, uuid.NewString(), f.at)
	}
	return session, before, after
}
