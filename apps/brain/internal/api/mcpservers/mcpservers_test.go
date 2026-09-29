package mcpservers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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

// --- fakes ---

// errAny stands in for any failure of ours. Which one never changes the
// answer: the caller is told nothing and the detail goes to the log.
var errAny = errors.New("connection reset by peer")

// fakeStore pages as the real query does — newest first, (created_at, id) as
// the tiebreak — so the cursor tests check the handler, not the fake.
type fakeStore struct {
	mu      sync.Mutex
	servers map[uuid.UUID]db.McpServer
	tools   map[uuid.UUID][]db.McpTool

	readErr, listErr, createErr, updateErr, deleteErr, toolsErr error

	reads      int
	created    []db.CreateMCPServerParams
	updated    []db.UpdateMCPServerParams
	deleted    []uuid.UUID
	listArgs   []db.ListMCPServersByTeamParams
	toolsAsked []uuid.UUID
}

func newFakeStore(existing ...db.McpServer) *fakeStore {
	s := &fakeStore{servers: map[uuid.UUID]db.McpServer{}, tools: map[uuid.UUID][]db.McpTool{}}
	for _, row := range existing {
		s.servers[row.ID] = row
	}
	return s
}

func (s *fakeStore) touched() bool {
	return s.reads != 0 || len(s.listArgs) != 0 || len(s.toolsAsked) != 0 || s.wrote()
}

func (s *fakeStore) wrote() bool {
	return len(s.created) != 0 || len(s.updated) != 0 || len(s.deleted) != 0
}

func (s *fakeStore) CreateMCPServer(_ context.Context, arg db.CreateMCPServerParams) (db.McpServer, error) {
	s.created = append(s.created, arg)
	if s.createErr != nil {
		return db.McpServer{}, s.createErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	row := db.McpServer{
		ID: uuid.New(), TeamID: arg.TeamID, Name: arg.Name, Url: arg.Url, Command: arg.Command, Env: arg.Env,
		AuthSecretRef: arg.AuthSecretRef, Bare: arg.Bare, CreatedAt: now, UpdatedAt: now,
	}
	s.servers[row.ID] = row
	return row, nil
}

func (s *fakeStore) GetMCPServer(_ context.Context, id uuid.UUID) (db.McpServer, error) {
	s.reads++
	if s.readErr != nil {
		return db.McpServer{}, s.readErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.servers[id]
	if !ok {
		return db.McpServer{}, pgx.ErrNoRows
	}
	return row, nil
}

func (s *fakeStore) ListMCPServersByTeam(_ context.Context, arg db.ListMCPServersByTeamParams) ([]db.McpServer, error) {
	s.listArgs = append(s.listArgs, arg)
	if s.listErr != nil {
		return nil, s.listErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []db.McpServer
	for _, row := range s.servers {
		if row.TeamID != arg.TeamID {
			continue
		}
		if arg.UseCursor && !before(row, arg.AfterCreatedAt, arg.AfterID) {
			continue
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b db.McpServer) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID.String(), a.ID.String())
	})
	return rows[:min(len(rows), int(arg.PageSize))], nil
}

// before is (created_at, id) < (at, id), as the query compares rows.
func before(row db.McpServer, at time.Time, id uuid.UUID) bool {
	if c := row.CreatedAt.Compare(at); c != 0 {
		return c < 0
	}
	return row.ID.String() < id.String()
}

func (s *fakeStore) UpdateMCPServer(_ context.Context, arg db.UpdateMCPServerParams) (db.McpServer, error) {
	s.updated = append(s.updated, arg)
	if s.updateErr != nil {
		return db.McpServer{}, s.updateErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.servers[arg.ID]
	if !ok {
		return db.McpServer{}, pgx.ErrNoRows
	}
	row.Name, row.Url, row.Command, row.Env = arg.Name, arg.Url, arg.Command, arg.Env
	row.AuthSecretRef, row.Bare, row.UpdatedAt = arg.AuthSecretRef, arg.Bare, time.Now()
	s.servers[arg.ID] = row
	return row, nil
}

func (s *fakeStore) DeleteMCPServer(_ context.Context, id uuid.UUID) error {
	s.deleted = append(s.deleted, id)
	if s.deleteErr != nil {
		return s.deleteErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.servers, id)
	return nil
}

func (s *fakeStore) ListMCPToolsByServer(_ context.Context, id uuid.UUID) ([]db.McpTool, error) {
	s.toolsAsked = append(s.toolsAsked, id)
	if s.toolsErr != nil {
		return nil, s.toolsErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tools[id], nil
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

// No route in this package is any_team, so reaching the strictly weaker check
// is itself the failure.
func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("an MCP server route used the strictly weaker CanInAnyTeam")
}

// serverRoutes is what rbac.json maps for this package. The real guard, not an
// open one, so a route whose scope changes fails here as well as in
// internal/store.
func serverRoutes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	add := func(method, path, scope string, permission *string) {
		t.Helper()
		if err := rs.Add(method, path, scope, permission); err != nil {
			t.Fatalf("Add %s %s: %v", method, path, err)
		}
	}

	write := "tool:write"
	add("GET", "/teams/{id}/mcp-servers", "member", nil)
	add("POST", "/teams/{id}/mcp-servers", "team", &write)
	add("GET", "/teams/{id}/mcp-servers/{serverID}", "member", nil)
	add("PUT", "/teams/{id}/mcp-servers/{serverID}", "team", &write)
	add("DELETE", "/teams/{id}/mcp-servers/{serverID}", "team", &write)
	add("GET", "/teams/{id}/mcp-servers/{serverID}/tools", "member", nil)
	return rs
}

func call(t *testing.T, s *fakeStore, a *fakeAuthz, u *auth.User, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), s).Register(mux, api.NewGuard(serverRoutes(t), a, discardLogger()))

	var in io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		in = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
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

func memberOf(teamID uuid.UUID) *auth.User {
	return &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: teamID, Name: "platform", Role: "member"}},
	}
}

func outsider() *auth.User {
	return &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"}
}

func pgCode(code string) error { return &pgconn.PgError{Code: code} }

func refIn(teamID uuid.UUID, name string) string {
	return "teams/" + teamID.String() + "/mcp/" + name + "#token"
}

func aURLServer(teamID uuid.UUID, name string) db.McpServer {
	u := "https://mcp.example.com/" + name
	ref := refIn(teamID, name)
	return db.McpServer{
		ID: uuid.New(), TeamID: teamID, Name: name, Url: &u, Env: []byte("{}"), AuthSecretRef: &ref,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func aCommandServer(teamID uuid.UUID, name string) db.McpServer {
	return db.McpServer{
		ID: uuid.New(), TeamID: teamID, Name: name, Command: []string{"npx", "-y", name},
		Env:       []byte(`{"TOKEN":"` + refIn(teamID, name) + `"}`),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

// urlBody is a valid request for a url server in the team.
func urlBody(teamID uuid.UUID, name string) map[string]any {
	return map[string]any{"name": name, "url": "https://mcp.example.com/" + name, "auth_secret_ref": refIn(teamID, name)}
}

func commandBody(teamID uuid.UUID, name string) map[string]any {
	return map[string]any{"name": name, "command": []string{"npx", "-y", name}, "env": map[string]string{"TOKEN": refIn(teamID, name)}}
}

func collection(teamID uuid.UUID) string { return "/teams/" + teamID.String() + "/mcp-servers" }

func one(teamID, id uuid.UUID) string { return collection(teamID) + "/" + id.String() }

func toolsOf(teamID, id uuid.UUID) string { return one(teamID, id) + "/tools" }

func decodeServer(t *testing.T, rec *httptest.ResponseRecorder) server {
	t.Helper()

	var got server
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v (%s)", err, rec.Body)
	}
	return got
}

// --- authorisation ---

// Every write asks the guard for tool:write and nothing else, and a refusal
// never reaches the store.
func TestWritesNeedToolWrite(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		method string
		path   func(db.McpServer) string
		body   func(db.McpServer) any
		ok     int
	}{
		"create": {http.MethodPost, func(r db.McpServer) string { return collection(r.TeamID) }, func(r db.McpServer) any { return urlBody(r.TeamID, "linear") }, http.StatusCreated},
		"update": {http.MethodPut, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, func(r db.McpServer) any { return urlBody(r.TeamID, r.Name) }, http.StatusOK},
		"delete": {http.MethodDelete, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, func(db.McpServer) any { return nil }, http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, allowed := range []bool{true, false} {
				row := aURLServer(uuid.New(), "github")
				s := newFakeStore(row)
				a := &fakeAuthz{allowed: allowed}
				rec := call(t, s, a, memberOf(row.TeamID), tc.method, tc.path(row), tc.body(row))

				want := tc.ok
				if !allowed {
					want = http.StatusForbidden
				}
				if rec.Code != want {
					t.Errorf("allowed=%v: status %d, want %d (%s)", allowed, rec.Code, want, rec.Body)
				}
				if len(a.asked) != 1 || a.asked[0] != authz.Action("tool:write") {
					t.Errorf("the guard asked for %v, want tool:write", a.asked)
				}
				if !allowed && s.touched() {
					t.Error("a refused write reached the store")
				}
			}
		})
	}
}

// Reading is member-scoped: belonging is the whole check, so no permission is
// asked for, and an outsider gets 404 — a 403 would confirm the team exists.
func TestReadsAreForMembersAndHiddenFromOutsiders(t *testing.T) {
	t.Parallel()

	for name, path := range map[string]func(db.McpServer) string{
		"list":  func(r db.McpServer) string { return collection(r.TeamID) },
		"get":   func(r db.McpServer) string { return one(r.TeamID, r.ID) },
		"tools": func(r db.McpServer) string { return toolsOf(r.TeamID, r.ID) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			row := aURLServer(uuid.New(), "github")
			a := &fakeAuthz{}
			if rec := call(t, newFakeStore(row), a, memberOf(row.TeamID), http.MethodGet, path(row), nil); rec.Code != http.StatusOK {
				t.Errorf("as a member: %d, want 200 (%s)", rec.Code, rec.Body)
			}
			if len(a.asked) != 0 {
				t.Errorf("a read asked for %v; belonging should be the whole check", a.asked)
			}

			s := newFakeStore(row)
			if rec := call(t, s, a, outsider(), http.MethodGet, path(row), nil); rec.Code != http.StatusNotFound {
				t.Errorf("as an outsider: %d, want 404", rec.Code)
			}
			if s.touched() {
				t.Error("an outsider's read reached the store")
			}
		})
	}
}

// Without the middleware there is no user on the context. That is a wiring
// bug, so it fails loudly rather than serving an anonymous caller.
func TestEveryRouteRefusesARequestWithNoUser(t *testing.T) {
	t.Parallel()

	teamID, id := uuid.New(), uuid.New()
	for name, tc := range map[string]struct{ method, path string }{
		"list":   {http.MethodGet, collection(teamID)},
		"create": {http.MethodPost, collection(teamID)},
		"get":    {http.MethodGet, one(teamID, id)},
		"update": {http.MethodPut, one(teamID, id)},
		"delete": {http.MethodDelete, one(teamID, id)},
		"tools":  {http.MethodGet, toolsOf(teamID, id)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newFakeStore()
			rec := call(t, s, &fakeAuthz{allowed: true}, nil, tc.method, tc.path, urlBody(teamID, "github"))

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if s.touched() {
				t.Error("the handler reached the store without a user")
			}
		})
	}
}

// The verbs are part of the contract. Tools are written by discovery, never
// by a caller, so the tools route answers GET and nothing else.
func TestUndeclaredMethodsDoNotAnswer(t *testing.T) {
	t.Parallel()

	teamID, id := uuid.New(), uuid.New()
	for name, tc := range map[string]struct{ method, path string }{
		"PUT on the collection":    {http.MethodPut, collection(teamID)},
		"DELETE on the collection": {http.MethodDelete, collection(teamID)},
		"POST on one server":       {http.MethodPost, one(teamID, id)},
		"PATCH on one server":      {http.MethodPatch, one(teamID, id)},
		"POST on its tools":        {http.MethodPost, toolsOf(teamID, id)},
		"PUT on its tools":         {http.MethodPut, toolsOf(teamID, id)},
		"DELETE on its tools":      {http.MethodDelete, toolsOf(teamID, id)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newFakeStore()
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), tc.method, tc.path, nil)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if s.touched() {
				t.Error("an undeclared method reached the store")
			}
		})
	}
}

// --- the wire contract ---

func keysOf(t *testing.T, raw []byte) map[string]bool {
	t.Helper()

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("body is not an object: %v (%s)", err, raw)
	}
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	return keys
}

func wantKeys(t *testing.T, what string, got map[string]bool, want ...string) {
	t.Helper()

	for _, k := range want {
		if !got[k] {
			t.Errorf("%s: field %q is missing", what, k)
		}
		delete(got, k)
	}
	for k := range got {
		t.Errorf("%s: unexpected field %q", what, k)
	}
}

var commonKeys = []string{"id", "team_id", "name", "env", "bare", "discovered_at", "created_at", "updated_at"}

// The wire shape is a contract. A server shows its own transport's fields and
// never the other's, and discovered_at is present as null until discovery
// has run, so "never discovered" is a value rather than an absence.
func TestOnlyContractedFieldsAreSerialised(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	byURL, byCommand := aURLServer(teamID, "github"), aCommandServer(teamID, "fs")
	s := newFakeStore(byURL, byCommand)
	s.tools[byURL.ID] = []db.McpTool{{Name: "search", Description: "Search", InputSchema: []byte(`{"type":"object"}`)}}

	rec := call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, one(teamID, byURL.ID), nil)
	wantKeys(t, "url server", keysOf(t, rec.Body.Bytes()), append(slices.Clone(commonKeys), "url", "auth_secret_ref")...)
	if !strings.Contains(rec.Body.String(), `"discovered_at":null`) {
		t.Errorf("discovered_at is not null before discovery: %s", rec.Body)
	}

	rec = call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, one(teamID, byCommand.ID), nil)
	wantKeys(t, "command server", keysOf(t, rec.Body.Bytes()), append(slices.Clone(commonKeys), "command")...)

	rec = call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID), nil)
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 {
		t.Fatalf("list body is not a page of two: %v (%s)", err, rec.Body)
	}

	rec = call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, toolsOf(teamID, byURL.ID), nil)
	wantKeys(t, "tool list", keysOf(t, rec.Body.Bytes()), "items")
	var tools struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tools); err != nil || len(tools.Items) != 1 {
		t.Fatalf("tools: %v (%s)", err, rec.Body)
	}
	wantKeys(t, "tool", keysOf(t, tools.Items[0]), "name", "description", "input_schema")
}

// A url server's env is {} rather than null or absent: the field is always an
// object, so a client never has to special-case it.
func TestEnvIsAnObjectEvenWhenEmpty(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	rec := call(t, newFakeStore(row), &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, one(row.TeamID, row.ID), nil)

	if !strings.Contains(rec.Body.String(), `"env":{}`) {
		t.Errorf("env = %s, want {}", rec.Body)
	}
}

// --- create ---

func TestCreateStoresWhatWasSentInTheTeamOfThePath(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]func(uuid.UUID) map[string]any{
		"url":     func(id uuid.UUID) map[string]any { b := urlBody(id, "github"); b["bare"] = true; return b },
		"command": func(id uuid.UUID) map[string]any { return commandBody(id, "fs") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			teamID := uuid.New()
			s := newFakeStore()
			sentBody := body(teamID)
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, collection(teamID), sentBody)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
			}
			if len(s.created) != 1 {
				t.Fatalf("created %d servers, want 1", len(s.created))
			}
			got := s.created[0]
			if got.TeamID != teamID || got.Name != sentBody["name"] {
				t.Errorf("created %+v in the wrong team or name", got)
			}

			out := decodeServer(t, rec)
			if out.TeamID != teamID || out.Name != sentBody["name"] || out.ID == uuid.Nil {
				t.Errorf("response = %+v", out)
			}
			switch name {
			case "url":
				if got.Url == nil || *got.Url != sentBody["url"] || got.Command != nil ||
					got.AuthSecretRef == nil || *got.AuthSecretRef != sentBody["auth_secret_ref"] || !got.Bare {
					t.Errorf("created %+v", got)
				}
				if string(got.Env) != "{}" {
					t.Errorf("env = %s, want {}", got.Env)
				}
			case "command":
				if got.Url != nil || got.AuthSecretRef != nil || !slices.Equal(got.Command, []string{"npx", "-y", "fs"}) {
					t.Errorf("created %+v", got)
				}
				if want := `{"TOKEN":"` + refIn(teamID, "fs") + `"}`; string(got.Env) != want {
					t.Errorf("env = %s, want %s", got.Env, want)
				}
				if out.Env["TOKEN"] != refIn(teamID, "fs") {
					t.Errorf("response env = %v", out.Env)
				}
			}
		})
	}
}

// Every refusal is the caller's mistake, answered before anything is written.
// The body says which rule, and never repeats the secret that broke it.
func TestARefusedBodyNeverReachesTheStore(t *testing.T) {
	t.Parallel()

	const literal = "ghp_abcdef0123456789"
	for name, tc := range map[string]struct {
		body func(uuid.UUID) any
		want string
	}{
		"not json":         {func(uuid.UUID) any { return "{" }, "invalid request body"},
		"two objects":      {func(uuid.UUID) any { return `{"name":"a"}{"name":"b"}` }, "single JSON object"},
		"an unknown field": {func(id uuid.UUID) any { b := urlBody(id, "x"); b["token"] = literal; return b }, "invalid request body"},
		"both transports":  {func(id uuid.UUID) any { b := urlBody(id, "x"); b["command"] = []string{"npx"}; return b }, "not both"},
		"neither":          {func(uuid.UUID) any { return map[string]any{"name": "x"} }, "needs a url or a command"},
		"a literal secret": {func(uuid.UUID) any {
			return map[string]any{"name": "x", "command": []string{"npx"}, "env": map[string]string{"GITHUB_TOKEN": literal}}
		}, "env GITHUB_TOKEN is a secret reference"},
		"another team's secret": {func(uuid.UUID) any { return urlBody(uuid.New(), "x") }, "auth_secret_ref is a secret reference"},
		"a bad name":            {func(id uuid.UUID) any { return urlBody(id, "a.b") }, "name must be"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			teamID := uuid.New()
			s := newFakeStore()
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, collection(teamID), tc.body(teamID))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body = %q, want it to say %q", rec.Body, tc.want)
			}
			if strings.Contains(rec.Body.String(), literal) {
				t.Errorf("the refusal repeats the secret: %s", rec.Body)
			}
			if s.touched() {
				t.Error("a refused body reached the store")
			}
		})
	}
}

func TestADuplicateNameIsAConflict(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	for name, tc := range map[string]struct{ method, path string }{
		"create": {http.MethodPost, collection(row.TeamID)},
		"update": {http.MethodPut, one(row.TeamID, row.ID)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newFakeStore(row)
			s.createErr, s.updateErr = pgCode(codeUniqueViolation), pgCode(codeUniqueViolation)
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(row.TeamID), tc.method, tc.path, urlBody(row.TeamID, "GitHub"))

			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already exists") {
				t.Errorf("status = %d (%s), want 409 naming the clash", rec.Code, rec.Body)
			}
		})
	}
}

// --- one server ---

func TestGetDescribesTheServer(t *testing.T) {
	t.Parallel()

	row := aCommandServer(uuid.New(), "fs")
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	row.DiscoveredAt, row.Bare = &at, true
	rec := call(t, newFakeStore(row), &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, one(row.TeamID, row.ID), nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	got := decodeServer(t, rec)
	if got.ID != row.ID || got.Name != "fs" || !slices.Equal(got.Command, row.Command) || !got.Bare ||
		got.DiscoveredAt == nil || !got.DiscoveredAt.Equal(at) || got.Env["TOKEN"] != refIn(row.TeamID, "fs") {
		t.Errorf("got %+v", got)
	}
}

// A server id from another team is answered exactly as one that does not
// exist, and nothing is written, whatever the verb.
func TestAnotherTeamsServerIsNotFound(t *testing.T) {
	t.Parallel()

	theirs := aURLServer(uuid.New(), "github")
	mine := uuid.New()
	for name, tc := range map[string]struct {
		method, path string
		body         any
	}{
		"get":    {http.MethodGet, one(mine, theirs.ID), nil},
		"update": {http.MethodPut, one(mine, theirs.ID), urlBody(mine, "github")},
		"delete": {http.MethodDelete, one(mine, theirs.ID), nil},
		"tools":  {http.MethodGet, toolsOf(mine, theirs.ID), nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newFakeStore(theirs)
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(mine), tc.method, tc.path, tc.body)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body)
			}
			if s.wrote() || len(s.toolsAsked) != 0 {
				t.Error("another team's server was acted on")
			}
		})
	}
}

func TestAMissingServerIsNotFound(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	rec := call(t, newFakeStore(), &fakeAuthz{}, memberOf(teamID), http.MethodGet, one(teamID, uuid.New()), nil)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestAServerIDThatIsNotAUUIDIsABadRequest(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		s := newFakeStore()
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), method, collection(teamID)+"/github", urlBody(teamID, "github"))

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must be a uuid") {
			t.Errorf("%s: status = %d (%s), want 400", method, rec.Code, rec.Body)
		}
		if s.touched() {
			t.Errorf("%s: a bad id reached the store", method)
		}
	}
}

// --- update ---

// An update replaces every field, so switching a url server to a command one
// leaves no url or auth reference behind.
func TestUpdateReplacesEveryField(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPut, one(row.TeamID, row.ID), commandBody(row.TeamID, "fs"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if len(s.updated) != 1 {
		t.Fatalf("updated %d times, want 1", len(s.updated))
	}
	got := s.updated[0]
	if got.ID != row.ID || got.Name != "fs" || got.Url != nil || got.AuthSecretRef != nil ||
		!slices.Equal(got.Command, []string{"npx", "-y", "fs"}) || got.Bare {
		t.Errorf("update sent %+v", got)
	}
	out := decodeServer(t, rec)
	if out.ID != row.ID || out.URL != nil || out.Name != "fs" {
		t.Errorf("response = %+v", out)
	}
}

// The body is checked before the server is looked up: a bad request costs no
// query, and is answered the same whether or not the server exists.
func TestABadUpdateBodyNeverReadsTheServer(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPut, one(row.TeamID, row.ID), map[string]any{"name": "x"})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if s.touched() {
		t.Error("a bad body reached the store")
	}
}

func TestUpdatingAServerDeletedMeanwhileIsNotFound(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	s.updateErr = pgx.ErrNoRows
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPut, one(row.TeamID, row.ID), urlBody(row.TeamID, "github"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body)
	}
}

// --- delete ---

func TestDeleteRemovesTheServer(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodDelete, one(row.TeamID, row.ID), nil)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("status = %d (%s), want an empty 204", rec.Code, rec.Body)
	}
	if !slices.Equal(s.deleted, []uuid.UUID{row.ID}) {
		t.Errorf("deleted %v, want %s", s.deleted, row.ID)
	}
	if _, still := s.servers[row.ID]; still {
		t.Error("the server is still there")
	}
}

// agent_mcp_servers refers to the server ON DELETE RESTRICT, so the database
// refuses and nothing is gone. The caller is told why, not handed a 500.
func TestDeletingAGrantedServerIsAConflictThatDeletedNothing(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	s.deleteErr = pgCode(codeRestrictViolation)
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodDelete, one(row.TeamID, row.ID), nil)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "granted to an agent") {
		t.Errorf("status = %d (%s), want 409 naming the grant", rec.Code, rec.Body)
	}
	if _, still := s.servers[row.ID]; !still {
		t.Error("the server was deleted anyway")
	}
}

// --- tools ---

// The tools are the ones discovery stored for this server, in the store's
// order, with each input schema passed through byte for byte.
func TestToolsListsWhatWasDiscovered(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	row, sibling := aURLServer(teamID, "github"), aURLServer(teamID, "linear")
	s := newFakeStore(row, sibling)
	schema := `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`
	s.tools[row.ID] = []db.McpTool{
		{McpServerID: row.ID, Name: "create_issue", Description: "Open an issue", InputSchema: []byte(`{"type":"object"}`)},
		{McpServerID: row.ID, Name: "search", Description: "Search code", InputSchema: []byte(schema)},
	}
	s.tools[sibling.ID] = []db.McpTool{{McpServerID: sibling.ID, Name: "not_mine", InputSchema: []byte(`{}`)}}

	rec := call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, toolsOf(teamID, row.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if !slices.Equal(s.toolsAsked, []uuid.UUID{row.ID}) {
		t.Errorf("asked for the tools of %v, want %s", s.toolsAsked, row.ID)
	}

	var got toolList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v (%s)", err, rec.Body)
	}
	if len(got.Items) != 2 || got.Items[0].Name != "create_issue" || got.Items[1].Name != "search" ||
		got.Items[1].Description != "Search code" {
		t.Fatalf("tools = %+v", got.Items)
	}
	if string(got.Items[1].InputSchema) != schema {
		t.Errorf("input_schema = %s, want %s", got.Items[1].InputSchema, schema)
	}
}

// A server not yet discovered lists [] rather than null: "we looked, there
// are none".
func TestAServerWithNoToolsListsAnEmptyArray(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	rec := call(t, newFakeStore(row), &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, toolsOf(row.TeamID, row.ID), nil)

	if rec.Body.String() != "{\"items\":[]}\n" {
		t.Errorf("body = %q, want items to be []", rec.Body)
	}
}

// --- list ---

func TestListAsksForOneMoreThanThePage(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newFakeStore()
	call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?limit=10", nil)

	if len(s.listArgs) != 1 || s.listArgs[0].PageSize != 11 || s.listArgs[0].TeamID != teamID || s.listArgs[0].UseCursor {
		t.Errorf("ListMCPServersByTeam got %+v, want team %s, 11 rows, no cursor", s.listArgs, teamID)
	}
}

// A full page carries a cursor, and handing it back continues after the last
// row shown — every server once, none twice, and another team's never.
func TestTheNextCursorWalksEveryServerOnce(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	var rows []db.McpServer
	for i, name := range []string{"a", "b", "c"} {
		row := aURLServer(teamID, name)
		row.CreatedAt = time.Date(2026, 9, 1, 0, 0, i, 0, time.UTC)
		rows = append(rows, row)
	}
	s := newFakeStore(append(rows, aURLServer(uuid.New(), "elsewhere"))...)

	var seen []string
	next := ""
	for range 5 {
		rec := call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?limit=1"+next, nil)
		var page api.Page[server]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body: %v (%s)", err, rec.Body)
		}
		for _, item := range page.Items {
			seen = append(seen, item.Name)
		}
		if page.NextCursor == nil {
			break
		}
		next = "&cursor=" + *page.NextCursor
	}

	if want := []string{"c", "b", "a"}; !slices.Equal(seen, want) {
		t.Errorf("paged %v, want %v newest first", seen, want)
	}
}

// Servers created in one transaction share now(), so the id is what orders
// them. A cursor carrying the wrong id skips or repeats rows exactly there.
func TestTheCursorBreaksTiesOnTheID(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var rows []db.McpServer
	for _, name := range []string{"a", "b", "c", "d"} {
		row := aURLServer(teamID, name)
		row.CreatedAt = at
		rows = append(rows, row)
	}
	s := newFakeStore(rows...)

	seen := map[string]int{}
	next := ""
	for range 6 {
		rec := call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?limit=1"+next, nil)
		var page api.Page[server]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body: %v (%s)", err, rec.Body)
		}
		for _, item := range page.Items {
			seen[item.Name]++
		}
		if page.NextCursor == nil {
			break
		}
		next = "&cursor=" + *page.NextCursor
	}

	if len(seen) != 4 || seen["a"] != 1 || seen["b"] != 1 || seen["c"] != 1 || seen["d"] != 1 {
		t.Errorf("paged %v, want each of a-d exactly once", seen)
	}
}

func TestABadLimitIsABadRequest(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newFakeStore()
	rec := call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?limit=many", nil)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if s.touched() {
		t.Error("a bad limit reached the store")
	}
}

// A mangled cursor restarting from the top would turn a client's paging loop
// into an infinite one.
func TestAMangledCursorIsABadRequest(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newFakeStore()
	rec := call(t, s, &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?cursor=not-a-cursor", nil)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if s.touched() {
		t.Error("a mangled cursor reached the store")
	}
}

func TestAnEmptyListIsAnEmptyArray(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	rec := call(t, newFakeStore(), &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID), nil)

	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty list = %s, want items to be []", rec.Body)
	}
}

// --- failures of ours ---

// A failure of ours is a 500 that says nothing: the detail goes to the log,
// and the database's own words never reach the caller. A row whose env is not
// an object of strings was written around the API, and is one of ours too.
func TestAFailureOfOursIsA500ThatLeaksNothing(t *testing.T) {
	t.Parallel()

	corrupt := func(s *fakeStore) {
		for id, row := range s.servers {
			row.Env = []byte(`{"TOKEN":1}`)
			s.servers[id] = row
		}
	}
	for name, tc := range map[string]struct {
		method  string
		path    func(db.McpServer) string
		body    bool
		breakIt func(*fakeStore)
	}{
		"list":               {http.MethodGet, func(r db.McpServer) string { return collection(r.TeamID) }, false, func(s *fakeStore) { s.listErr = errAny }},
		"list's env":         {http.MethodGet, func(r db.McpServer) string { return collection(r.TeamID) }, false, corrupt},
		"get":                {http.MethodGet, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, false, func(s *fakeStore) { s.readErr = errAny }},
		"get's env":          {http.MethodGet, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, false, corrupt},
		"create":             {http.MethodPost, func(r db.McpServer) string { return collection(r.TeamID) }, true, func(s *fakeStore) { s.createErr = errAny }},
		"update's lookup":    {http.MethodPut, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, true, func(s *fakeStore) { s.readErr = errAny }},
		"update":             {http.MethodPut, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, true, func(s *fakeStore) { s.updateErr = errAny }},
		"delete":             {http.MethodDelete, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, false, func(s *fakeStore) { s.deleteErr = errAny }},
		"tools":              {http.MethodGet, func(r db.McpServer) string { return toolsOf(r.TeamID, r.ID) }, false, func(s *fakeStore) { s.toolsErr = errAny }},
		"another constraint": {http.MethodDelete, func(r db.McpServer) string { return one(r.TeamID, r.ID) }, false, func(s *fakeStore) { s.deleteErr = pgCode("23503") }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			row := aURLServer(uuid.New(), "github")
			s := newFakeStore(row)
			tc.breakIt(s)
			var body any
			if tc.body {
				body = urlBody(row.TeamID, "github")
			}

			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(row.TeamID), tc.method, tc.path(row), body)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 (%s)", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "connection reset") || strings.Contains(rec.Body.String(), "failed") {
				t.Errorf("the reply leaked the failure: %s", rec.Body)
			}
		})
	}
}
