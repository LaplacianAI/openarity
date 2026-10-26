package graph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The projector is tested against the three things it sits between: a real
// Postgres with the real triggers writing the outbox, a real FalkorDB, and a
// model router that answers like one and counts what it was asked.
//
// None of these tests is parallel. Each has a schema of its own, but they
// share one server and migrate it, and migrations take a lock.

const (
	testModel      = "test-model"
	cleanupTimeout = 30 * time.Second
)

// liveSchema is a migrated schema of its own for one test, and a plain
// connection into it for the few statements no query is generated for.
func liveSchema(t *testing.T) (*store.Store, *pgx.Conn) {
	t.Helper()

	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}

	// Postgres cuts an identifier at 63 bytes without failing, and two tests
	// cut to the same name would share their tables.
	schema := "graph_test_" + strings.ToLower(t.Name())
	if len(schema) > 63 {
		t.Fatalf("the schema name %s is %d bytes, and Postgres keeps 63", schema, len(schema))
	}

	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, statement := range []string{"DROP SCHEMA IF EXISTS " + schema + " CASCADE", "CREATE SCHEMA " + schema} {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	_ = admin.Close(t.Context())

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("BRAIN_TEST_POSTGRES_DSN is not a URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := store.New(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect to the schema: %v", err)
	}
	raw, err := pgx.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect to the schema: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
		defer cancel()

		s.Close()
		if _, err := raw.Exec(ctx, "SET lock_timeout = '5s'"); err != nil {
			t.Errorf("set lock_timeout: %v", err)
		}
		if _, err := raw.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		_ = raw.Close(ctx)
	})

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s, raw
}

// vectorOf is the vector the fake router gives a text under a model: three
// numbers taken from a hash of both, so the wrong text's vector on a node, or
// the right text's under the wrong model, is a visible difference.
func vectorOf(model, text string) []float32 {
	sum := sha256.Sum256([]byte(model + "\x00" + text))
	return []float32{float32(sum[0]) + 1, float32(sum[1]) + 1, float32(sum[2]) + 1}
}

// faulty is the store with a fault put in front of one call. Zero, it is the
// store.
type faulty struct {
	*store.Store

	claim, forget, getTeam, backlog, entities error
	backwards                                 bool
	onGetSkill                                func()
}

func (f *faulty) ClaimGraphOutbox(ctx context.Context, arg db.ClaimGraphOutboxParams) ([]db.GraphOutbox, error) {
	if f.claim != nil {
		return nil, f.claim
	}
	return f.Store.ClaimGraphOutbox(ctx, arg)
}

func (f *faulty) ForgetGraphOutbox(ctx context.Context, ids []int64) error {
	if f.forget != nil {
		return f.forget
	}
	return f.Store.ForgetGraphOutbox(ctx, ids)
}

func (f *faulty) GraphOutboxBacklog(ctx context.Context) ([]db.GraphOutboxBacklogRow, error) {
	if f.backlog != nil {
		return nil, f.backlog
	}
	return f.Store.GraphOutboxBacklog(ctx)
}

func (f *faulty) ListGraphEntities(ctx context.Context, team uuid.UUID) ([]db.ListGraphEntitiesRow, error) {
	if f.entities != nil {
		return nil, f.entities
	}
	rows, err := f.Store.ListGraphEntities(ctx, team)
	if f.backwards {
		slices.Reverse(rows)
	}
	return rows, err
}

func (f *faulty) GetTeam(ctx context.Context, id uuid.UUID) (db.Team, error) {
	if f.getTeam != nil {
		return db.Team{}, f.getTeam
	}
	return f.Store.GetTeam(ctx, id)
}

func (f *faulty) GetSkill(ctx context.Context, id uuid.UUID) (db.Skill, error) {
	row, err := f.Store.GetSkill(ctx, id)
	if f.onGetSkill != nil {
		hook := f.onGetSkill
		f.onGetSkill = nil
		hook()
	}
	return row, err
}

type world struct {
	t *testing.T

	store *faulty
	sql   *pgx.Conn
	graph *Graph
	p     *Projector

	router *router
	down   atomic.Bool
	logs   bytes.Buffer

	names map[uuid.UUID]string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	s, raw := liveSchema(t)
	g, err := Dial(t.Context(), liveFalkorURL(t), 3)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	w := &world{t: t, store: &faulty{Store: s}, sql: raw, graph: g, names: map[uuid.UUID]string{}}
	w.router = newRouter(t, func(rw http.ResponseWriter, req embedRequest) {
		if w.down.Load() {
			rw.WriteHeader(http.StatusServiceUnavailable)
			_, _ = rw.Write([]byte(`{"error":{"code":"no_upstream","message":"no healthy upstream"}}`))
			return
		}
		data := make([]embedding, len(req.Input))
		for i, text := range req.Input {
			data[i] = embedding{Index: i, Embedding: vectorOf(req.Model, text)}
		}
		_ = json.NewEncoder(rw).Encode(embedResponse{Data: data})
	})
	w.p = w.projector(testModel, g)
	return w
}

// projector is one more projector over the same database and router, as a
// second worker is, or the same worker after its model was changed.
func (w *world) projector(model string, g *Graph) *Projector {
	logger := slog.New(slog.NewJSONHandler(&w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := NewProjector(w.store, g, NewEmbedder(w.router.URL, model, g.dims, nil, client()), logger)
	// Advisory locks are per database, not per schema, and every package's
	// tests run at once against the same one.
	p.lockID = rand.Int64N(1 << 62)
	return p
}

func (w *world) drain() Result {
	w.t.Helper()

	res, err := w.p.Drain(w.t.Context(), 100)
	if err != nil {
		w.t.Fatalf("Drain: %v", err)
	}
	return res
}

// drained is a drain that must have left nothing behind.
func (w *world) drained() {
	w.t.Helper()

	if res := w.drain(); res.Failed != 0 || res.Busy {
		w.t.Fatalf("drain = %+v, want nothing failed; logs:\n%s", res, w.logs.String())
	}
	if n := w.outstanding(); n != 0 {
		w.t.Fatalf("%d outbox rows are left after a clean drain", n)
	}
}

func (w *world) exec(statement string, args ...any) {
	w.t.Helper()
	if _, err := w.sql.Exec(w.t.Context(), statement, args...); err != nil {
		w.t.Fatalf("%s: %v", statement, err)
	}
}

func (w *world) outstanding() int {
	w.t.Helper()

	var n int
	if err := w.sql.QueryRow(w.t.Context(), "SELECT count(*) FROM graph_outbox").Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// retryNow makes every waiting row due, as a minute passing would.
func (w *world) retryNow() {
	w.t.Helper()
	w.exec("UPDATE graph_outbox SET last_attempt_at = now() - interval '1 hour' WHERE last_attempt_at IS NOT NULL")
}

func (w *world) enqueue(team uuid.UUID, entity string, id uuid.UUID) {
	w.t.Helper()
	w.exec("INSERT INTO graph_outbox (team_id, entity, entity_id) VALUES ($1, $2, $3)", team, entity, id)
}

// embedded is every text the router has been asked to embed, in order.
func (w *world) embedded() []string {
	var texts []string
	for _, req := range w.router.seen() {
		texts = append(texts, req.body.Input...)
	}
	return texts
}

func (w *world) snapshot(team uuid.UUID) Snapshot {
	w.t.Helper()
	return mustSnapshot(w.t, w.graph, team)
}

// edges renders a snapshot's edges by the names the test gave things, sorted.
func (w *world) edges(s Snapshot) []string {
	w.t.Helper()

	var out []string
	for _, e := range s.Edges {
		from, to := w.names[e.From.ID], w.names[e.To.ID]
		if from == "" || to == "" {
			w.t.Fatalf("an edge joins a node this test never made: %+v", e)
		}
		line := fmt.Sprintf("%s:%s %s %s:%s", e.From.Label, from, e.Type, e.To.Label, to)
		if only, ok := e.Props["only"]; ok {
			line += fmt.Sprintf(" only%v", only)
		}
		out = append(out, line)
	}
	slices.Sort(out)
	return out
}

func (w *world) nodes(s Snapshot) []string {
	w.t.Helper()

	var out []string
	for _, n := range s.Nodes {
		name := w.names[n.ID]
		if name == "" {
			w.t.Fatalf("the graph holds a node this test never made: %+v", n)
		}
		out = append(out, string(n.Label)+":"+name)
	}
	slices.Sort(out)
	return out
}

func (w *world) wantGraph(team uuid.UUID, nodes, edges []string) Snapshot {
	w.t.Helper()

	s := w.snapshot(team)
	slices.Sort(nodes)
	slices.Sort(edges)
	if got := w.nodes(s); !slices.Equal(got, nodes) {
		w.t.Errorf("nodes:\n got %v\nwant %v", got, nodes)
	}
	if got := w.edges(s); !slices.Equal(got, edges) {
		w.t.Errorf("edges:\n got %v\nwant %v", got, edges)
	}
	return s
}

// wantVector checks a node carries the vector of exactly this text under this
// model, and the two properties that say so.
func (w *world) wantVector(n Node, model, text string) {
	w.t.Helper()

	sum := sha256.Sum256([]byte(text))
	if n.Model != model || !bytes.Equal(n.ContentSHA256, sum[:]) {
		w.t.Errorf("%s %s is marked %q %x, want %q and the hash of %q", n.Label, w.names[n.ID], n.Model, n.ContentSHA256, model, text)
	}
	if !slices.Equal(n.Embedding, vectorOf(model, text)) {
		w.t.Errorf("%s %s has the vector %v, want the one for %q under %s", n.Label, w.names[n.ID], n.Embedding, text, model)
	}
}

// --- rows ---

func (w *world) team(name string) db.Team {
	w.t.Helper()

	team, err := w.store.CreateTeam(w.t.Context(), name)
	if err != nil {
		w.t.Fatalf("CreateTeam: %v", err)
	}
	w.names[team.ID] = name
	w.t.Cleanup(func() { _ = w.graph.DropTeam(context.WithoutCancel(w.t.Context()), team.ID) })
	return team
}

func (w *world) server(team db.Team, name string) db.McpServer {
	w.t.Helper()

	address := "https://mcp.example.com"
	srv, err := w.store.CreateMCPServer(w.t.Context(), db.CreateMCPServerParams{
		TeamID: team.ID, Name: name, Url: &address, Env: []byte(`{}`),
	})
	if err != nil {
		w.t.Fatalf("CreateMCPServer: %v", err)
	}
	w.names[srv.ID] = name
	return srv
}

func (w *world) tool(srv db.McpServer, name, description string) db.McpTool {
	w.t.Helper()

	err := w.store.UpsertMCPTool(w.t.Context(), db.UpsertMCPToolParams{
		McpServerID: srv.ID, TeamID: srv.TeamID, Name: name, Description: description,
		InputSchema: []byte(`{"type":"object"}`),
	})
	if err != nil {
		w.t.Fatalf("UpsertMCPTool: %v", err)
	}
	tools, err := w.store.ListMCPToolsByServer(w.t.Context(), srv.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, tl := range tools {
		if tl.Name == name {
			w.names[tl.ID] = name
			return tl
		}
	}
	w.t.Fatalf("the tool %s was not stored", name)
	return db.McpTool{}
}

func skillParams(sk db.Skill) db.UpdateSkillParams {
	return db.UpdateSkillParams{
		ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body,
		Metadata: sk.Metadata, Source: sk.Source,
	}
}

func (w *world) skill(team db.Team, name, description string) db.Skill {
	w.t.Helper()

	sk, err := w.store.CreateSkill(w.t.Context(), db.CreateSkillParams{
		TeamID: team.ID, Name: name, Description: description, Body: "# " + name,
		Metadata: []byte(`{}`), Source: "upload",
	})
	if err != nil {
		w.t.Fatalf("CreateSkill: %v", err)
	}
	w.names[sk.ID] = name
	return sk
}

func (w *world) updateSkill(p db.UpdateSkillParams) {
	w.t.Helper()
	if _, err := w.store.UpdateSkill(w.t.Context(), p); err != nil {
		w.t.Fatalf("UpdateSkill: %v", err)
	}
}

func agentUpdate(a db.Agent) db.UpdateAgentParams {
	return db.UpdateAgentParams{
		ID: a.ID, Name: a.Name, Description: a.Description, Instructions: a.Instructions,
		ModelName: a.ModelName, MaxTokens: a.MaxTokens, Pattern: a.Pattern, MaxSteps: a.MaxSteps,
		SteerContinuations: a.SteerContinuations, OutputSchema: a.OutputSchema,
		Delegation: a.Delegation,
	}
}

func (w *world) agent(team db.Team, name, description string) db.Agent {
	w.t.Helper()

	a, err := w.store.CreateAgent(w.t.Context(), db.CreateAgentParams{
		TeamID: team.ID, Name: name, Description: description, Kind: "custom",
		ModelName: "test/model", MaxTokens: 1024, Pattern: "react", MaxSteps: 5,
		Delegation: "team",
	})
	if err != nil {
		w.t.Fatalf("CreateAgent: %v", err)
	}
	w.names[a.ID] = name
	return a
}

func (w *world) grant(a db.Agent, srv db.McpServer, allow ...string) {
	w.t.Helper()

	err := w.store.AddAgentMCPServer(w.t.Context(), db.AddAgentMCPServerParams{
		AgentID: a.ID, McpServerID: srv.ID, TeamID: a.TeamID, Allow: allow,
	})
	if err != nil {
		w.t.Fatalf("AddAgentMCPServer: %v", err)
	}
}

func (w *world) teach(a db.Agent, skills ...db.Skill) {
	w.t.Helper()

	ids := make([]uuid.UUID, len(skills))
	for i, sk := range skills {
		ids[i] = sk.ID
	}
	err := w.store.AddAgentSkills(w.t.Context(), db.AddAgentSkillsParams{AgentID: a.ID, SkillIds: ids, TeamID: a.TeamID})
	if err != nil {
		w.t.Fatalf("AddAgentSkills: %v", err)
	}
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

// acme is a team with one of everything, joined every way the graph joins
// things, and nothing drained yet.
type acme struct {
	team           db.Team
	github         db.McpServer
	search, create db.McpTool
	pdf            db.Skill
	triage         db.Agent
}

func (w *world) acme() acme {
	w.t.Helper()

	var a acme
	a.team = w.team("acme")
	a.github = w.server(a.team, "github")
	a.search = w.tool(a.github, "search", "Search code")
	a.create = w.tool(a.github, "create", "Open an issue")
	a.pdf = w.skill(a.team, "pdf", "Extract text from PDFs")
	a.triage = w.agent(a.team, "triage", "Sorts incoming issues")
	w.grant(a.triage, a.github)
	w.teach(a.triage, a.pdf)
	return a
}

var (
	acmeNodes = []string{"Team:acme", "Toolkit:github", "Tool:search", "Tool:create", "Skill:pdf", "Agent:triage"}
	acmeEdges = []string{
		"Team:acme OWNS Toolkit:github", "Team:acme OWNS Skill:pdf", "Team:acme OWNS Agent:triage",
		"Toolkit:github CONTAINS Tool:search", "Toolkit:github CONTAINS Tool:create",
		"Agent:triage GRANTED Toolkit:github", "Agent:triage HAS_SKILL Skill:pdf",
	}
)

// --- what reaches the graph ---

func TestEachEntityAppearsInTheGraph(t *testing.T) {
	w := newWorld(t)
	a := w.acme()

	before := w.outstanding()
	res := w.drain()
	if res.Projected != before || res.Failed != 0 || res.Busy || before == 0 {
		t.Errorf("drain = %+v with %d rows waiting, want every one projected", res, before)
	}
	if n := w.outstanding(); n != 0 {
		t.Errorf("%d outbox rows are left", n)
	}

	s := w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))

	for id, want := range map[uuid.UUID]map[string]any{
		a.team.ID:   nil,
		a.github.ID: {"name": "github", "kind": "mcp"},
		a.search.ID: {"name": "search", "description": "Search code"},
		a.pdf.ID:    {"name": "pdf", "description": "Extract text from PDFs"},
		a.triage.ID: {"name": "triage", "description": "Sorts incoming issues", "kind": "custom"},
	} {
		got := find(t, s, id).Props
		if len(got) != len(want) {
			t.Errorf("%s has the properties %v, want %v", w.names[id], got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s.%s = %v, want %v", w.names[id], k, got[k], v)
			}
		}
	}

	// What is embedded is the name and the description, and nothing else of
	// the row: not a skill's body, not an agent's instructions.
	w.wantVector(find(t, s, a.search.ID), testModel, "search\nSearch code")
	w.wantVector(find(t, s, a.create.ID), testModel, "create\nOpen an issue")
	w.wantVector(find(t, s, a.pdf.ID), testModel, "pdf\nExtract text from PDFs")
	w.wantVector(find(t, s, a.triage.ID), testModel, "triage\nSorts incoming issues")
	for _, id := range []uuid.UUID{a.team.ID, a.github.ID} {
		if n := find(t, s, id); n.Embedding != nil || n.Model != "" || n.ContentSHA256 != nil {
			t.Errorf("%s carries a vector, and nothing searches for one", w.names[id])
		}
	}

	if got := w.embedded(); len(got) != 4 {
		t.Errorf("the router embedded %q, want the four texts once each", got)
	}
}

// A vector has to be findable, not only stored: the index the projector made
// is the one a search reads.
func TestAProjectedSkillIsFoundByItsVector(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	pdf := w.skill(team, "pdf", "Extract text from PDFs")
	w.skill(team, "docx", "Write Word documents")
	w.skill(team, "xlsx", "Read spreadsheets")
	w.drained()

	res, err := w.graph.run(t.Context(), team.ID, true,
		"CALL db.idx.vector.queryNodes('Skill', 'embedding', 1, vecf32($v)) YIELD node RETURN node.id",
		map[string]any{"v": vectorOf(testModel, "pdf\nExtract text from PDFs")})
	if err != nil {
		t.Fatalf("vector query: %v", err)
	}
	if !res.Next() || res.Record().Values()[0] != pdf.ID.String() {
		t.Error("the nearest skill to pdf's own vector is not pdf")
	}
}

func TestADeletedRowLeavesTheGraph(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()

	w.must(w.store.DeleteMCPToolsNotIn(t.Context(), db.DeleteMCPToolsNotInParams{McpServerID: a.github.ID, Names: []string{"search"}}))
	w.drained()
	w.wantGraph(a.team.ID,
		[]string{"Team:acme", "Toolkit:github", "Tool:search", "Skill:pdf", "Agent:triage"},
		[]string{
			"Team:acme OWNS Toolkit:github", "Team:acme OWNS Skill:pdf", "Team:acme OWNS Agent:triage",
			"Toolkit:github CONTAINS Tool:search", "Agent:triage GRANTED Toolkit:github", "Agent:triage HAS_SKILL Skill:pdf",
		})

	// A skill in use cannot be deleted, so it is taken from the agent first.
	// Both rows are in one batch, and the agent's edge goes with the node.
	w.must(w.store.ClearAgentSkills(t.Context(), a.triage.ID))
	w.must(w.store.DeleteSkill(t.Context(), a.pdf.ID))
	w.drained()
	w.wantGraph(a.team.ID,
		[]string{"Team:acme", "Toolkit:github", "Tool:search", "Agent:triage"},
		[]string{"Team:acme OWNS Toolkit:github", "Team:acme OWNS Agent:triage", "Toolkit:github CONTAINS Tool:search", "Agent:triage GRANTED Toolkit:github"})

	// A server goes, and its tools go with it. Nothing here deleted a tool:
	// the cascade did, and the trigger saw it.
	w.must(w.store.ClearAgentMCPServers(t.Context(), a.triage.ID))
	w.must(w.store.DeleteMCPServer(t.Context(), a.github.ID))
	w.drained()
	w.wantGraph(a.team.ID, []string{"Team:acme", "Agent:triage"}, []string{"Team:acme OWNS Agent:triage"})

	w.must(w.store.DeleteAgent(t.Context(), a.triage.ID))
	w.drained()
	w.wantGraph(a.team.ID, []string{"Team:acme"}, nil)
}

// A drain interrupted after its writes and before it cleared its rows runs
// them all again.
func TestReplayingAnOutboxRowChangesNothing(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()
	before, asked := w.snapshot(a.team.ID), len(w.embedded())

	for range 2 {
		w.enqueue(a.team.ID, "mcp_server", a.github.ID)
		w.enqueue(a.team.ID, "mcp_tool", a.search.ID)
		w.enqueue(a.team.ID, "mcp_tool", a.create.ID)
		w.enqueue(a.team.ID, "skill", a.pdf.ID)
		w.enqueue(a.team.ID, "agent", a.triage.ID)
		w.drained()
	}

	after := w.snapshot(a.team.ID)
	if !slices.Equal(w.nodes(after), w.nodes(before)) || !slices.Equal(w.edges(after), w.edges(before)) {
		t.Errorf("a replay changed the graph:\n was %v %v\n now %v %v", w.nodes(before), w.edges(before), w.nodes(after), w.edges(after))
	}
	for _, n := range before.Nodes {
		if again := find(t, after, n.ID); !slices.Equal(again.Embedding, n.Embedding) || again.Model != n.Model {
			t.Errorf("a replay changed the vector of %s", w.names[n.ID])
		}
	}
	if got := len(w.embedded()); got != asked {
		t.Errorf("a replay cost %d embeddings", got-asked)
	}
}

// --- what an embedding costs ---

func TestAnUnchangedDescriptionIsNotEmbeddedAgain(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()
	asked := len(w.embedded())

	// The body is most of a skill, and none of what is embedded.
	p := skillParams(a.pdf)
	p.Body = "# pdf\n\nA much longer body than before."
	w.updateSkill(p)
	// A tool rediscovered with a new schema and the description it had.
	w.must(w.store.UpsertMCPTool(t.Context(), db.UpsertMCPToolParams{
		McpServerID: a.github.ID, TeamID: a.team.ID, Name: "search", Description: "Search code",
		InputSchema: []byte(`{"type":"object","required":["q"]}`),
	}))
	if w.outstanding() < 2 {
		t.Fatal("the two edits enqueued nothing, so this test would prove nothing")
	}
	w.drained()

	if got := w.embedded()[asked:]; len(got) != 0 {
		t.Errorf("edits that changed no embedded text were embedded again: %q", got)
	}
}

func TestChangingOnlyInstructionsCostsNoEmbedding(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()
	asked := len(w.embedded())

	p := agentUpdate(a.triage)
	p.Instructions = "Read the issue. Label it. Say why."
	p.MaxSteps = 9
	if _, err := w.store.UpdateAgent(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	w.drained()
	if got := w.embedded()[asked:]; len(got) != 0 {
		t.Errorf("new instructions were embedded: %q", got)
	}

	// And the other direction: a description that did change is embedded,
	// once, and the node says so.
	p.Description = "Sorts and labels incoming issues"
	if _, err := w.store.UpdateAgent(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	w.drained()

	text := "triage\nSorts and labels incoming issues"
	if got := w.embedded()[asked:]; !slices.Equal(got, []string{text}) {
		t.Errorf("after the description changed the router embedded %q, want %q once", got, text)
	}
	n := find(t, w.snapshot(a.team.ID), a.triage.ID)
	w.wantVector(n, testModel, text)
	if n.Props["description"] != "Sorts and labels incoming issues" {
		t.Errorf("description = %v", n.Props["description"])
	}
}

// A renamed thing is a different text, whatever its description.
func TestARenameIsEmbeddedAgain(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	pdf := w.skill(team, "pdf", "Extract text")
	w.drained()

	p := skillParams(pdf)
	p.Name = "pdf-reader"
	w.updateSkill(p)
	w.drained()

	n := find(t, w.snapshot(team.ID), pdf.ID)
	w.wantVector(n, testModel, "pdf-reader\nExtract text")
	if n.Props["name"] != "pdf-reader" {
		t.Errorf("name = %v", n.Props["name"])
	}
}

// The name and the description are joined with a line break, so the pair
// ("a b", "c") is not the pair ("a", "b c") and the two are told apart.
func TestTheEmbeddedTextKeepsTheNameAndDescriptionApart(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	one := w.skill(team, "pdf", "reader extracts text")
	w.drained()

	p := skillParams(one)
	p.Name, p.Description = "pdf-reader", "extracts text"
	w.updateSkill(p)
	w.drained()

	if got := w.embedded(); !slices.Equal(got, []string{"pdf\nreader extracts text", "pdf-reader\nextracts text"}) {
		t.Errorf("embedded %q", got)
	}
}

func TestAChangedModelReembeds(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()
	asked := len(w.embedded())

	w.p = w.projector("another-model", w.graph)
	w.enqueue(a.team.ID, "skill", a.pdf.ID)
	w.enqueue(a.team.ID, "agent", a.triage.ID)
	w.enqueue(a.team.ID, "mcp_tool", a.search.ID)
	w.drained()

	s := w.snapshot(a.team.ID)
	w.wantVector(find(t, s, a.pdf.ID), "another-model", "pdf\nExtract text from PDFs")
	w.wantVector(find(t, s, a.triage.ID), "another-model", "triage\nSorts incoming issues")
	w.wantVector(find(t, s, a.search.ID), "another-model", "search\nSearch code")
	// The one nothing re-projected keeps what it had, and says which model.
	w.wantVector(find(t, s, a.create.ID), testModel, "create\nOpen an issue")
	if got := len(w.embedded()) - asked; got != 3 {
		t.Errorf("%d embeddings after the model changed, want one for each of the three re-projected", got)
	}
}

// A vector with no record of what made it cannot be trusted to match.
func TestANodeWithNoFingerprintIsEmbedded(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	pdf := w.skill(team, "pdf", "Extract text")
	w.drained()

	if _, err := w.graph.run(t.Context(), team.ID, false,
		"MATCH (n:Skill {id: $id}) SET n.model = NULL, n.content_sha256 = NULL", map[string]any{"id": pdf.ID.String()}); err != nil {
		t.Fatal(err)
	}
	w.enqueue(team.ID, "skill", pdf.ID)
	w.drained()

	w.wantVector(find(t, w.snapshot(team.ID), pdf.ID), testModel, "pdf\nExtract text")
	if got := len(w.embedded()); got != 2 {
		t.Errorf("%d embeddings, want the second", got)
	}
}

// --- grants ---

func TestARevokedGrantLeavesTheGraph(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	linear := w.server(a.team, "linear")
	docx := w.skill(a.team, "docx", "Write Word documents")
	w.grant(a.triage, linear)
	w.teach(a.triage, docx)
	w.drained()

	w.must(w.store.ClearAgentMCPServers(t.Context(), a.triage.ID))
	w.grant(a.triage, linear)
	w.must(w.store.ClearAgentSkills(t.Context(), a.triage.ID))
	w.drained()

	got := w.edges(w.snapshot(a.team.ID))
	for _, gone := range []string{"Agent:triage GRANTED Toolkit:github", "Agent:triage HAS_SKILL Skill:pdf", "Agent:triage HAS_SKILL Skill:docx"} {
		if slices.Contains(got, gone) {
			t.Errorf("%q is still in the graph after it was revoked", gone)
		}
	}
	if !slices.Contains(got, "Agent:triage GRANTED Toolkit:linear") {
		t.Errorf("the grant that was kept is gone: %v", got)
	}
	// The things themselves were only un-granted.
	if !slices.Contains(got, "Team:acme OWNS Toolkit:github") || !slices.Contains(got, "Team:acme OWNS Skill:pdf") {
		t.Errorf("revoking a grant removed what was granted: %v", got)
	}
}

func TestAnAllowListReachesTheGrantedEdge(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	github, linear := w.server(team, "github"), w.server(team, "linear")
	triage := w.agent(team, "triage", "Sorts issues")
	w.grant(triage, github, "search", "create-issue")
	w.grant(triage, linear)
	w.drained()

	want := []string{
		"Agent:triage GRANTED Toolkit:github only[search create-issue]",
		"Agent:triage GRANTED Toolkit:linear",
	}
	granted := func() []string {
		var out []string
		for _, e := range w.edges(w.snapshot(team.ID)) {
			if strings.Contains(e, " GRANTED ") {
				out = append(out, e)
			}
		}
		return out
	}
	if got := granted(); !slices.Equal(got, want) {
		t.Errorf("granted:\n got %v\nwant %v", got, want)
	}

	// Widened to every tool: the list has to go, not linger.
	w.must(w.store.ClearAgentMCPServers(t.Context(), triage.ID))
	w.grant(triage, github)
	w.drained()
	if got := granted(); !slices.Equal(got, []string{"Agent:triage GRANTED Toolkit:github"}) {
		t.Errorf("after the grant was widened: %v", got)
	}
}

// --- changes that arrive together ---

func TestManyChangesToOneThingAreProjectedOnce(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	pdf := w.skill(team, "pdf", "v0")
	p := skillParams(pdf)
	for i := 1; i <= 5; i++ {
		p.Description = fmt.Sprintf("v%d", i)
		w.updateSkill(p)
	}

	if res := w.drain(); res.Projected != 6 || res.Failed != 0 {
		t.Errorf("drain = %+v, want all six rows cleared", res)
	}
	if got := w.embedded(); !slices.Equal(got, []string{"pdf\nv5"}) {
		t.Errorf("embedded %q, want the last description once", got)
	}
	if n := w.outstanding(); n != 0 {
		t.Errorf("%d rows left", n)
	}
}

// The outbox hands rows over oldest first, which is usually the order things
// were made in. Usually: a row that failed sinks behind fresh ones. Within a
// batch the projector does not depend on it.
func TestABatchIsProjectedInDependencyOrderWhateverItsOwn(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.exec("DELETE FROM graph_outbox")

	w.enqueue(a.team.ID, "agent", a.triage.ID)
	w.enqueue(a.team.ID, "mcp_tool", a.create.ID)
	w.enqueue(a.team.ID, "mcp_tool", a.search.ID)
	w.enqueue(a.team.ID, "skill", a.pdf.ID)
	w.enqueue(a.team.ID, "mcp_server", a.github.ID)

	if res := w.drain(); res.Failed != 0 || res.Projected != 5 {
		t.Fatalf("drain = %+v; logs:\n%s", res, w.logs.String())
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

// One Drain empties the outbox however small its batches.
func TestADrainRunsUntilTheOutboxIsEmpty(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	waiting := w.outstanding()

	res, err := w.p.Drain(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	// A batch of two splits a tool from its toolkit, and an agent from what
	// it was granted: those wait for their retry, and get it.
	w.retryNow()
	again, err := w.p.Drain(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Projected+again.Projected != waiting || again.Failed != 0 || w.outstanding() != 0 {
		t.Errorf("first %+v, then %+v, of %d rows; %d left", res, again, waiting, w.outstanding())
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

// --- failures ---

func TestAFailedEmbeddingLeavesTheRowToRetry(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	github := w.server(team, "github")
	pdf := w.skill(team, "pdf", "Extract text")

	w.down.Store(true)
	res := w.drain()
	if res.Failed != 1 || res.Projected != 1 {
		t.Errorf("drain = %+v, want the skill failed and the server projected", res)
	}

	// The server needs no embedding, and one failure does not stop the rest.
	w.wantGraph(team.ID, []string{"Team:acme", "Toolkit:github"}, []string{"Team:acme OWNS Toolkit:github"})

	var entity string
	var id uuid.UUID
	var attempts int
	if err := w.sql.QueryRow(t.Context(), "SELECT entity, entity_id, attempts FROM graph_outbox").Scan(&entity, &id, &attempts); err != nil {
		t.Fatalf("want exactly the skill's row left: %v", err)
	}
	if entity != "skill" || id != pdf.ID || attempts != 1 {
		t.Errorf("the row left is %s %s after %d attempts", entity, id, attempts)
	}
	if !strings.Contains(w.logs.String(), "no healthy upstream") || !strings.Contains(w.logs.String(), pdf.ID.String()) {
		t.Errorf("the failure was not logged with its reason and its row:\n%s", w.logs.String())
	}

	// It waits its turn: a drain a moment later does not hammer the router.
	asked := len(w.router.seen())
	w.down.Store(false)
	if res := w.drain(); res.Projected != 0 || res.Failed != 0 || len(w.router.seen()) != asked {
		t.Errorf("the row was retried at once: %+v", res)
	}

	w.retryNow()
	w.drained()
	s := w.wantGraph(team.ID,
		[]string{"Team:acme", "Toolkit:github", "Skill:pdf"},
		[]string{"Team:acme OWNS Toolkit:github", "Team:acme OWNS Skill:pdf"})
	w.wantVector(find(t, s, pdf.ID), testModel, "pdf\nExtract text")
	_ = github
}

// A description that changed while the router was down: the node keeps the
// vector and the text it had, together, until the retry replaces both.
func TestAFailedEmbeddingLeavesTheOldNodeWhole(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	pdf := w.skill(team, "pdf", "Extract text")
	w.drained()

	p := skillParams(pdf)
	p.Description = "Extract text and tables"
	w.updateSkill(p)
	w.down.Store(true)
	if res := w.drain(); res.Failed != 1 {
		t.Fatalf("drain = %+v", res)
	}

	n := find(t, w.snapshot(team.ID), pdf.ID)
	w.wantVector(n, testModel, "pdf\nExtract text")
	if n.Props["description"] != "Extract text" {
		t.Errorf("the description moved on to %q while its vector could not", n.Props["description"])
	}
}

// A tool whose toolkit is not in the graph is not left half-attached and
// forgotten: its row stays until the toolkit is there.
func TestAToolWaitsForItsToolkit(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	github := w.server(team, "github")
	search := w.tool(github, "search", "Search code")
	w.exec("DELETE FROM graph_outbox WHERE entity = 'mcp_server'")

	if res := w.drain(); res.Failed != 1 || w.outstanding() != 1 {
		t.Fatalf("drain = %+v with %d rows left, want the tool's row kept", res, w.outstanding())
	}
	if !strings.Contains(w.logs.String(), "no CONTAINS edge was written") {
		t.Errorf("the log does not say why:\n%s", w.logs.String())
	}

	w.enqueue(team.ID, "mcp_server", github.ID)
	w.retryNow()
	w.drained()
	w.wantGraph(team.ID,
		[]string{"Team:acme", "Toolkit:github", "Tool:search"},
		[]string{"Team:acme OWNS Toolkit:github", "Toolkit:github CONTAINS Tool:search"})
	_ = search
}

// An agent granted a server that is not in the graph yet waits the same way.
func TestAnAgentWaitsForWhatItWasGranted(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	github := w.server(team, "github")
	pdf := w.skill(team, "pdf", "Extract text")
	triage := w.agent(team, "triage", "Sorts issues")
	w.grant(triage, github)
	w.teach(triage, pdf)

	for _, missing := range []string{"mcp_server", "skill"} {
		w.exec("DELETE FROM graph_outbox WHERE entity = $1", missing)
		if res := w.drain(); res.Failed == 0 {
			t.Errorf("with the %s not projected the agent's row was cleared: %+v", missing, res)
		}
		w.enqueue(team.ID, "mcp_server", github.ID)
		w.enqueue(team.ID, "skill", pdf.ID)
		w.enqueue(team.ID, "agent", triage.ID)
		w.retryNow()
		if missing == "mcp_server" {
			w.must(w.graph.DropTeam(t.Context(), team.ID))
		}
	}
	w.drained()
	w.wantGraph(team.ID,
		[]string{"Team:acme", "Toolkit:github", "Skill:pdf", "Agent:triage"},
		[]string{
			"Team:acme OWNS Toolkit:github", "Team:acme OWNS Skill:pdf", "Team:acme OWNS Agent:triage",
			"Agent:triage GRANTED Toolkit:github", "Agent:triage HAS_SKILL Skill:pdf",
		})
}

// A row that keeps failing is a warning three times and an error after:
// the first few are a dependency arriving late, the rest are somebody's job.
func TestARowThatKeepsFailingIsLoggedAsAnError(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	w.skill(team, "pdf", "Extract text")
	w.down.Store(true)

	levels := func() []string {
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(w.logs.String()), "\n") {
			var rec struct{ Level, Msg string }
			if json.Unmarshal([]byte(line), &rec) == nil && strings.Contains(rec.Msg, "not projected") {
				out = append(out, rec.Level)
			}
		}
		return out
	}

	for range 5 {
		w.drain()
		w.retryNow()
	}
	if got := levels(); !slices.Equal(got, []string{"WARN", "WARN", "WARN", "ERROR", "ERROR"}) {
		t.Errorf("levels = %v", got)
	}
}

// The outbox can only name five things, because a constraint says so. If it
// ever names a sixth before the projector knows it, the row must stay and say
// so, not be cleared as done.
func TestAnEntityTheProjectorDoesNotKnowIsKeptAndReported(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	w.skill(team, "pdf", "Extract text")
	w.exec("ALTER TABLE graph_outbox DROP CONSTRAINT graph_outbox_entity_known")
	w.enqueue(team.ID, "capability", uuid.New())

	res := w.drain()
	if res.Failed != 1 || res.Projected != 1 || w.outstanding() != 1 {
		t.Errorf("drain = %+v with %d left, want the unknown row kept and the skill projected", res, w.outstanding())
	}
	if !strings.Contains(w.logs.String(), `capability`) {
		t.Errorf("the log does not name the entity:\n%s", w.logs.String())
	}
}

// The five names in the switch are the five the constraint allows. Each is
// written here by the trigger that owns it, never by this test, so a renamed
// entity in a migration fails here.
func TestTheProjectorKnowsEveryEntityTheTriggersWrite(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.must(w.store.DeleteTeam(t.Context(), a.team.ID))

	rows, err := w.sql.Query(t.Context(), "SELECT DISTINCT entity FROM graph_outbox ORDER BY entity")
	if err != nil {
		t.Fatal(err)
	}
	written, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}

	known := make([]string, len(projectionOrder))
	for i, e := range projectionOrder {
		known[i] = string(e)
	}
	slices.Sort(known)
	if !slices.Equal(written, known) {
		t.Errorf("the triggers write %v and the projector orders %v", written, known)
	}
	for _, e := range written {
		if err := w.p.project(t.Context(), uuid.New(), entity(e), uuid.New()); err != nil && strings.Contains(err.Error(), "nothing here projects") {
			t.Errorf("the projector does not know %q", e)
		}
	}
}

// --- teams ---

func TestDeletingATeamDropsItsGraphAndNoOther(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	globex := w.team("globex")
	slack := w.server(globex, "slack")
	w.tool(slack, "post", "Post a message")
	w.drained()

	w.must(w.store.DeleteTeam(t.Context(), a.team.ID))
	if w.outstanding() < 6 {
		t.Fatalf("deleting a team enqueued %d rows, want one for it and one for each thing it held", w.outstanding())
	}
	w.drained()

	// Not an empty graph: no graph. The cascade's rows each say "this is
	// gone", and a delete sent to a graph that is not there would make one.
	graphs, err := w.graph.db.ListGraphs()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(graphs, graphName(a.team.ID)) {
		t.Errorf("the deleted team still has a graph, holding %v", w.nodes(w.snapshot(a.team.ID)))
	}

	w.wantGraph(globex.ID,
		[]string{"Team:globex", "Toolkit:slack", "Tool:post"},
		[]string{"Team:globex OWNS Toolkit:slack", "Toolkit:slack CONTAINS Tool:post"})
}

// A team made and deleted between two drains never had a graph, and must not
// get one.
func TestATeamDeletedBeforeItWasEverProjectedGetsNoGraph(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.must(w.store.DeleteTeam(t.Context(), a.team.ID))
	w.drained()

	graphs, err := w.graph.db.ListGraphs()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(graphs, graphName(a.team.ID)) {
		t.Error("a team that was deleted before its first drain was given a graph")
	}
	if got := w.embedded(); len(got) != 0 {
		t.Errorf("a deleted team's rows were embedded: %q", got)
	}
}

// Each team's rows land in that team's graph and nowhere else.
func TestOneTeamsRowsNeverReachAnothersGraph(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	globex := w.team("globex")
	w.skill(globex, "docx", "Write Word documents")
	w.drained()

	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
	w.wantGraph(globex.ID, []string{"Team:globex", "Skill:docx"}, []string{"Team:globex OWNS Skill:docx"})

	// Only triggers write the outbox, and they copy the team from the row.
	// If a row ever named the wrong team, the thing it names must not be
	// copied into that team's graph: the row says whose it is, not the outbox.
	w.enqueue(globex.ID, "skill", a.pdf.ID)
	w.enqueue(globex.ID, "agent", a.triage.ID)
	w.enqueue(globex.ID, "mcp_server", a.github.ID)
	w.enqueue(globex.ID, "mcp_tool", a.search.ID)
	w.drained()

	w.wantGraph(globex.ID, []string{"Team:globex", "Skill:docx"}, []string{"Team:globex OWNS Skill:docx"})
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

// A team that cannot be read is not a team that was deleted.
func TestATeamThatCannotBeReadKeepsItsGraphAndItsRows(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()

	p := skillParams(a.pdf)
	p.Description = "Changed"
	w.updateSkill(p)
	w.store.getTeam = errors.New("the database went away")

	res := w.drain()
	if res.Failed != 1 || res.Projected != 0 || w.outstanding() != 1 {
		t.Errorf("drain = %+v with %d left, want the row kept", res, w.outstanding())
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
	if !strings.Contains(w.logs.String(), "the database went away") {
		t.Errorf("not logged:\n%s", w.logs.String())
	}
}

// The index is built for one width. A projector started with another must
// not write a vector nothing can search; it leaves the rows and says what to
// run.
func TestAGraphBuiltForAnotherWidthIsLeftAlone(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()

	wide, err := Dial(t.Context(), liveFalkorURL(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wide.Close() })
	w.p = w.projector("a-wider-model", wide)

	p := skillParams(a.pdf)
	p.Description = "Changed"
	w.updateSkill(p)

	if res := w.drain(); res.Failed != 1 || w.outstanding() != 1 {
		t.Errorf("drain = %+v with %d left", res, w.outstanding())
	}
	if !strings.Contains(w.logs.String(), "brain graph rebuild") {
		t.Errorf("the log does not say what to run:\n%s", w.logs.String())
	}
	w.wantVector(find(t, w.snapshot(a.team.ID), a.pdf.ID), testModel, "pdf\nExtract text from PDFs")
}

// --- the outbox ---

// A row is cleared only after its change reached the graph. If clearing
// fails the drain says so, and the rows are still there to run again.
func TestRowsThatCannotBeClearedAreKept(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	waiting := w.outstanding()
	w.store.forget = errors.New("the database went away")

	if _, err := w.p.Drain(t.Context(), 100); err == nil || !strings.Contains(err.Error(), "the database went away") {
		t.Errorf("Drain: err = %v", err)
	}
	if got := w.outstanding(); got != waiting {
		t.Errorf("%d rows left of %d", got, waiting)
	}

	w.store.forget = nil
	w.retryNow()
	w.drained()
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

func TestAClaimThatFailsIsTheDrainsError(t *testing.T) {
	w := newWorld(t)
	w.acme()
	w.store.claim = errors.New("the database went away")

	res, err := w.p.Drain(t.Context(), 100)
	if err == nil || !strings.Contains(err.Error(), "the database went away") || res.Busy {
		t.Errorf("Drain = %+v, %v", res, err)
	}
}

// A change made while the projector was reading has a row of its own, which
// this drain did not claim and must not clear. The graph is stale for one
// drain, and the next one catches it up.
func TestAChangeMadeDuringADrainIsNotLost(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	pdf := w.skill(team, "pdf", "first")

	w.store.onGetSkill = func() {
		p := skillParams(pdf)
		p.Description = "second"
		w.updateSkill(p)
	}
	res, err := w.p.Drain(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	// One Drain may run to the end of the outbox, and so project it twice.
	// What it may not do is finish with the graph behind and nothing queued.
	_ = res
	if w.outstanding() != 0 {
		w.drained()
	}
	if n := find(t, w.snapshot(team.ID), pdf.ID); n.Props["description"] != "second" {
		t.Errorf("the graph says %q and the outbox is empty", n.Props["description"])
	}
	w.wantVector(find(t, w.snapshot(team.ID), pdf.ID), testModel, "pdf\nsecond")
}

// --- one drain at a time ---

func TestADrainDoesNothingWhileAnotherHoldsTheLock(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	waiting := w.outstanding()

	held, err := w.store.WithAdvisoryLock(t.Context(), w.p.lockID, func(ctx context.Context) error {
		res, err := w.p.Drain(ctx, 100)
		if err != nil || !res.Busy || res.Projected != 0 || res.Failed != 0 {
			t.Errorf("Drain under another's lock = %+v, %v; want busy and nothing done", res, err)
		}
		return nil
	})
	if err != nil || !held {
		t.Fatalf("the test could not take the lock: %v %v", held, err)
	}

	var attempted int
	if err := w.sql.QueryRow(t.Context(), "SELECT count(*) FROM graph_outbox WHERE attempts > 0").Scan(&attempted); err != nil {
		t.Fatal(err)
	}
	if attempted != 0 || w.outstanding() != waiting || len(w.embedded()) != 0 {
		t.Errorf("the turned-away drain claimed %d rows and embedded %d texts", attempted, len(w.embedded()))
	}
	if graphs, _ := w.graph.db.ListGraphs(); slices.Contains(graphs, graphName(a.team.ID)) {
		t.Error("the turned-away drain wrote to the graph")
	}

	// And with the lock free again it is not busy.
	w.drained()
}

// Two projectors in one process share the lock: it is the key that matters,
// not who made the projector.
func TestTwoProjectorsShareOneLock(t *testing.T) {
	w := newWorld(t)
	w.acme()
	other := w.projector(testModel, w.graph)
	other.lockID = w.p.lockID

	w.store.onGetSkill = func() {
		res, err := other.Drain(t.Context(), 100)
		if err != nil || !res.Busy {
			t.Errorf("a second drain ran inside the first: %+v, %v", res, err)
		}
	}
	w.drained()
	if w.store.onGetSkill != nil {
		t.Fatal("the first drain never read a skill, so the second was never tried")
	}
}

// The key every worker shares in production, and not the one migrations take.
func TestTheDrainLockIsItsOwn(t *testing.T) {
	t.Parallel()

	if want := int64(crc32.ChecksumIEEE([]byte("openarity.graph"))); drainLockID != want {
		t.Errorf("drainLockID = %d, want crc32 of openarity.graph, %d", drainLockID, want)
	}
	if migrations := int64(crc32.ChecksumIEEE([]byte("openarity"))); drainLockID == migrations {
		t.Error("the drain shares its lock with migrations, so a worker would hold up a deploy")
	}
	if p := NewProjector(nil, nil, nil, nil); p.lockID != drainLockID {
		t.Errorf("a new projector locks on %d, so two workers would not exclude each other", p.lockID)
	}
}

func TestACancelledDrainIsAnErrorAndClaimsNothing(t *testing.T) {
	w := newWorld(t)
	w.acme()
	waiting := w.outstanding()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// Not busy: nobody else holds the lock. It was never asked for.
	if res, err := w.p.Drain(ctx, 100); !errors.Is(err, context.Canceled) || res.Busy {
		t.Errorf("Drain = %+v, %v", res, err)
	}
	if got := w.outstanding(); got != waiting {
		t.Errorf("%d rows left of %d", got, waiting)
	}
}

// Cancelled before anything was projected, there is nothing to clear and so
// no failed write to carry the news. The drain still has to say it stopped.
func TestADrainCancelledBeforeItsFirstWriteSaysSo(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	w.skill(team, "pdf", "Extract text")

	ctx, cancel := context.WithCancel(t.Context())
	w.store.onGetSkill = cancel
	res, err := w.p.Drain(ctx, 100)
	if !errors.Is(err, context.Canceled) || res.Projected != 0 {
		t.Errorf("Drain = %+v, %v; want the cancellation and nothing projected", res, err)
	}
	if w.outstanding() != 1 {
		t.Errorf("%d rows left, want the one", w.outstanding())
	}
}

// Cancelled part-way, a drain stops and says so: it does not report a clean
// run with rows still waiting.
func TestADrainCancelledPartWayStops(t *testing.T) {
	w := newWorld(t)
	w.acme()

	ctx, cancel := context.WithCancel(t.Context())
	w.store.onGetSkill = cancel
	if _, err := w.p.Drain(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Errorf("Drain: err = %v, want the cancellation", err)
	}
	if w.outstanding() == 0 {
		t.Error("a cancelled drain cleared every row")
	}
	// It stops; it does not walk the rest of the batch failing each row in
	// turn. The skill was mid-flight and is the one failure there is to log.
	if n := strings.Count(w.logs.String(), "a change was not projected"); n != 1 {
		t.Errorf("%d failures were logged after the cancellation, want only the one in flight:\n%s", n, w.logs.String())
	}

	w.retryNow()
	w.drained()
}

// --- the pure parts ---

func TestCoalesceKeepsEveryRowAndTheMostAttempts(t *testing.T) {
	t.Parallel()

	x, y := uuid.New(), uuid.New()
	got := coalesce([]db.GraphOutbox{
		{ID: 1, Entity: "agent", EntityID: x, Attempts: 1},
		{ID: 2, Entity: "skill", EntityID: x, Attempts: 4},
		{ID: 3, Entity: "agent", EntityID: x, Attempts: 7},
		{ID: 8, Entity: "agent", EntityID: x, Attempts: 3},
		{ID: 4, Entity: "mcp_tool", EntityID: y, Attempts: 2},
		{ID: 5, Entity: "mcp_server", EntityID: y},
		{ID: 6, Entity: "agent", EntityID: y, Attempts: 3},
		{ID: 7, Entity: "team", EntityID: y},
	})

	var lines []string
	for _, c := range got {
		who := map[uuid.UUID]string{x: "x", y: "y"}[c.id]
		lines = append(lines, fmt.Sprintf("%s %s %v x%d", c.entity, who, c.rows, c.attempts))
	}
	// The same id under two entities is two things, and stays two.
	want := []string{
		"team y [7] x0", "mcp_server y [5] x0", "skill x [2] x4", "mcp_tool y [4] x2",
		"agent x [1 3 8] x7", "agent y [6] x3",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("coalesce:\n got %v\nwant %v", lines, want)
	}
}

func TestGoneMeansAbsentOrSomebodyElses(t *testing.T) {
	t.Parallel()

	mine, theirs := uuid.New(), uuid.New()
	for name, tc := range map[string]struct {
		err   error
		owner uuid.UUID
		want  bool
	}{
		"the row is there and this team's":  {nil, mine, false},
		"no row":                            {pgx.ErrNoRows, uuid.Nil, true},
		"no row, wrapped":                   {fmt.Errorf("reading: %w", pgx.ErrNoRows), uuid.Nil, true},
		"another team's row":                {nil, theirs, true},
		"the read failed":                   {errors.New("connection reset"), uuid.Nil, false},
		"the read failed with a zero owner": {errors.New("connection reset"), mine, false},
	} {
		if got := gone(tc.err, tc.owner, mine); got != tc.want {
			t.Errorf("%s: gone = %v", name, got)
		}
	}
}
