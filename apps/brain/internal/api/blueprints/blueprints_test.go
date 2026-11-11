package blueprints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

// TestMain drops the migrated template the database tests copied.
func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

var loops = []string{"plan", "react"}

type world struct {
	store   *store.Store
	raw     *pgx.Conn
	team    uuid.UUID
	authz   *fakeAuthz
	objects stored
	// failOn makes that write fail inside the transaction, as a database
	// that drops the connection halfway would.
	failOn string
	// user is who calls; a fresh id when unset.
	user uuid.UUID
	// refresh answers for the servers an apply wrote.
	refresh fakeRefresher
	// probe answers the builder's list_mcp_tools.
	probe fakeDiscoverer
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

	w := &world{store: s, raw: raw, authz: everything()}
	if err := raw.QueryRow(t.Context(), `INSERT INTO teams (name) VALUES ('support') RETURNING id`).Scan(&w.team); err != nil {
		t.Fatalf("team: %v", err)
	}
	w.user = w.newUser(t, "someone")
	return w
}

func (w *world) InTx(ctx context.Context, fn func(Queries) error) error {
	return w.store.InTx(ctx, func(q *db.Queries) error { return fn(failing{q, w.failOn}) })
}

func (w *world) ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error {
	return w.store.ReserveObjects(ctx, arg)
}

func (w *world) ListTeamAgents(ctx context.Context, id uuid.UUID) ([]db.Agent, error) {
	return w.store.ListTeamAgents(ctx, id)
}

func (w *world) ListTeamAgentServers(ctx context.Context, id uuid.UUID) ([]db.AgentMcpServer, error) {
	return w.store.ListTeamAgentServers(ctx, id)
}

func (w *world) ListTeamAgentSkills(ctx context.Context, id uuid.UUID) ([]db.AgentSkill, error) {
	return w.store.ListTeamAgentSkills(ctx, id)
}

func (w *world) ListTeamAgentDelegates(ctx context.Context, id uuid.UUID) ([]db.AgentDelegate, error) {
	return w.store.ListTeamAgentDelegates(ctx, id)
}

func (w *world) ListTeamMCPServers(ctx context.Context, id uuid.UUID) ([]db.McpServer, error) {
	return w.store.ListTeamMCPServers(ctx, id)
}

func (w *world) ListTeamSkills(ctx context.Context, id uuid.UUID) ([]db.Skill, error) {
	return w.store.ListTeamSkills(ctx, id)
}

func (w *world) caller() uuid.UUID {
	if w.user != uuid.Nil {
		return w.user
	}
	return uuid.New()
}

func (w *world) CountRunProposals(ctx context.Context, id uuid.UUID) (int64, error) {
	return w.store.CountRunProposals(ctx, id)
}

func (w *world) GetProposalVisible(ctx context.Context, arg db.GetProposalVisibleParams) (db.Proposal, error) {
	return w.store.GetProposalVisible(ctx, arg)
}

func (w *world) ListProposalsVisible(ctx context.Context, arg db.ListProposalsVisibleParams) ([]db.Proposal, error) {
	return w.store.ListProposalsVisible(ctx, arg)
}

func (w *world) ListTeamToolNames(ctx context.Context, id uuid.UUID) ([]db.ListTeamToolNamesRow, error) {
	return w.store.ListTeamToolNames(ctx, id)
}

func (w *world) ListTeamSkillFiles(ctx context.Context, id uuid.UUID) ([]db.ListTeamSkillFilesRow, error) {
	return w.store.ListTeamSkillFiles(ctx, id)
}

var errDropped = errors.New("connection reset by peer")

type failing struct {
	*db.Queries
	on string
}

func (f failing) AddAgentDelegates(ctx context.Context, arg db.AddAgentDelegatesParams) error {
	if f.on == "delegates" {
		return errDropped
	}
	return f.Queries.AddAgentDelegates(ctx, arg)
}

// stored is an object store that keeps what it is given.
type stored struct {
	mu   sync.Mutex
	keys []string
}

func (s *stored) Put(_ context.Context, _ uuid.UUID, key string, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	return nil
}

type fakeAuthz struct{ holds map[authz.Action]bool }

func everything() *fakeAuthz {
	return &fakeAuthz{holds: map[authz.Action]bool{"agent:write": true, "skill:write": true, "tool:write": true}}
}

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	return f.holds[a], nil
}

func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	return false, nil
}

type liveTeams struct{}

func (liveTeams) TeamDeleted(context.Context, uuid.UUID) (bool, error) { return false, nil }

func (w *world) call(t *testing.T, verb string, body any) *httptest.ResponseRecorder {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/teams/"+w.team.String()+"/blueprints/"+verb, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return w.serve(t, req)
}

// serve sends a request as a member of the team, through the guard.
func (w *world) serve(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rs := authz.NewRoutes()
	for _, r := range []struct{ method, path string }{
		{"POST", "/teams/{id}/blueprints/plan"},
		{"POST", "/teams/{id}/blueprints/apply"},
		{"GET", "/teams/{id}/blueprint"},
		{"GET", "/teams/{id}/proposals"},
		{"GET", "/teams/{id}/proposals/{proposalID}"},
		{"POST", "/teams/{id}/proposals/{proposalID}/apply"},
	} {
		if err := rs.Add(r.method, r.path, "member", nil); err != nil {
			t.Fatalf("route: %v", err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	New(logger, w, w.authz, &w.objects, &w.refresh, loops).Register(mux, api.NewGuard(rs, w.authz, liveTeams{}, logger))

	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{
		ID: w.caller(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: w.team, Name: "support", Role: "member"}},
	}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (w *world) plan(t *testing.T, bp Blueprint) Plan {
	t.Helper()
	rec := w.call(t, "plan", bp)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	var p Plan
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("plan: %v", err)
	}
	return p
}

// apply plans, then applies the plan it was shown, as oa apply does.
func (w *world) apply(t *testing.T, bp Blueprint) *httptest.ResponseRecorder {
	t.Helper()
	return w.call(t, "apply", applyRequest{Blueprint: bp, PlanDigest: w.plan(t, bp).Digest})
}

func (w *world) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := w.raw.QueryRow(t.Context(), query, w.team).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// written is every row a blueprint can write, and every trace of a write:
// the graph's outbox and the reserved objects.
func (w *world) written(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{
		"agents", "mcp_servers", "skills", "skill_files", "agent_mcp_servers",
		"agent_skills", "agent_delegates", "graph_outbox", "deleted_objects",
	} {
		out[table] = w.count(t, "SELECT count(*) FROM "+table+" WHERE team_id = $1")
	}
	return out
}

func manifest(name string) []byte {
	return []byte("---\nname: " + name + "\ndescription: Answers from the support FAQ.\n---\nRead faq.md first.\n")
}

func faqSkill(faq string) skillSpec {
	return skillSpec{Files: []skillFile{
		{Path: "SKILL.md", Data: manifest("support-faq")},
		{Path: "faq.md", Data: []byte(faq)},
	}}
}

func agentNamed(name string) agentSpec {
	a := agentSpec{}
	a.Name, a.Instructions, a.Pattern, a.MaxSteps = name, "Sort the email.", "react", 10
	a.Model.Name, a.Model.MaxTokens = "claude-sonnet-5-5", 4096
	return a
}

func supportTeam() Blueprint {
	url := "https://helpdesk.example/mcp"
	ref := "helpdesk#token"
	triage := agentNamed("triage")
	triage.Skills = []string{"support-faq"}
	triage.MCPServers = []serverRef{{Name: "helpdesk", Allow: []string{"create_ticket"}}}
	listed := "listed"
	triage.Delegation = &listed
	triage.Delegates = []string{"writer"}

	return Blueprint{
		MCPServers: []serverSpec{{Name: "helpdesk", URL: &url, AuthSecret: &ref}},
		Skills:     []skillSpec{faqSkill("Q: refunds? A: within 30 days.")},
		Agents:     []agentSpec{triage, agentNamed("writer")},
	}
}

func actions(p []Change) map[string]Action {
	out := map[string]Action{}
	for _, c := range p {
		out[string(c.Kind)+"/"+c.Name] = c.Action
	}
	return out
}

func TestApplyingABlueprintMakesEverythingItNames(t *testing.T) {
	t.Parallel()
	w := newWorld(t)

	rec := w.apply(t, supportTeam())
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	var out applied
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("applied: %v", err)
	}
	for _, c := range out.Changes {
		if c.Action != ActionCreate || c.ID == nil {
			t.Errorf("%s %s: %s, id %v; want created with an id", c.Kind, c.Name, c.Action, c.ID)
		}
	}

	got := w.written(t)
	for table, want := range map[string]int{
		"agents": 2, "mcp_servers": 1, "skills": 1, "skill_files": 1,
		"agent_mcp_servers": 1, "agent_skills": 1, "agent_delegates": 1,
	} {
		if got[table] != want {
			t.Errorf("%s: %d rows, want %d", table, got[table], want)
		}
	}

	var ref string
	if err := w.raw.QueryRow(t.Context(), `SELECT auth_secret_ref FROM mcp_servers WHERE team_id = $1`, w.team).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if want := "teams/" + w.team.String() + "/mcp/helpdesk#token"; ref != want {
		t.Errorf("auth_secret_ref = %q, want the team's own %q", ref, want)
	}
}

// The second apply is the promise a blueprint makes: nothing written, not
// even a row the graph would rebuild.
func TestApplyingTheSameBlueprintTwiceChangesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)

	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("first apply: %d %s", rec.Code, rec.Body)
	}
	before := w.written(t)

	p := w.plan(t, supportTeam())
	for name, a := range actions(p.Changes) {
		if a != ActionUnchanged {
			t.Errorf("%s: %s the second time, want unchanged", name, a)
		}
	}
	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("second apply: %d %s", rec.Code, rec.Body)
	}
	if after := w.written(t); !equalCounts(before, after) {
		t.Errorf("the second apply wrote: before %v, after %v", before, after)
	}
}

func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestAChangeIsPlannedAsAnUpdateOfWhatChanged(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}

	bp := supportTeam()
	bp.Skills[0] = faqSkill("Q: refunds? A: within 60 days.")
	bp.Agents[0].Instructions = "Sort the email, then answer it."
	bp.Agents[0].Skills = nil

	got := map[string]Change{}
	for _, c := range w.plan(t, bp).Changes {
		got[string(c.Kind)+"/"+c.Name] = c
	}
	if c := got["skill/support-faq"]; c.Action != ActionUpdate || len(c.Fields) != 1 || c.Fields[0] != "files" {
		t.Errorf("skill: %+v, want an update of files", c)
	}
	if c := got["agent/triage"]; c.Action != ActionUpdate || !equalNames(c.Fields, "instructions", "skills") {
		t.Errorf("triage: %+v, want an update of instructions and skills", c)
	}
	if c := got["agent/writer"]; c.Action != ActionUnchanged {
		t.Errorf("writer: %+v, want unchanged", c)
	}

	if rec := w.apply(t, bp); rec.Code != http.StatusOK {
		t.Fatalf("apply the change: %d %s", rec.Code, rec.Body)
	}
	if n := w.count(t, "SELECT count(*) FROM agent_skills WHERE team_id = $1"); n != 0 {
		t.Errorf("triage still holds %d skills; a blueprint describes an agent whole", n)
	}
}

func equalNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// SKILL.md is kept on the skill's row, not as a file, so a change to it alone
// is still a change.
func TestAChangeToSkillMDAloneIsAChange(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}

	bp := supportTeam()
	bp.Skills[0].Files[0].Data = []byte("---\nname: support-faq\ndescription: Refunds and returns.\n---\nRead faq.md first.\n")
	p := w.plan(t, bp)
	if got := actions(p.Changes)["skill/support-faq"]; got != ActionUpdate {
		t.Errorf("skill: %s, want update", got)
	}
}

func TestAnItemTheBlueprintDoesNotNameIsLeftAlone(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	before := w.written(t)

	only := Blueprint{Agents: []agentSpec{agentNamed("writer")}}
	if rec := w.apply(t, only); rec.Code != http.StatusOK {
		t.Fatalf("apply a smaller blueprint: %d %s", rec.Code, rec.Body)
	}
	if after := w.written(t); !equalCounts(before, after) {
		t.Errorf("a blueprint naming only writer changed the team: before %v, after %v", before, after)
	}
}

// A grant may name what the team already holds without the blueprint
// describing it too, both for an agent it creates and for one it changes.
func TestAnAgentMayBeGrantedWhatTheTeamAlreadyHolds(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}

	listed := "listed"
	clerk := agentNamed("clerk")
	clerk.MCPServers = []serverRef{{Name: "helpdesk"}}
	clerk.Skills = []string{"support-faq"}
	clerk.Delegation, clerk.Delegates = &listed, []string{"writer"}
	writer := agentNamed("writer")
	writer.Skills = []string{"support-faq"}

	if rec := w.apply(t, Blueprint{Agents: []agentSpec{clerk, writer}}); rec.Code != http.StatusOK {
		t.Fatalf("apply grants of what the team holds: %d %s", rec.Code, rec.Body)
	}
	got := w.written(t)
	for table, want := range map[string]int{"agent_mcp_servers": 2, "agent_skills": 3, "agent_delegates": 2} {
		if got[table] != want {
			t.Errorf("%s: %d rows, want %d", table, got[table], want)
		}
	}
}

// Each refusal refuses the whole plan: nothing is written, and no object is
// reserved for a skill that will never be stored.
func TestARefusedBlueprintWritesNothing(t *testing.T) {
	t.Parallel()

	unknownSkill := supportTeam()
	unknownSkill.Agents[0].Skills = []string{"faq"}
	badModel := supportTeam()
	badModel.Agents[1].Model.MaxTokens = 0
	self := supportTeam()
	self.Agents[0].Delegates = []string{"triage"}
	secret := supportTeam()
	plain := "sk-live-123"
	secret.MCPServers[0].AuthSecret = &plain
	hub := supportTeam()
	hub.Agents[1].Name = "orchestrator"

	cases := map[string]struct {
		bp    Blueprint
		authz *fakeAuthz
	}{
		"an unknown skill":                 {unknownSkill, everything()},
		"a model with no max_tokens":       {badModel, everything()},
		"an agent delegating to itself":    {self, everything()},
		"a secret instead of a reference":  {secret, everything()},
		"the hub's name":                   {hub, everything()},
		"no tool:write for its MCP server": {supportTeam(), &fakeAuthz{holds: map[authz.Action]bool{"agent:write": true, "skill:write": true}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.authz = tc.authz
			before := w.written(t)

			p := w.plan(t, tc.bp)
			if len(p.Refusals) == 0 {
				t.Fatalf("plan has no refusal: %+v", p)
			}
			rec := w.call(t, "apply", applyRequest{Blueprint: tc.bp, PlanDigest: p.Digest})
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("apply: %d %s, want 422", rec.Code, rec.Body)
			}
			if after := w.written(t); !equalCounts(before, after) {
				t.Errorf("a refused apply wrote: before %v, after %v", before, after)
			}
		})
	}
}

func TestAnAgentTheBrainProvidesIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.raw.Exec(t.Context(), `
		INSERT INTO agents (team_id, name, kind, instructions, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'recall', 'memory', '', 'm', 1, 'react', 1)`, w.team); err != nil {
		t.Fatalf("insert: %v", err)
	}

	p := w.plan(t, Blueprint{Agents: []agentSpec{agentNamed("recall")}})
	if len(p.Refusals) != 1 || p.Refusals[0].Name != "recall" {
		t.Errorf("refusals = %+v, want one for recall", p.Refusals)
	}
}

// A digest says which plan the person agreed to. If the team moved since,
// they are shown the new plan and nothing is written.
func TestAPlanThatIsNoLongerTheOneShownIsNotApplied(t *testing.T) {
	t.Parallel()
	w := newWorld(t)

	shown := w.plan(t, supportTeam())
	if rec := w.apply(t, Blueprint{Agents: []agentSpec{agentNamed("writer")}}); rec.Code != http.StatusOK {
		t.Fatalf("someone else's apply: %d %s", rec.Code, rec.Body)
	}
	before := w.written(t)

	rec := w.call(t, "apply", applyRequest{Blueprint: supportTeam(), PlanDigest: shown.Digest})
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply: %d %s, want 409", rec.Code, rec.Body)
	}
	var now Plan
	if err := json.Unmarshal(rec.Body.Bytes(), &now); err != nil || now.Digest == shown.Digest {
		t.Errorf("409 body %s, want the new plan", rec.Body)
	}
	if after := w.written(t); !equalCounts(before, after) {
		t.Errorf("a stale apply wrote: before %v, after %v", before, after)
	}
}

func TestAnApplyWithNoDigestIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)

	if rec := w.call(t, "apply", applyRequest{Blueprint: supportTeam()}); rec.Code != http.StatusBadRequest {
		t.Errorf("apply: %d, want 400", rec.Code)
	}
	if n := w.count(t, "SELECT count(*) FROM agents WHERE team_id = $1"); n != 0 {
		t.Errorf("%d agents written", n)
	}
}

// A write that fails halfway leaves no row: the servers, skills and agents
// written before it are rolled back with it.
func TestAnApplyThatFailsHalfwayLeavesNoRow(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.failOn = "delegates"

	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusInternalServerError {
		t.Fatalf("apply: %d %s, want 500", rec.Code, rec.Body)
	}
	for table, n := range w.written(t) {
		if n != 0 && table != "deleted_objects" {
			t.Errorf("%s holds %d rows after a failed apply", table, n)
		}
	}
	if n := w.count(t, "SELECT count(*) FROM deleted_objects WHERE team_id = $1"); n != 1 {
		t.Errorf("%d objects reserved, want faq.md's left for the reaper", n)
	}
}

func TestABlueprintOverTheLimitIsRefusedBeforeItIsRead(t *testing.T) {
	t.Parallel()
	w := newWorld(t)

	bp := Blueprint{Agents: make([]agentSpec, maxItems+1)}
	if rec := w.call(t, "plan", bp); rec.Code != http.StatusBadRequest {
		t.Errorf("plan: %d, want 400", rec.Code)
	}
}
