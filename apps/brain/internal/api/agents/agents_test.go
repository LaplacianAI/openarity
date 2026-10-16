package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// errAny stands in for any failure of ours.
var errAny = errors.New("connection reset by peer")

// --- fakes ---

// fakeStore keeps the constraints a handler relies on: a grant must name a
// server or skill of the same team (23503, by the constraint's name), and a
// name is unique per team case-insensitively (23505). InTx snapshots and
// restores, so a rolled-back write is visibly absent.
type fakeStore struct {
	mu      sync.Mutex
	agents  map[uuid.UUID]db.Agent
	servers map[uuid.UUID][]db.AgentMcpServer
	skills  map[uuid.UUID][]uuid.UUID

	// What exists in each team to be granted.
	ownServers map[uuid.UUID]uuid.UUID
	ownSkills  map[uuid.UUID]uuid.UUID

	readErr, listErr, serversErr, skillsErr, deleteErr error
	createErr, updateErr, clearErr                     error

	reads, txs int
	created    []db.CreateAgentParams
	updated    []db.UpdateAgentParams
	deleted    []uuid.UUID
	listArgs   []db.ListAgentsByTeamParams
}

func newFakeStore(existing ...db.Agent) *fakeStore {
	s := &fakeStore{
		agents:     map[uuid.UUID]db.Agent{},
		servers:    map[uuid.UUID][]db.AgentMcpServer{},
		skills:     map[uuid.UUID][]uuid.UUID{},
		ownServers: map[uuid.UUID]uuid.UUID{},
		ownSkills:  map[uuid.UUID]uuid.UUID{},
	}
	for _, a := range existing {
		s.agents[a.ID] = a
	}
	return s
}

func (s *fakeStore) touched() bool {
	return s.reads != 0 || len(s.listArgs) != 0 || s.txs != 0 || len(s.deleted) != 0
}

func (s *fakeStore) server(team uuid.UUID) uuid.UUID {
	id := uuid.New()
	s.ownServers[id] = team
	return id
}

func (s *fakeStore) skill(team uuid.UUID) uuid.UUID {
	id := uuid.New()
	s.ownSkills[id] = team
	return id
}

func (s *fakeStore) InTx(_ context.Context, fn func(Queries) error) error {
	s.txs++
	s.mu.Lock()
	agents, servers, skills := maps.Clone(s.agents), maps.Clone(s.servers), maps.Clone(s.skills)
	s.mu.Unlock()

	if err := fn(s); err != nil {
		s.mu.Lock()
		s.agents, s.servers, s.skills = agents, servers, skills
		s.mu.Unlock()
		return err
	}
	return nil
}

func constraint(code, name string) error {
	return &pgconn.PgError{Code: code, ConstraintName: name}
}

func (s *fakeStore) nameTaken(team uuid.UUID, name string, except uuid.UUID) bool {
	for _, a := range s.agents {
		if a.TeamID == team && a.ID != except && strings.EqualFold(a.Name, name) {
			return true
		}
	}
	return false
}

func (s *fakeStore) CreateAgent(_ context.Context, p db.CreateAgentParams) (db.Agent, error) {
	s.created = append(s.created, p)
	if s.createErr != nil {
		return db.Agent{}, s.createErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nameTaken(p.TeamID, p.Name, uuid.Nil) {
		return db.Agent{}, constraint("23505", "agents_team_name_key")
	}
	now := time.Now()
	a := db.Agent{
		ID: uuid.New(), TeamID: p.TeamID, Name: p.Name, Description: p.Description, Kind: p.Kind,
		Instructions: p.Instructions, ModelName: p.ModelName, MaxTokens: p.MaxTokens, Temperature: p.Temperature,
		Pattern: p.Pattern, MaxSteps: p.MaxSteps, SteerContinuations: p.SteerContinuations,
		OutputSchema: p.OutputSchema, Parser: p.Parser, ParserModelName: p.ParserModelName,
		ParserMaxTokens: p.ParserMaxTokens, ParserTemperature: p.ParserTemperature,
		CreatedAt: now, UpdatedAt: now,
	}
	s.agents[a.ID] = a
	return a, nil
}

func (s *fakeStore) UpdateAgent(_ context.Context, p db.UpdateAgentParams) (db.Agent, error) {
	s.updated = append(s.updated, p)
	if s.updateErr != nil {
		return db.Agent{}, s.updateErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[p.ID]
	if !ok {
		return db.Agent{}, pgx.ErrNoRows
	}
	if s.nameTaken(a.TeamID, p.Name, a.ID) {
		return db.Agent{}, constraint("23505", "agents_team_name_key")
	}
	a.Name, a.Description, a.Instructions = p.Name, p.Description, p.Instructions
	a.ModelName, a.MaxTokens, a.Temperature = p.ModelName, p.MaxTokens, p.Temperature
	a.Pattern, a.MaxSteps, a.SteerContinuations = p.Pattern, p.MaxSteps, p.SteerContinuations
	a.OutputSchema, a.Parser = p.OutputSchema, p.Parser
	a.ParserModelName, a.ParserMaxTokens, a.ParserTemperature = p.ParserModelName, p.ParserMaxTokens, p.ParserTemperature
	a.UpdatedAt = time.Now()
	s.agents[a.ID] = a
	return a, nil
}

func (s *fakeStore) AddAgentMCPServer(_ context.Context, p db.AddAgentMCPServerParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if team, ok := s.ownServers[p.McpServerID]; !ok || team != p.TeamID {
		return constraint("23503", "agent_mcp_servers_server_in_team")
	}
	s.servers[p.AgentID] = append(s.servers[p.AgentID], db.AgentMcpServer(p))
	return nil
}

func (s *fakeStore) ClearAgentMCPServers(_ context.Context, id uuid.UUID) error {
	if s.clearErr != nil {
		return s.clearErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.servers, id)
	return nil
}

func (s *fakeStore) AddAgentSkills(_ context.Context, p db.AddAgentSkillsParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range p.SkillIds {
		if team, ok := s.ownSkills[id]; !ok || team != p.TeamID {
			return constraint("23503", "agent_skills_skill_in_team")
		}
	}
	s.skills[p.AgentID] = append(s.skills[p.AgentID], p.SkillIds...)
	return nil
}

func (s *fakeStore) ClearAgentSkills(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, id)
	return nil
}

func (s *fakeStore) GetAgent(_ context.Context, id uuid.UUID) (db.Agent, error) {
	s.reads++
	if s.readErr != nil {
		return db.Agent{}, s.readErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return db.Agent{}, pgx.ErrNoRows
	}
	return a, nil
}

// The real queries order by id; so does this.
func (s *fakeStore) ListAgentMCPServers(_ context.Context, id uuid.UUID) ([]db.AgentMcpServer, error) {
	if s.serversErr != nil {
		return nil, s.serversErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.servers[id])
	slices.SortFunc(out, func(a, b db.AgentMcpServer) int { return bytes.Compare(a.McpServerID[:], b.McpServerID[:]) })
	return out, nil
}

func (s *fakeStore) ListAgentSkills(_ context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	if s.skillsErr != nil {
		return nil, s.skillsErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.skills[id])
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out, nil
}

func (s *fakeStore) DeleteAgent(_ context.Context, id uuid.UUID) error {
	s.deleted = append(s.deleted, id)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, id)
	delete(s.servers, id)
	delete(s.skills, id)
	return nil
}

func (s *fakeStore) ListAgentsByTeam(_ context.Context, p db.ListAgentsByTeamParams) ([]db.Agent, error) {
	s.listArgs = append(s.listArgs, p)
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []db.Agent
	for _, a := range s.agents {
		if a.TeamID != p.TeamID {
			continue
		}
		if p.UseCursor && !before(a, p.AfterCreatedAt, p.AfterID) {
			continue
		}
		rows = append(rows, a)
	}
	slices.SortFunc(rows, func(a, b db.Agent) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return bytes.Compare(b.ID[:], a.ID[:])
	})
	return rows[:min(len(rows), int(p.PageSize))], nil
}

// before is (created_at, id) < (at, id), as the query compares rows.
func before(a db.Agent, at time.Time, id uuid.UUID) bool {
	if c := a.CreatedAt.Compare(at); c != 0 {
		return c < 0
	}
	return bytes.Compare(a.ID[:], id[:]) < 0
}

type fakeAuthz struct {
	allowed bool
	asked   []authz.Action
}

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	f.asked = append(f.asked, a)
	return f.allowed, nil
}

func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("an agents route used the strictly weaker CanInAnyTeam")
}

// agentRoutes is what rbac.json maps for this package, behind the real guard.
func agentRoutes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	add := func(method, path, scope string, permission *string) {
		t.Helper()
		if err := rs.Add(method, path, scope, permission); err != nil {
			t.Fatalf("Add %s %s: %v", method, path, err)
		}
	}
	write := "agent:write"
	add("GET", "/teams/{id}/agents", "member", nil)
	add("POST", "/teams/{id}/agents", "team", &write)
	add("GET", "/teams/{id}/agents/{agentID}", "member", nil)
	add("PUT", "/teams/{id}/agents/{agentID}", "team", &write)
	add("DELETE", "/teams/{id}/agents/{agentID}", "team", &write)
	return rs
}

func call(t *testing.T, s *fakeStore, a *fakeAuthz, u *auth.User, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), s).Register(mux, api.NewGuard(agentRoutes(t), a, discardLogger()))

	var in io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		in = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		in = bytes.NewReader(raw)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, in)
	req.Header.Set("Content-Type", "application/json")
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func memberOf(team uuid.UUID) *auth.User {
	return &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: team, Name: "platform", Role: "member"}},
	}
}

func outsider() *auth.User { return &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"} }

func anAgent(team uuid.UUID, name string) db.Agent {
	return db.Agent{
		ID: uuid.New(), TeamID: team, Name: name, Kind: "custom", ModelName: "m", MaxTokens: 100,
		Pattern: "react", MaxSteps: 4, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func body(name string) map[string]any {
	return map[string]any{
		"name": name, "pattern": "react", "max_steps": 8,
		"model": map[string]any{"name": "anthropic/claude-opus-5", "max_tokens": 4096},
	}
}

func collection(team uuid.UUID) string { return "/teams/" + team.String() + "/agents" }

func one(team, id uuid.UUID) string { return collection(team) + "/" + id.String() }

func decode(t *testing.T, rec *httptest.ResponseRecorder) detail {
	t.Helper()
	var d detail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("body: %v (%s)", err, rec.Body)
	}
	return d
}

func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

// --- authorisation ---

func TestWritesNeedAgentWrite(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		method string
		path   func(db.Agent) string
		body   func(db.Agent) any
		ok     int
	}{
		"create":  {http.MethodPost, func(a db.Agent) string { return collection(a.TeamID) }, func(db.Agent) any { return body("new") }, http.StatusCreated},
		"replace": {http.MethodPut, func(a db.Agent) string { return one(a.TeamID, a.ID) }, func(a db.Agent) any { return body(a.Name) }, http.StatusOK},
		"delete":  {http.MethodDelete, func(a db.Agent) string { return one(a.TeamID, a.ID) }, func(db.Agent) any { return nil }, http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, allowed := range []bool{true, false} {
				a := anAgent(uuid.New(), "triage")
				s := newFakeStore(a)
				z := &fakeAuthz{allowed: allowed}
				rec := call(t, s, z, memberOf(a.TeamID), tc.method, tc.path(a), tc.body(a))

				want := tc.ok
				if !allowed {
					want = http.StatusForbidden
				}
				if rec.Code != want {
					t.Errorf("allowed=%v: %d, want %d (%s)", allowed, rec.Code, want, rec.Body)
				}
				if len(z.asked) != 1 || z.asked[0] != authz.Action("agent:write") {
					t.Errorf("the guard asked for %v", z.asked)
				}
				if !allowed && s.touched() {
					t.Error("a refused write reached the store")
				}
			}
		})
	}
}

func TestReadsAreForMembersAndHiddenFromOutsiders(t *testing.T) {
	t.Parallel()

	for name, path := range map[string]func(db.Agent) string{
		"list": func(a db.Agent) string { return collection(a.TeamID) },
		"get":  func(a db.Agent) string { return one(a.TeamID, a.ID) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a := anAgent(uuid.New(), "triage")
			z := &fakeAuthz{}
			if rec := call(t, newFakeStore(a), z, memberOf(a.TeamID), http.MethodGet, path(a), nil); rec.Code != http.StatusOK {
				t.Errorf("member: %d (%s)", rec.Code, rec.Body)
			}
			if len(z.asked) != 0 {
				t.Errorf("a read asked for %v", z.asked)
			}

			s := newFakeStore(a)
			if rec := call(t, s, z, outsider(), http.MethodGet, path(a), nil); rec.Code != http.StatusNotFound {
				t.Errorf("outsider: %d, want 404", rec.Code)
			}
			if s.touched() {
				t.Error("an outsider's read reached the store")
			}
		})
	}
}

func TestEveryRouteRefusesARequestWithNoUser(t *testing.T) {
	t.Parallel()

	team, id := uuid.New(), uuid.New()
	for name, tc := range map[string]struct{ method, path string }{
		"list":    {http.MethodGet, collection(team)},
		"create":  {http.MethodPost, collection(team)},
		"get":     {http.MethodGet, one(team, id)},
		"replace": {http.MethodPut, one(team, id)},
		"delete":  {http.MethodDelete, one(team, id)},
	} {
		s := newFakeStore()
		rec := call(t, s, &fakeAuthz{allowed: true}, nil, tc.method, tc.path, body("x"))
		if rec.Code != http.StatusInternalServerError || s.touched() {
			t.Errorf("%s: %d, touched %v; want 500 and nothing reached", name, rec.Code, s.touched())
		}
	}
}

func TestUndeclaredMethodsDoNotAnswer(t *testing.T) {
	t.Parallel()

	team, id := uuid.New(), uuid.New()
	for name, tc := range map[string]struct{ method, path string }{
		"PUT on the collection":    {http.MethodPut, collection(team)},
		"DELETE on the collection": {http.MethodDelete, collection(team)},
		"POST on one agent":        {http.MethodPost, one(team, id)},
		"PATCH on one agent":       {http.MethodPatch, one(team, id)},
	} {
		s := newFakeStore()
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), tc.method, tc.path, nil)
		if rec.Code != http.StatusMethodNotAllowed || s.touched() {
			t.Errorf("%s: %d, touched %v; want 405", name, rec.Code, s.touched())
		}
	}
}

// --- the wire contract ---

func keysOf(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not an object: %v (%s)", err, raw)
	}
	return slices.Sorted(maps.Keys(m))
}

var summaryKeys = []string{"created_at", "description", "id", "kind", "model", "name", "pattern", "team_id", "updated_at"}

// A list item is a summary, a single agent is the detail, and the optional
// parts are absent rather than null when they are not set.
func TestOnlyContractedFieldsAreSerialised(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	plain := anAgent(team, "plain")
	full := anAgent(team, "full")
	full.OutputSchema = []byte(`{"name":"v","json":{"type":"object"},"strict":true}`)
	full.Parser = true
	full.ParserModelName = new("p")
	s := newFakeStore(plain, full)

	detailKeys := slices.Sorted(slices.Values(append(slices.Clone(summaryKeys),
		"instructions", "max_steps", "steer_continuations", "parser", "mcp_servers", "skills")))
	if got := keysOf(t, call(t, s, &fakeAuthz{}, memberOf(team), http.MethodGet, one(team, plain.ID), nil).Body.Bytes()); !slices.Equal(got, detailKeys) {
		t.Errorf("plain agent keys = %v, want %v", got, detailKeys)
	}

	withOptional := slices.Sorted(slices.Values(append(slices.Clone(detailKeys), "output_schema", "parser_model")))
	if got := keysOf(t, call(t, s, &fakeAuthz{}, memberOf(team), http.MethodGet, one(team, full.ID), nil).Body.Bytes()); !slices.Equal(got, withOptional) {
		t.Errorf("full agent keys = %v, want %v", got, withOptional)
	}

	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(call(t, s, &fakeAuthz{}, memberOf(team), http.MethodGet, collection(team), nil).Body.Bytes(), &page); err != nil || len(page.Items) != 2 {
		t.Fatalf("page: %v", err)
	}
	for _, item := range page.Items {
		if got := keysOf(t, item); !slices.Equal(got, summaryKeys) {
			t.Errorf("list item keys = %v, want %v", got, summaryKeys)
		}
	}
}

// Grants are arrays even when there are none: "none granted" is a value.
func TestAnAgentWithNoGrantsHasEmptyArrays(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "triage")
	rec := call(t, newFakeStore(a), &fakeAuthz{}, memberOf(a.TeamID), http.MethodGet, one(a.TeamID, a.ID), nil)
	if !strings.Contains(rec.Body.String(), `"mcp_servers":[]`) || !strings.Contains(rec.Body.String(), `"skills":[]`) {
		t.Errorf("body = %s", rec.Body)
	}
}

// --- create ---

func TestCreateStoresEveryField(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	b := body("triage")
	b["description"], b["instructions"], b["steer_continuations"] = "Sorts issues", "You sort issues.", 2
	b["model"] = map[string]any{"name": "anthropic/claude-opus-5", "max_tokens": 4096, "temperature": 0.3}
	b["output_schema"] = map[string]any{"name": "verdict", "description": "d", "json": map[string]any{"type": "object"}, "strict": true}
	b["parser"] = true
	b["parser_model"] = map[string]any{"name": "small", "max_tokens": 256, "temperature": 0.0}

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), b)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}

	p := s.created[0]
	if p.TeamID != team || p.Kind != "custom" || p.Name != "triage" || p.Description != "Sorts issues" ||
		p.Instructions != "You sort issues." || p.ModelName != "anthropic/claude-opus-5" || p.MaxTokens != 4096 ||
		p.Temperature == nil || *p.Temperature != 0.3 || p.Pattern != "react" || p.MaxSteps != 8 ||
		p.SteerContinuations != 2 || !p.Parser || p.ParserModelName == nil || *p.ParserModelName != "small" ||
		p.ParserMaxTokens == nil || *p.ParserMaxTokens != 256 || p.ParserTemperature == nil || *p.ParserTemperature != 0 {
		t.Errorf("created %+v", p)
	}
	var o outputSchema
	if err := json.Unmarshal(p.OutputSchema, &o); err != nil || o.Name != "verdict" || o.Description != "d" || !o.Strict ||
		string(o.JSON) != `{"type":"object"}` {
		t.Errorf("output_schema stored as %s", p.OutputSchema)
	}

	got := decode(t, rec)
	if got.Kind != KindCustom || got.OutputSchema == nil || got.OutputSchema.Name != "verdict" ||
		got.ParserModel == nil || got.ParserModel.Name != "small" || got.Model.Temperature == nil {
		t.Errorf("response = %+v", got)
	}
}

func TestAnAgentWithoutOptionalPartsStoresNulls(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	if rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), body("triage")); rec.Code != http.StatusCreated {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	p := s.created[0]
	if p.OutputSchema != nil || p.ParserModelName != nil || p.ParserMaxTokens != nil || p.ParserTemperature != nil || p.Temperature != nil {
		t.Errorf("created %+v, want no optional parts", p)
	}
}

func TestCreatingANonCustomKindIsRefused(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"orchestrator", "chat", "Custom", "anything"} {
		team := uuid.New()
		s := newFakeStore()
		b := body("triage")
		b["kind"] = kind
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), b)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kind must be custom") {
			t.Errorf("kind %q: %d %q", kind, rec.Code, rec.Body)
		}
		if s.txs != 0 {
			t.Errorf("kind %q reached the store", kind)
		}
	}

	team := uuid.New()
	b := body("triage")
	b["kind"] = "custom"
	if rec := call(t, newFakeStore(), &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), b); rec.Code != http.StatusCreated {
		t.Errorf("kind custom spelled out: %d (%s)", rec.Code, rec.Body)
	}
}

func TestARefusedBodyNeverReachesTheStore(t *testing.T) {
	t.Parallel()

	for name, b := range map[string]any{
		"not json":         "{",
		"an unknown field": map[string]any{"name": "x", "pattern": "react", "max_steps": 1, "model": map[string]any{"name": "m", "max_tokens": 1}, "secret": 1},
		"a rule broken":    map[string]any{"name": "x", "pattern": "react", "max_steps": 0, "model": map[string]any{"name": "m", "max_tokens": 1}},
		"a bad uuid":       map[string]any{"name": "x", "pattern": "react", "max_steps": 1, "model": map[string]any{"name": "m", "max_tokens": 1}, "skills": []string{"nope"}},
	} {
		team := uuid.New()
		s := newFakeStore()
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), b)
		if rec.Code != http.StatusBadRequest || s.touched() {
			t.Errorf("%s: %d, touched %v (%s)", name, rec.Code, s.touched(), rec.Body)
		}
	}
}

// The composite foreign key is the check: a grant naming another team's
// server or skill fails inside the transaction, which takes the agent row
// with it.
func TestAGrantOfAnotherTeamsServerIsRefusedAndNothingWasWritten(t *testing.T) {
	t.Parallel()

	for name, grant := range map[string]func(s *fakeStore, b map[string]any){
		"a server of another team": func(s *fakeStore, b map[string]any) {
			b["mcp_servers"] = []map[string]any{{"id": s.server(uuid.New())}}
		},
		"a server that does not exist": func(_ *fakeStore, b map[string]any) {
			b["mcp_servers"] = []map[string]any{{"id": uuid.New()}}
		},
		"a skill of another team": func(s *fakeStore, b map[string]any) {
			b["skills"] = []uuid.UUID{s.skill(uuid.New())}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			team := uuid.New()
			s := newFakeStore()
			b := body("triage")
			grant(s, b)

			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), b)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not exist in this team") {
				t.Errorf("%d %q", rec.Code, rec.Body)
			}
			if len(s.agents) != 0 {
				t.Error("the agent was written although its grant was refused")
			}
		})
	}
}

func TestTheResponseCarriesTheGrants(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	a, b, c := s.server(team), s.server(team), s.server(team)
	k1, k2 := s.skill(team), s.skill(team)

	req := body("triage")
	req["mcp_servers"] = []map[string]any{{"id": a, "allow": []string{"search", "create"}}, {"id": b}, {"id": c, "allow": []string{"x"}}}
	req["skills"] = []uuid.UUID{k2, k1}

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	created := decode(t, rec)

	ids := make([]uuid.UUID, len(created.MCPServers))
	for i, g := range created.MCPServers {
		ids[i] = g.ID
		switch g.ID {
		case a:
			if !slices.Equal(g.Allow, []string{"search", "create"}) {
				t.Errorf("a's allow = %v, want it in the order sent", g.Allow)
			}
		case b:
			if g.Allow != nil {
				t.Errorf("b's allow = %v, want absent: every tool", g.Allow)
			}
		}
	}
	if !slices.Equal(ids, sortedIDs(a, b, c)) {
		t.Errorf("servers in order %v, want ordered by id", ids)
	}
	if !slices.Equal(created.Skills, sortedIDs(k1, k2)) {
		t.Errorf("skills = %v, want ordered by id", created.Skills)
	}

	// And GET answers in the same shape.
	got := decode(t, call(t, s, &fakeAuthz{}, memberOf(team), http.MethodGet, one(team, created.ID), nil))
	if !slices.EqualFunc(got.MCPServers, created.MCPServers, func(x, y serverGrant) bool {
		return x.ID == y.ID && slices.Equal(x.Allow, y.Allow)
	}) || !slices.Equal(got.Skills, created.Skills) {
		t.Errorf("GET grants %+v %v differ from POST's %+v %v", got.MCPServers, got.Skills, created.MCPServers, created.Skills)
	}
	if !strings.Contains(rec.Body.String(), `{"id":"`+b.String()+`"}`) {
		t.Errorf("an unrestricted grant should carry no allow at all: %s", rec.Body)
	}
}

func TestADuplicateNameIsAConflict(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	taken, mine := anAgent(team, "Triage"), anAgent(team, "other")

	rec := call(t, newFakeStore(taken), &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), body("triage"))
	if rec.Code != http.StatusConflict {
		t.Errorf("create: %d", rec.Code)
	}
	rec = call(t, newFakeStore(taken, mine), &fakeAuthz{allowed: true}, memberOf(team), http.MethodPut, one(team, mine.ID), body("TRIAGE"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("replace: %d %q", rec.Code, rec.Body)
	}
}

// The database holds the list of patterns, so a pattern it refuses is a
// caller's mistake by its constraint's name — and only that constraint's.
func TestConstraintsAreAnsweredByName(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		code int
		says string
	}{
		"an unknown pattern":       {constraint("23514", "agents_pattern_known"), http.StatusBadRequest, "pattern is not one the brain runs"},
		"another check":            {constraint("23514", "agents_max_steps_positive"), http.StatusInternalServerError, "internal server error"},
		"another foreign key":      {constraint("23503", "agents_team_id_fkey"), http.StatusInternalServerError, "internal server error"},
		"a uniqueness by any name": {constraint("23505", "whatever"), http.StatusConflict, "already exists"},
		"no such row":              {pgx.ErrNoRows, http.StatusNotFound, "not found"},
		"a wrapped constraint":     {errors.Join(errAny, constraint("23514", "agents_pattern_known")), http.StatusBadRequest, "pattern"},
	} {
		team := uuid.New()
		s := newFakeStore()
		s.createErr = tc.err
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), body("x"))
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.says) {
			t.Errorf("%s: %d %q; want %d saying %q", name, rec.Code, rec.Body, tc.code, tc.says)
		}
	}
}

// --- one agent ---

func TestAnotherTeamsAgentIsNotFound(t *testing.T) {
	t.Parallel()

	theirs := anAgent(uuid.New(), "triage")
	mine := uuid.New()
	for name, tc := range map[string]struct {
		method string
		body   any
	}{
		"get":     {http.MethodGet, nil},
		"replace": {http.MethodPut, body("triage")},
		"delete":  {http.MethodDelete, nil},
	} {
		s := newFakeStore(theirs)
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(mine), tc.method, one(mine, theirs.ID), tc.body)
		if rec.Code != http.StatusNotFound || s.txs != 0 || len(s.deleted) != 0 {
			t.Errorf("%s: %d, txs %d, deleted %v", name, rec.Code, s.txs, s.deleted)
		}
	}
}

func TestAMissingAgentIsNotFoundAndABadIDIsABadRequest(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	if rec := call(t, newFakeStore(), &fakeAuthz{}, memberOf(team), http.MethodGet, one(team, uuid.New()), nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing: %d", rec.Code)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		s := newFakeStore()
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(team), method, collection(team)+"/triage", body("x"))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent id must be a uuid") || s.touched() {
			t.Errorf("%s: %d %q", method, rec.Code, rec.Body)
		}
	}
}

// --- replace ---

func TestReplacingCannotChangeTheKind(t *testing.T) {
	t.Parallel()

	seeded := anAgent(uuid.New(), "router")
	seeded.Kind = "orchestrator"

	for _, kind := range []string{"custom", "chat", "Orchestrator"} {
		s := newFakeStore(seeded)
		b := body("router")
		b["kind"] = kind
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(seeded.TeamID), http.MethodPut, one(seeded.TeamID, seeded.ID), b)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kind cannot change: this agent is orchestrator") {
			t.Errorf("kind %q: %d %q", kind, rec.Code, rec.Body)
		}
		if s.txs != 0 {
			t.Errorf("kind %q reached the transaction", kind)
		}
	}

	// Left out, or the same: the seeded agent can be tuned and stays what it is.
	for _, kind := range []any{nil, "orchestrator"} {
		s := newFakeStore(seeded)
		b := body("router")
		if kind != nil {
			b["kind"] = kind
		}
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(seeded.TeamID), http.MethodPut, one(seeded.TeamID, seeded.ID), b)
		if rec.Code != http.StatusOK || decode(t, rec).Kind != "orchestrator" {
			t.Errorf("kind %v: %d (%s)", kind, rec.Code, rec.Body)
		}
	}
}

func TestReplaceSwapsTheGrantsWhole(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "triage")
	s := newFakeStore(a)
	oldServer, keptServer, newServer := s.server(a.TeamID), s.server(a.TeamID), s.server(a.TeamID)
	oldSkill, newSkill := s.skill(a.TeamID), s.skill(a.TeamID)
	s.servers[a.ID] = []db.AgentMcpServer{{AgentID: a.ID, McpServerID: oldServer, TeamID: a.TeamID}, {AgentID: a.ID, McpServerID: keptServer, TeamID: a.TeamID}}
	s.skills[a.ID] = []uuid.UUID{oldSkill}

	b := body("triage")
	b["mcp_servers"] = []map[string]any{{"id": keptServer, "allow": []string{"only_this"}}, {"id": newServer}}
	b["skills"] = []uuid.UUID{newSkill}

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), http.MethodPut, one(a.TeamID, a.ID), b)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}

	var ids []uuid.UUID
	for _, g := range s.servers[a.ID] {
		ids = append(ids, g.McpServerID)
		if g.McpServerID == keptServer && !slices.Equal(g.Allow, []string{"only_this"}) {
			t.Errorf("the kept server's allow is %v, want the new one", g.Allow)
		}
	}
	if !slices.Equal(sortedIDs(ids...), sortedIDs(keptServer, newServer)) {
		t.Errorf("servers now %v, want only the two sent", ids)
	}
	if !slices.Equal(s.skills[a.ID], []uuid.UUID{newSkill}) {
		t.Errorf("skills now %v, want only %s", s.skills[a.ID], newSkill)
	}
	if s.updated[0].ID != a.ID {
		t.Errorf("updated %s, want %s", s.updated[0].ID, a.ID)
	}
}

// A replace whose new grant is refused keeps everything as it was: the row
// edit, the cleared grants and the refused one roll back together.
func TestAReplaceWithARefusedGrantChangesNothing(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "triage")
	s := newFakeStore(a)
	kept := s.server(a.TeamID)
	s.servers[a.ID] = []db.AgentMcpServer{{AgentID: a.ID, McpServerID: kept, TeamID: a.TeamID}}

	b := body("renamed")
	b["mcp_servers"] = []map[string]any{{"id": s.server(uuid.New())}}

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), http.MethodPut, one(a.TeamID, a.ID), b)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d (%s)", rec.Code, rec.Body)
	}
	if s.agents[a.ID].Name != "triage" {
		t.Errorf("name is now %q", s.agents[a.ID].Name)
	}
	if len(s.servers[a.ID]) != 1 || s.servers[a.ID][0].McpServerID != kept {
		t.Errorf("grants are now %+v, want the old one", s.servers[a.ID])
	}
}

func TestABadReplaceBodyNeverReadsTheAgent(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "triage")
	s := newFakeStore(a)
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), http.MethodPut, one(a.TeamID, a.ID), map[string]any{"name": "x"})
	if rec.Code != http.StatusBadRequest || s.touched() {
		t.Errorf("%d, touched %v", rec.Code, s.touched())
	}
}

func TestReplacingAnAgentDeletedMeanwhileIsNotFound(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "triage")
	s := newFakeStore(a)
	s.updateErr = pgx.ErrNoRows
	if rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), http.MethodPut, one(a.TeamID, a.ID), body("triage")); rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
}

// --- delete ---

func TestDeleteRemovesTheAgent(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "triage")
	s := newFakeStore(a)
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), http.MethodDelete, one(a.TeamID, a.ID), nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || !slices.Equal(s.deleted, []uuid.UUID{a.ID}) {
		t.Errorf("%d %q, deleted %v", rec.Code, rec.Body, s.deleted)
	}
}

// --- list ---

func TestListAsksForOneMoreThanThePage(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newFakeStore()
	call(t, s, &fakeAuthz{}, memberOf(team), http.MethodGet, collection(team)+"?limit=10", nil)
	if len(s.listArgs) != 1 || s.listArgs[0].PageSize != 11 || s.listArgs[0].TeamID != team || s.listArgs[0].UseCursor {
		t.Errorf("asked %+v", s.listArgs)
	}
}

func TestTheCursorWalksEveryAgentOnceEvenOnTies(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var rows []db.Agent
	for i, n := range []string{"a", "b", "c", "d", "e"} {
		r := anAgent(team, n)
		r.CreatedAt = at
		if i == 4 {
			r.CreatedAt = at.Add(time.Second)
		}
		rows = append(rows, r)
	}
	s := newFakeStore(append(rows, anAgent(uuid.New(), "elsewhere"))...)

	seen := map[string]int{}
	next := ""
	for range 8 {
		rec := call(t, s, &fakeAuthz{}, memberOf(team), http.MethodGet, collection(team)+"?limit=2"+next, nil)
		var page api.Page[summary]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("page: %v (%s)", err, rec.Body)
		}
		for _, item := range page.Items {
			seen[item.Name]++
		}
		if page.NextCursor == nil {
			break
		}
		next = "&cursor=" + *page.NextCursor
	}
	if len(seen) != 5 {
		t.Errorf("paged %v, want a-e once each", seen)
	}
	for n, c := range seen {
		if c != 1 {
			t.Errorf("%s seen %d times", n, c)
		}
	}
}

func TestABadLimitOrCursorIsABadRequest(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	for _, q := range []string{"?limit=many", "?cursor=not-a-cursor"} {
		s := newFakeStore()
		rec := call(t, s, &fakeAuthz{}, memberOf(team), http.MethodGet, collection(team)+q, nil)
		if rec.Code != http.StatusBadRequest || s.touched() {
			t.Errorf("%s: %d, touched %v", q, rec.Code, s.touched())
		}
	}
}

func TestAnEmptyListIsAnEmptyArray(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	if rec := call(t, newFakeStore(), &fakeAuthz{}, memberOf(team), http.MethodGet, collection(team), nil); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s", rec.Body)
	}
}

// --- failures of ours ---

func TestAFailureOfOursIsA500ThatLeaksNothing(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		method  string
		path    func(db.Agent) string
		body    bool
		breakIt func(*fakeStore, db.Agent)
	}{
		"list":          {http.MethodGet, func(a db.Agent) string { return collection(a.TeamID) }, false, func(s *fakeStore, _ db.Agent) { s.listErr = errAny }},
		"get":           {http.MethodGet, func(a db.Agent) string { return one(a.TeamID, a.ID) }, false, func(s *fakeStore, _ db.Agent) { s.readErr = errAny }},
		"get's servers": {http.MethodGet, func(a db.Agent) string { return one(a.TeamID, a.ID) }, false, func(s *fakeStore, _ db.Agent) { s.serversErr = errAny }},
		"get's skills":  {http.MethodGet, func(a db.Agent) string { return one(a.TeamID, a.ID) }, false, func(s *fakeStore, _ db.Agent) { s.skillsErr = errAny }},
		"a stored schema that is not ours": {http.MethodGet, func(a db.Agent) string { return one(a.TeamID, a.ID) }, false, func(s *fakeStore, a db.Agent) {
			a.OutputSchema = []byte(`[1]`)
			s.agents[a.ID] = a
		}},
		"create":           {http.MethodPost, func(a db.Agent) string { return collection(a.TeamID) }, true, func(s *fakeStore, _ db.Agent) { s.createErr = errAny }},
		"replace's update": {http.MethodPut, func(a db.Agent) string { return one(a.TeamID, a.ID) }, true, func(s *fakeStore, _ db.Agent) { s.updateErr = errAny }},
		"replace's clear":  {http.MethodPut, func(a db.Agent) string { return one(a.TeamID, a.ID) }, true, func(s *fakeStore, _ db.Agent) { s.clearErr = errAny }},
		"replace's lookup": {http.MethodPut, func(a db.Agent) string { return one(a.TeamID, a.ID) }, true, func(s *fakeStore, _ db.Agent) { s.readErr = errAny }},
		"delete":           {http.MethodDelete, func(a db.Agent) string { return one(a.TeamID, a.ID) }, false, func(s *fakeStore, _ db.Agent) { s.deleteErr = errAny }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a := anAgent(uuid.New(), "triage")
			s := newFakeStore(a)
			tc.breakIt(s, a)
			var b any
			if tc.body {
				b = body("triage")
			}
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), tc.method, tc.path(a), b)
			if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "connection reset") || strings.Contains(rec.Body.String(), "failed") {
				t.Errorf("%d %q", rec.Code, rec.Body)
			}
		})
	}
}

// The caller is told which rule, not merely that one broke.
func TestARefusedBodySaysWhichRule(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	b := body("triage")
	b["max_steps"] = 0
	rec := call(t, newFakeStore(), &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), b)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_steps must be greater than 0") {
		t.Errorf("%d %q", rec.Code, rec.Body)
	}
}

// A replace carries the output schema and the parser model through to the
// row, as create does — and clears them when they are left out.
func TestReplaceWritesAndClearsTheOptionalParts(t *testing.T) {
	t.Parallel()

	a := anAgent(uuid.New(), "triage")
	s := newFakeStore(a)
	b := body("triage")
	b["output_schema"] = map[string]any{"name": "verdict", "json": map[string]any{"type": "object"}}
	b["parser"] = true
	b["parser_model"] = map[string]any{"name": "small", "max_tokens": 64}

	if rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), http.MethodPut, one(a.TeamID, a.ID), b); rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	p := s.updated[0]
	if p.OutputSchema == nil || !strings.Contains(string(p.OutputSchema), `"verdict"`) || !p.Parser ||
		p.ParserModelName == nil || *p.ParserModelName != "small" || p.ParserMaxTokens == nil || *p.ParserMaxTokens != 64 {
		t.Errorf("update sent %+v", p)
	}

	if rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(a.TeamID), http.MethodPut, one(a.TeamID, a.ID), body("triage")); rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body)
	}
	p = s.updated[1]
	if p.OutputSchema != nil || p.Parser || p.ParserModelName != nil || p.ParserMaxTokens != nil {
		t.Errorf("a replace without them sent %+v, want them cleared", p)
	}
}

// Created with no grants, the reply still says [] for both.
func TestACreatedAgentWithNoGrantsHasEmptyArrays(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rec := call(t, newFakeStore(), &fakeAuthz{allowed: true}, memberOf(team), http.MethodPost, collection(team), body("triage"))
	if !strings.Contains(rec.Body.String(), `"mcp_servers":[]`) || !strings.Contains(rec.Body.String(), `"skills":[]`) {
		t.Errorf("body = %s", rec.Body)
	}
}
