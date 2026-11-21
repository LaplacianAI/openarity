package improvements

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/skills"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

var loops = []string{"plan", "react"}

type world struct {
	store   *store.Store
	raw     *pgx.Conn
	team    uuid.UUID
	user    uuid.UUID
	holds   map[authz.Action]bool
	objects *memObjects
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dsn := storetest.Migrated(t, func(ctx context.Context, dsn string) error {
		s, err := store.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer s.Close()
		_, err = s.Migrate(ctx)
		return err
	})
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	raw, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })

	w := &world{
		store: s, raw: raw, objects: &memObjects{m: map[string][]byte{}},
		holds: map[authz.Action]bool{approveAction: true, agentWrite: true, skillWrite: true},
	}
	if err := raw.QueryRow(t.Context(), `INSERT INTO teams (name) VALUES ('support') RETURNING id`).Scan(&w.team); err != nil {
		t.Fatalf("team: %v", err)
	}
	if err := raw.QueryRow(t.Context(), `INSERT INTO users (issuer, subject) VALUES ('dev', 'someone') RETURNING id`).Scan(&w.user); err != nil {
		t.Fatalf("user: %v", err)
	}
	return w
}

type memObjects struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (o *memObjects) Put(_ context.Context, _ uuid.UUID, key string, body []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = bytes.Clone(body)
	return nil
}

func (o *memObjects) Get(_ context.Context, _ uuid.UUID, key string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, ok := o.m[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return bytes.Clone(b), nil
}

type fakeAuthz struct{ holds map[authz.Action]bool }

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }
func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	return f.holds[a], nil
}

func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	return false, nil
}

type liveTeams struct{}

func (liveTeams) TeamDeleted(context.Context, uuid.UUID) (bool, error) { return false, nil }

func (w *world) serve(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, "/teams/"+w.team.String()+path, rd)
	req.Header.Set("Content-Type", "application/json")

	rs := authz.NewRoutes()
	for _, r := range []struct{ method, path string }{
		{"GET", "/teams/{id}/improvements"},
		{"GET", "/teams/{id}/improvements/passes"},
		{"GET", "/teams/{id}/improvements/{proposalID}"},
		{"POST", "/teams/{id}/improvements/{proposalID}/decision"},
	} {
		if err := rs.Add(r.method, r.path, "member", nil); err != nil {
			t.Fatalf("route: %v", err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	az := &fakeAuthz{holds: w.holds}
	New(logger, w.store, az, NewDecider(w.store, w.objects, loops)).Register(mux, api.NewGuard(rs, az, liveTeams{}, logger))

	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{
		ID: w.user, Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: w.team, Name: "support", Role: "member"}},
	}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (w *world) agent(t *testing.T, name string) db.Agent {
	t.Helper()
	req := agents.Request{
		Name: name, Description: "answers support tickets", Kind: agents.KindCustom,
		Instructions: "Answer the customer, then close the ticket.",
		Model:        agents.ModelRef{Name: "m", MaxTokens: 4000}, Pattern: "react", MaxSteps: 10,
	}
	params, err := agents.Columns(req)
	if err != nil {
		t.Fatal(err)
	}
	params.TeamID, params.Kind = w.team, string(agents.KindCustom)
	row, err := w.store.CreateAgent(t.Context(), params)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	return row
}

func (w *world) now(t *testing.T, a db.Agent) AgentState {
	t.Helper()
	var st AgentState
	err := w.store.InTx(t.Context(), func(q *db.Queries) error {
		row, err := q.GetAgent(t.Context(), a.ID)
		if err != nil {
			return err
		}
		st, err = AgentNow(t.Context(), q, row)
		return err
	})
	if err != nil {
		t.Fatalf("agent state: %v", err)
	}
	return st
}

func (w *world) propose(t *testing.T, p Proposed) (db.ImprovementProposal, error) {
	t.Helper()
	if p.TeamID == uuid.Nil {
		p.TeamID = w.team
	}
	if p.Why == "" {
		p.Why = "closed tickets before confirming the order id (4 runs)"
	}
	if p.Hypothesis == "" {
		p.Hypothesis = "tickets close only after the order id is confirmed"
	}
	if p.Scope == "" {
		p.Scope = "invariant"
	}
	var row db.ImprovementProposal
	err := w.store.InTx(t.Context(), func(q *db.Queries) error {
		var err error
		row, err = Create(t.Context(), q, p)
		return err
	})
	return row, err
}

func (w *world) mustPropose(t *testing.T, p Proposed) db.ImprovementProposal {
	t.Helper()
	row, err := w.propose(t, p)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	return row
}

// instructionsChange is the standard proposal: one line of an agent's
// instructions changed.
func (w *world) instructionsChange(t *testing.T, a db.Agent) Proposed {
	t.Helper()
	before := w.now(t, a)
	after := w.now(t, a)
	after.Agent.Instructions = "Confirm the order id with the customer before closing the ticket."
	return Proposed{AgentID: a.ID, Kind: KindInstructions, Before: Snapshot{Agent: &before}, After: Snapshot{Agent: &after}}
}

func (w *world) decide(t *testing.T, id uuid.UUID, action string) *httptest.ResponseRecorder {
	t.Helper()
	return w.serve(t, "POST", "/improvements/"+id.String()+"/decision", map[string]string{"action": action})
}

func (w *world) status(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := w.raw.QueryRow(t.Context(), `SELECT status FROM improvement_proposals WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (w *world) instructions(t *testing.T, a db.Agent) string {
	t.Helper()
	var s string
	if err := w.raw.QueryRow(t.Context(), `SELECT instructions FROM agents WHERE id = $1`, a.ID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Approving writes the change through the agents' own write path, and
// reverting puts the old instructions back.
func TestApproveAppliesAndRevertRestores(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))

	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if got := w.instructions(t, a); !strings.HasPrefix(got, "Confirm the order id") {
		t.Errorf("after approve, instructions = %q", got)
	}
	if s := w.status(t, p.ID); s != "applied" {
		t.Errorf("status = %s, want applied", s)
	}

	if rec := w.decide(t, p.ID, "revert"); rec.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", rec.Code, rec.Body)
	}
	if got := w.instructions(t, a); got != "Answer the customer, then close the ticket." {
		t.Errorf("after revert, instructions = %q", got)
	}
	if s := w.status(t, p.ID); s != "reverted" {
		t.Errorf("status = %s, want reverted", s)
	}
}

// An agent a person edited since the proposal was written is not
// overwritten: the approve is a 409 and nothing changes.
func TestApproveOfAChangedAgentIsRefused(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))

	if _, err := w.raw.Exec(t.Context(), `UPDATE agents SET instructions = 'by hand' WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	rec := w.decide(t, p.ID, "approve")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "changed since") {
		t.Fatalf("approve = %d %s, want 409 changed since", rec.Code, rec.Body)
	}
	if got := w.instructions(t, a); got != "by hand" {
		t.Errorf("instructions = %q, want the person's edit kept", got)
	}
	if s := w.status(t, p.ID); s != "waiting" {
		t.Errorf("status = %s, want still waiting", s)
	}
}

// A revert after a person's later edit would undo that edit too, so it is
// refused.
func TestRevertAfterAnEditIsRefused(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if _, err := w.raw.Exec(t.Context(), `UPDATE agents SET max_steps = 12 WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if rec := w.decide(t, p.ID, "revert"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "cannot be put back") {
		t.Fatalf("revert = %d %s, want 409 saying it cannot be put back", rec.Code, rec.Body)
	}
	if s := w.status(t, p.ID); s != "applied" {
		t.Errorf("status = %s, want still applied", s)
	}
}

// Deciding a change needs what making it by hand needs.
func TestApproveWithoutTheWritePermissionIsForbidden(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	delete(w.holds, agentWrite)

	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusForbidden {
		t.Fatalf("approve = %d, want 403", rec.Code)
	}
	if got := w.instructions(t, a); got != "Answer the customer, then close the ticket." {
		t.Errorf("instructions changed to %q without agent:write", got)
	}
}

// Waiting proposals quote runs others may not read: someone who may not
// approve neither lists nor opens them.
func TestWaitingIsHiddenFromThoseWhoMayNotApprove(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	w.holds = map[authz.Action]bool{}

	if rec := w.serve(t, "GET", "/improvements?status=waiting", nil); rec.Code != http.StatusForbidden {
		t.Errorf("list waiting = %d, want 403", rec.Code)
	}
	if rec := w.serve(t, "GET", "/improvements/"+p.ID.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("get waiting = %d, want 404", rec.Code)
	}
	rec := w.serve(t, "GET", "/improvements", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), p.ID.String()) {
		t.Errorf("default list = %d %s, want applied only", rec.Code, rec.Body)
	}
}

// An approver's list shows the proposal with its diff in names.
func TestListShowsTheChange(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))

	rec := w.serve(t, "GET", "/improvements", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var page struct{ Items []view }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != p.ID || page.Items[0].Agent.Name != "support-bot" {
		t.Fatalf("items = %+v", page.Items)
	}
	ch := page.Items[0].Changes
	if len(ch) != 1 || ch[0].Field != "instructions" || ch[0].Type != "text" {
		t.Errorf("changes = %+v, want the instructions as text", ch)
	}
}

// Approving one proposal withdraws the others waiting on the same agent,
// which it has made stale.
func TestApproveWithdrawsTheOthers(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	first := w.mustPropose(t, w.instructionsChange(t, a))
	before := w.now(t, a)
	after := w.now(t, a)
	after.Agent.MaxSteps = 20
	second := w.mustPropose(t, Proposed{AgentID: a.ID, Kind: KindLimits, Before: Snapshot{Agent: &before}, After: Snapshot{Agent: &after}})

	if rec := w.decide(t, first.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if s := w.status(t, second.ID); s != "withdrawn" {
		t.Errorf("the other proposal is %s, want withdrawn", s)
	}
}

// Two approvals at once: one applies, the other finds it decided.
func TestTwoApprovalsAtOnce(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))

	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { codes <- w.decide(t, p.ID, "approve").Code })
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[http.StatusOK] != 1 || got[http.StatusConflict] != 1 {
		t.Errorf("codes = %v, want one 200 and one 409", got)
	}
}

// A rejected proposal is kept, and cannot then be approved.
func TestRejectKeepsItAndEndsIt(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	if rec := w.decide(t, p.ID, "reject"); rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusConflict {
		t.Errorf("approve after reject = %d, want 409", rec.Code)
	}
	if got := w.instructions(t, a); got != "Answer the customer, then close the ticket." {
		t.Errorf("a rejected proposal changed the agent: %q", got)
	}
}

// Every kind may change only its own fields, and never the model, the name
// or a built-in agent.
func TestCreateHoldsAProposalToItsKind(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")

	for name, change := range map[string]func(*AgentState){
		"model":        func(s *AgentState) { s.Agent.Instructions = "x"; s.Agent.Model.Name = "other" },
		"name":         func(s *AgentState) { s.Agent.Instructions = "x"; s.Agent.Name = "renamed" },
		"other column": func(s *AgentState) { s.Agent.Instructions = "x"; s.Agent.MaxSteps = 99 },
		"nothing":      func(*AgentState) {},
	} {
		t.Run(name, func(t *testing.T) {
			before, after := w.now(t, a), w.now(t, a)
			change(&after)
			_, err := w.propose(t, Proposed{AgentID: a.ID, Kind: KindInstructions, Before: Snapshot{Agent: &before}, After: Snapshot{Agent: &after}})
			var rule ruleError
			if !errorsAs(err, &rule) {
				t.Errorf("err = %v, want a refused rule", err)
			}
		})
	}

	t.Run("built-in", func(t *testing.T) {
		before, after := w.now(t, a), w.now(t, a)
		before.Agent.Kind, after.Agent.Kind = "orchestrator", "orchestrator"
		after.Agent.Instructions = "x"
		if _, err := w.propose(t, Proposed{AgentID: a.ID, Kind: KindInstructions, Before: Snapshot{Agent: &before}, After: Snapshot{Agent: &after}}); err == nil {
			t.Error("a built-in agent's proposal was stored")
		}
	})
}

// A tool note reaches the agent's notes, survives a person's PUT-style
// regrant, and is refused on a server the agent does not hold.
func TestToolNoteAppliesAndIsHeldToTheGrant(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	var server uuid.UUID
	if err := w.raw.QueryRow(t.Context(),
		`INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'jira', 'https://jira.example') RETURNING id`, w.team).Scan(&server); err != nil {
		t.Fatal(err)
	}
	if _, err := w.raw.Exec(t.Context(), `INSERT INTO mcp_tools (mcp_server_id, team_id, name) VALUES ($1, $2, 'get_issue')`, server, w.team); err != nil {
		t.Fatal(err)
	}

	before, after := w.now(t, a), w.now(t, a)
	after.Notes = []ToolNote{{Server: server, Tool: "get_issue", Note: "Project keys are upper case."}}
	if _, err := w.propose(t, Proposed{AgentID: a.ID, Kind: KindToolNote, Before: Snapshot{Agent: &before}, After: Snapshot{Agent: &after}}); err == nil {
		t.Fatal("a note on a server the agent does not hold was stored")
	}

	if _, err := w.raw.Exec(t.Context(), `INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id) VALUES ($1, $2, $3)`, a.ID, server, w.team); err != nil {
		t.Fatal(err)
	}
	before, after = w.now(t, a), w.now(t, a)
	after.Notes = []ToolNote{{Server: server, Tool: "get_issue", Note: "Project keys are upper case."}}
	p := w.mustPropose(t, Proposed{AgentID: a.ID, Kind: KindToolNote, Before: Snapshot{Agent: &before}, After: Snapshot{Agent: &after}})
	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}

	// A person's replace of the agent rewrites the grants and must leave the
	// note alone.
	if err := w.store.InTx(t.Context(), func(q *db.Queries) error {
		row, err := q.GetAgent(t.Context(), a.ID)
		if err != nil {
			return err
		}
		req, err := agents.RequestOf(row)
		if err != nil {
			return err
		}
		req.MCPServers = []agents.ServerGrant{{ID: server}}
		return agents.Regrant(t.Context(), q, row, req)
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.now(t, a).Notes; len(got) != 1 || got[0].Note != "Project keys are upper case." {
		t.Errorf("notes after a person's regrant = %+v", got)
	}
}

// A skill edit replaces the skill, a script among its files, and its revert
// writes back the old text, which the proposal kept.
func TestSkillEditAppliesAndReverts(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	dir := skill.Directory{
		Manifest: skill.Manifest{Name: "refunds", Description: "How refunds are made", Body: "Step 1: check the order.", Metadata: map[string]string{}},
		Files:    []skill.File{file("references/policy.md", "Refunds within 30 days.")},
	}
	var made db.Skill
	if err := w.store.InTx(t.Context(), func(q *db.Queries) error {
		keys, err := skills.Reserve(t.Context(), w.store, w.objects, w.team, dir.Files)
		if err != nil {
			return err
		}
		made, err = skills.Create(t.Context(), q, w.team, dir, skills.Origin{Source: "upload"}, keys)
		return err
	}); err != nil {
		t.Fatalf("skill: %v", err)
	}

	before := w.skillNow(t, made)
	policy := "Refunds within 30 days."
	before.Files[0].Text = &policy
	after := before
	after.Files = append([]FileState{}, before.Files...)
	after.Body = "Step 1: check the order. Step 2: confirm the order id."
	script := "print('check')\n"
	after.Files = append(after.Files, FileState{Path: "scripts/check.py", SHA256: hashText(script), Text: &script})

	p := w.mustPropose(t, Proposed{
		AgentID: a.ID, SkillID: &made.ID, Kind: KindSkillEdit,
		Before: Snapshot{Skill: &before}, After: Snapshot{Skill: &after},
	})
	if !slicesContain(p.Marks, MarkChangesCode) {
		t.Errorf("marks = %v, want changes_code for a new script", p.Marks)
	}

	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	got := w.skillNow(t, made)
	if got.Body != after.Body || len(got.Files) != 2 {
		t.Fatalf("after approve: body %q, %d files", got.Body, len(got.Files))
	}

	if rec := w.decide(t, p.ID, "revert"); rec.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", rec.Code, rec.Body)
	}
	got = w.skillNow(t, made)
	if got.Body != "Step 1: check the order." || len(got.Files) != 1 || got.Files[0].Path != "references/policy.md" {
		t.Errorf("after revert: body %q, files %+v", got.Body, got.Files)
	}
}

// An imported skill is never a target.
func TestImportedSkillIsNeverEdited(t *testing.T) {
	before := SkillState{Name: "x", Description: "d", Body: "b", Source: "github"}
	after := before
	after.Body = "changed"
	if err := Check(KindSkillEdit, Snapshot{Skill: &before}, Snapshot{Skill: &after}); err == nil ||
		!strings.Contains(err.Error(), "imported") {
		t.Errorf("Check = %v, want the import refused", err)
	}
}

// skill_new creates and grants a skill; its revert ungrants and deletes it,
// and is refused once another agent holds it.
func TestSkillNewAppliesAndReverts(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	other := w.agent(t, "billing-bot")
	before, after := w.now(t, a), w.now(t, a)
	guide := "Ask for the order id first."
	made := SkillState{
		Name: "order-ids", Description: "Confirming order ids", Body: "Always confirm.", Source: "upload",
		Files: []FileState{{Path: "references/guide.md", SHA256: hashText(guide), Text: &guide}},
	}
	p := w.mustPropose(t, Proposed{AgentID: a.ID, Kind: KindSkillNew, Before: Snapshot{Agent: &before}, After: Snapshot{Agent: &after, Skill: &made}})

	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if got := w.now(t, a).Agent.Skills; len(got) != 1 {
		t.Fatalf("skills after approve = %v, want the new one", got)
	}
	skillID := w.now(t, a).Agent.Skills[0]

	if _, err := w.raw.Exec(t.Context(), `INSERT INTO agent_skills (agent_id, skill_id, team_id) VALUES ($1, $2, $3)`, other.ID, skillID, w.team); err != nil {
		t.Fatal(err)
	}
	if rec := w.decide(t, p.ID, "revert"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "other agents hold") {
		t.Fatalf("revert while held elsewhere = %d %s, want 409", rec.Code, rec.Body)
	}
	if got := w.now(t, a).Agent.Skills; len(got) != 1 {
		t.Errorf("a refused revert changed the agent: %v", got)
	}

	if _, err := w.raw.Exec(t.Context(), `DELETE FROM agent_skills WHERE agent_id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if rec := w.decide(t, p.ID, "revert"); rec.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", rec.Code, rec.Body)
	}
	var n int
	if err := w.raw.QueryRow(t.Context(), `SELECT count(*) FROM skills WHERE id = $1`, skillID).Scan(&n); err != nil || n != 0 {
		t.Errorf("the created skill is still there (%d, %v)", n, err)
	}
}

func (w *world) skillNow(t *testing.T, s db.Skill) SkillState {
	t.Helper()
	var st SkillState
	if err := w.store.InTx(t.Context(), func(q *db.Queries) error {
		row, err := q.GetSkill(t.Context(), s.ID)
		if err != nil {
			return err
		}
		st, err = SkillNow(t.Context(), q, row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

func file(path, text string) skill.File {
	f := skill.File{Path: path, Data: []byte(text), MediaType: "text/markdown"}
	copy(f.SHA256[:], mustHex(hashText(text)))
	return f
}

// A skill a person edited since the proposal was written is not
// overwritten, and a file whose text does not match its hash is refused
// before it is stored.
func TestSkillEditOfAChangedSkillIsRefused(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	var made db.Skill
	dir := skill.Directory{Manifest: skill.Manifest{Name: "refunds", Description: "How refunds are made", Body: "Step 1.", Metadata: map[string]string{}}}
	if err := w.store.InTx(t.Context(), func(q *db.Queries) error {
		var err error
		made, err = skills.Create(t.Context(), q, w.team, dir, skills.Origin{Source: "upload"}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	before := w.skillNow(t, made)
	after := before
	after.Body = "Step 1. Step 2."
	wrong := "not what the hash says"
	bad := after
	bad.Files = []FileState{{Path: "references/x.md", SHA256: hashText("something else"), Text: &wrong}}
	if _, err := w.propose(t, Proposed{
		AgentID: a.ID, SkillID: &made.ID, Kind: KindSkillEdit,
		Before: Snapshot{Skill: &before}, After: Snapshot{Skill: &bad},
	}); err == nil {
		t.Error("a file whose text does not match its hash was stored")
	}

	p := w.mustPropose(t, Proposed{
		AgentID: a.ID, SkillID: &made.ID, Kind: KindSkillEdit,
		Before: Snapshot{Skill: &before}, After: Snapshot{Skill: &after},
	})
	if _, err := w.raw.Exec(t.Context(), `UPDATE skills SET body = 'by hand' WHERE id = $1`, made.ID); err != nil {
		t.Fatal(err)
	}
	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusConflict {
		t.Fatalf("approve = %d %s, want 409", rec.Code, rec.Body)
	}
	if got := w.skillNow(t, made).Body; got != "by hand" {
		t.Errorf("body = %q, want the person's edit kept", got)
	}
}

// run puts a run in a new session of the given kind, and says which.
func (w *world) run(t *testing.T, kind string) (run, session uuid.UUID) {
	t.Helper()
	var channel uuid.UUID
	if err := w.raw.QueryRow(t.Context(),
		`INSERT INTO channels (team_id, provider, name) VALUES ($1, 'telegram', $2) RETURNING id`,
		w.team, uuid.NewString()).Scan(&channel); err != nil {
		t.Fatalf("channel: %v", err)
	}
	if err := w.raw.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, channel_id, provider_ref, kind) VALUES ($1, $2, '42', $3) RETURNING id`,
		w.team, channel, kind).Scan(&session); err != nil {
		t.Fatalf("session: %v", err)
	}
	run = uuid.New()
	if _, err := w.raw.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, hub, reply_to_channel)
		VALUES ($1, $2, $3, $1, 'support-bot', 'where is my order', false, true)`, run, w.team, session); err != nil {
		t.Fatalf("run: %v", err)
	}
	return run, session
}

// A cited run says where it can be read only to someone who may read it: a
// direct conversation with someone else stays closed without session:read_all.
func TestRunsSayWhereTheyAreReadOnlyToThoseWhoMay(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	group, groupSession := w.run(t, "group")
	direct, directSession := w.run(t, "direct")
	p := w.instructionsChange(t, a)
	p.RunIDs = []uuid.UUID{group, direct}
	row := w.mustPropose(t, p)

	runs := func() map[uuid.UUID]runRef {
		t.Helper()
		rec := w.serve(t, "GET", "/improvements/"+row.ID.String(), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get: %d %s", rec.Code, rec.Body)
		}
		var v view
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]runRef{}
		for _, r := range v.Runs {
			out[r.ID] = r
		}
		return out
	}

	got := runs()
	if r := got[group]; !r.Readable || r.SessionID == nil || *r.SessionID != groupSession {
		t.Errorf("group run = %+v, want readable in %s", r, groupSession)
	}
	if r := got[direct]; r.Readable || r.SessionID != nil {
		t.Errorf("someone else's direct run = %+v, want closed and nowhere", r)
	}

	w.holds[readEveryConvo] = true
	if r := runs()[direct]; !r.Readable || r.SessionID == nil || *r.SessionID != directSession {
		t.Errorf("direct run for a moderator = %+v, want readable in %s", r, directSession)
	}
}

// Two approvals at once on different proposals for one agent: the target is
// held first, so one applies and the other finds itself withdrawn. Neither
// deadlocks into a server error.
func TestTwoApprovalsOnOneAgentDoNotDeadlock(t *testing.T) {
	w := newWorld(t)
	for i := range 20 {
		a := w.agent(t, fmt.Sprintf("bot-%d", i))
		first := w.mustPropose(t, w.instructionsChange(t, a))
		second := w.instructionsChange(t, a)
		second.After.Agent.Agent.Instructions = "Ask for the order id first."
		other := w.mustPropose(t, second)

		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for _, id := range []uuid.UUID{first.ID, other.ID} {
			wg.Go(func() { codes <- w.decide(t, id, "approve").Code })
		}
		wg.Wait()
		close(codes)
		got := map[int]int{}
		for c := range codes {
			got[c]++
		}
		if got[http.StatusOK] != 1 || got[http.StatusConflict] != 1 {
			t.Fatalf("round %d: codes = %v, want one 200 and one 409", i, got)
		}
	}
}

// Rejecting writes nothing to the agent, so the approve permission alone
// decides it; approving still needs the write.
func TestRejectNeedsOnlyTheApprovePermission(t *testing.T) {
	w := newWorld(t)
	a := w.agent(t, "support-bot")
	p := w.mustPropose(t, w.instructionsChange(t, a))
	w.holds = map[authz.Action]bool{approveAction: true}

	if rec := w.decide(t, p.ID, "approve"); rec.Code != http.StatusForbidden {
		t.Errorf("approve without agent:write = %d, want 403", rec.Code)
	}
	if rec := w.decide(t, p.ID, "reject"); rec.Code != http.StatusOK {
		t.Errorf("reject = %d %s, want 200", rec.Code, rec.Body)
	}
	if s := w.status(t, p.ID); s != "rejected" {
		t.Errorf("status = %s, want rejected", s)
	}
}
