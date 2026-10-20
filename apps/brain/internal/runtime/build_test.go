package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// --- fakes ---

type fakeStore struct {
	mu         sync.Mutex
	agents     map[uuid.UUID]db.Agent
	agentErr   error
	grants     []db.ListGrantedMCPServersRow
	grantsErr  error
	grantsRead int
	skills     []db.Skill
	skillsErr  error
	files      map[uuid.UUID][]db.SkillFile
}

func (f *fakeStore) GetAgent(_ context.Context, id uuid.UUID) (db.Agent, error) {
	if f.agentErr != nil {
		return db.Agent{}, f.agentErr
	}
	a, ok := f.agents[id]
	if !ok {
		return db.Agent{}, pgx.ErrNoRows
	}
	return a, nil
}

func (f *fakeStore) ListGrantedMCPServers(context.Context, uuid.UUID) ([]db.ListGrantedMCPServersRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grantsRead++
	return f.grants, f.grantsErr
}

func (f *fakeStore) ListGrantedSkills(context.Context, uuid.UUID) ([]db.Skill, error) {
	return f.skills, f.skillsErr
}

func (f *fakeStore) ListSkillFiles(_ context.Context, skillID uuid.UUID) ([]db.SkillFile, error) {
	return f.files[skillID], nil
}

type fakeObjects struct {
	mu     sync.Mutex
	data   map[string]string
	err    error
	gotten []string
}

func (f *fakeObjects) Get(_ context.Context, teamID uuid.UUID, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotten = append(f.gotten, teamID.String()+"/"+key)
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.data[key]), nil
}

func (f *fakeObjects) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.gotten)
}

// fakeClients hands every server the same client unless told otherwise, and
// records which servers it was asked about.
type fakeClients struct {
	mu     sync.Mutex
	client *http.Client
	err    error
	asked  []string
}

func (f *fakeClients) Client(_ context.Context, s db.McpServer) (*http.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, s.Name)
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

// deadlines records the deadline of every request the run's client sends, so
// a test can see what bound a tool call without waiting five minutes for it.
type deadlines struct {
	mu   sync.Mutex
	seen []time.Time
}

func (d *deadlines) RoundTrip(r *http.Request) (*http.Response, error) {
	if dl, ok := r.Context().Deadline(); ok && r.Method == http.MethodPost {
		d.mu.Lock()
		d.seen = append(d.seen, dl)
		d.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (d *deadlines) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = nil
}

func (d *deadlines) all() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.seen)
}

type noSession struct{ agent.Session }

// --- an MCP server ---

// mcpServer is a real go-sdk server over streamable HTTP. It counts the
// DELETEs that end a session, which is how a test sees Close happen.
type mcpServer struct {
	url     string
	deletes atomic.Int32
}

func serve(t *testing.T, tools ...string) *mcpServer {
	t.Helper()

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "v1"}, nil)
	for _, name := range tools {
		server.AddTool(&sdk.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: name + " ran"}}}, nil
			})
	}

	s := &mcpServer{}
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			s.deletes.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	// A session left open holds a stream, and Close alone would wait on it
	// forever; a leak should fail the test that made it, not hang the package.
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	})
	s.url = httpServer.URL
	return s
}

// unreachable is the address of a server that has gone away.
func unreachable(t *testing.T) string {
	t.Helper()

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	return gone.URL
}

func (s *mcpServer) waitClosed(t *testing.T, want int32) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for s.deletes.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("the server saw %d sessions closed, want %d", s.deletes.Load(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- fixtures ---

type fixture struct {
	store   *fakeStore
	objects *fakeObjects
	clients *fakeClients
	builder *Builder
	agent   db.Agent
	run     db.Run
}

func newFixture() *fixture {
	teamID := uuid.New()
	a := db.Agent{
		ID: uuid.New(), TeamID: teamID, Name: "researcher",
		ModelName: "claude-sonnet", MaxTokens: 4096, Pattern: "react", MaxSteps: 20,
	}
	f := &fixture{
		store:   &fakeStore{agents: map[uuid.UUID]db.Agent{a.ID: a}, files: map[uuid.UUID][]db.SkillFile{}},
		objects: &fakeObjects{data: map[string]string{}},
		clients: &fakeClients{client: &http.Client{}},
		agent:   a,
		run:     db.Run{ID: uuid.New(), TeamID: teamID, AgentID: &a.ID, AgentName: a.Name},
	}
	f.builder = NewBuilder(f.store, f.objects, f.clients)
	return f
}

func urlServer(name, url string, bare bool, allow ...string) db.ListGrantedMCPServersRow {
	row := db.ListGrantedMCPServersRow{McpServer: db.McpServer{ID: uuid.New(), Name: name, Url: &url, Bare: bare}}
	if len(allow) > 0 {
		row.Allow = allow
	}
	return row
}

func commandServer(name string) db.ListGrantedMCPServersRow {
	return db.ListGrantedMCPServersRow{McpServer: db.McpServer{ID: uuid.New(), Name: name, Command: []string{"npx", name}}}
}

func (f *fixture) build(t *testing.T) Built {
	t.Helper()

	built, err := f.builder.Build(t.Context(), f.run, noSession{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)
	return built
}

// buildErr is Build for a test that expects it to fail. A build that succeeds
// anyway is closed, so its sessions do not outlive the test.
func (f *fixture) buildErr(t *testing.T) error {
	t.Helper()

	built, err := f.builder.Build(t.Context(), f.run, noSession{})
	if err == nil {
		built.Close()
	}
	return err
}

func toolNames(built Built) []string {
	out := make([]string, 0, len(built.Spec.Tools))
	for _, tl := range built.Spec.Tools {
		out = append(out, tl.Name)
	}
	slices.Sort(out)
	return out
}

func tool(t *testing.T, built Built, name string) agent.Tool {
	t.Helper()

	for _, tl := range built.Spec.Tools {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %q; the spec has %v", name, toolNames(built))
	return agent.Tool{}
}

// --- the spec ---

func TestAnAgentRowBecomesTheSpecItDescribes(t *testing.T) {
	temperature, parserTemperature := 0.2, 0.0
	parserModel := "claude-haiku"
	parserMaxTokens := int32(512)
	schema := []byte(`{"type":"object","properties":{"answer":{"type":"string"}}}`)

	cases := []struct {
		name string
		edit func(*db.Agent)
		want agent.Spec
	}{
		{
			name: "every field set",
			edit: func(a *db.Agent) {
				a.Instructions = "Answer from the sources."
				a.Temperature = &temperature
				a.Pattern = "plan"
				a.MaxSteps = 12
				a.SteerContinuations = 3
				a.OutputSchema = schema
				a.Parser = true
				a.ParserModelName = &parserModel
				a.ParserMaxTokens = &parserMaxTokens
				a.ParserTemperature = &parserTemperature
			},
			want: agent.Spec{
				Model:              agent.ModelRef{Name: "claude-sonnet", MaxTokens: 4096, Temperature: &temperature},
				Pattern:            agent.PatternPlan,
				System:             agent.System("Answer from the sources."),
				MaxSteps:           12,
				SteerContinuations: 3,
				OutputSchema:       &agent.OutputSchema{Name: "output", JSON: schema},
				Parser:             true,
				ParserModel:        agent.ModelRef{Name: "claude-haiku", MaxTokens: 512, Temperature: &parserTemperature},
			},
		},
		{
			name: "nothing optional set",
			edit: func(*db.Agent) {},
			want: agent.Spec{
				Model:    agent.ModelRef{Name: "claude-sonnet", MaxTokens: 4096},
				Pattern:  agent.PatternReAct,
				MaxSteps: 20,
			},
		},
		{
			name: "a parser model with no budget of its own takes the agent's",
			edit: func(a *db.Agent) { a.ParserModelName = &parserModel },
			want: agent.Spec{
				Model:       agent.ModelRef{Name: "claude-sonnet", MaxTokens: 4096},
				Pattern:     agent.PatternReAct,
				MaxSteps:    20,
				ParserModel: agent.ModelRef{Name: "claude-haiku", MaxTokens: 4096},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			a := f.agent
			tc.edit(&a)
			f.store.agents[a.ID] = a

			session := &noSession{}
			built, err := f.builder.Build(t.Context(), f.run, session)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if built.Spec.Session != session {
				t.Errorf("the spec's session is %v, want the one passed in", built.Spec.Session)
			}

			got := built.Spec
			got.Tools, got.Skills, got.Session = nil, nil, nil
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("spec\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// --- the agent ---

func TestARunWhoseAgentIsGoneIsRefusedNamingIt(t *testing.T) {
	cases := map[string]func(*fixture){
		"the run lost its agent id": func(f *fixture) { f.run.AgentID = nil },
		"the agent row is gone":     func(f *fixture) { delete(f.store.agents, f.agent.ID) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			edit(f)

			err := f.buildErr(t)
			if err == nil || !strings.Contains(err.Error(), `the agent "researcher" was deleted`) {
				t.Fatalf("Build = %v, want the agent named as deleted", err)
			}
			if f.store.grantsRead != 0 {
				t.Errorf("the grants were read %d times for an agent that is gone", f.store.grantsRead)
			}
		})
	}
}

// A database that is down is not an agent that was deleted: the run would be
// failed with a reason that sends somebody looking for the wrong thing.
func TestAnAgentThatCannotBeReadIsNotCalledDeleted(t *testing.T) {
	f := newFixture()
	down := errors.New("connection refused")
	f.store.agentErr = down

	err := f.buildErr(t)
	if !errors.Is(err, down) {
		t.Fatalf("Build = %v, want it to wrap %v", err, down)
	}
	if strings.Contains(err.Error(), "deleted") {
		t.Errorf("Build = %v, which calls a read failure a deletion", err)
	}
}

// --- tools ---

func TestAnAllowListBindsOnlyTheToolsItNames(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("github", serve(t, "create_issue", "delete_repo", "search").url, false, "create_issue", "search"),
		urlServer("web", serve(t, "fetch", "crawl").url, true, "fetch"),
		urlServer("docs", serve(t, "read", "write").url, false),
	}

	built := f.build(t)

	want := []string{"docs__read", "docs__write", "fetch", "github__create_issue", "github__search"}
	if got := toolNames(built); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

// The allow-list holds the name the server gave. Only a prefixed server's
// tools have a prefix to take off; a bare server's name is its own, even when
// it happens to start with the server's.
func TestAnAllowListNamesToolsAsTheServerDoes(t *testing.T) {
	cases := []struct {
		name  string
		grant func(url string) db.ListGrantedMCPServersRow
		tool  string
		want  []string
	}{
		{
			name:  "a prefixed name on a prefixed server",
			grant: func(url string) db.ListGrantedMCPServersRow { return urlServer("github", url, false, "github__search") },
			tool:  "search",
			want:  []string{},
		},
		{
			name:  "a bare server's tool that looks prefixed, allowed by its tail",
			grant: func(url string) db.ListGrantedMCPServersRow { return urlServer("web", url, true, "fetch") },
			tool:  "web__fetch",
			want:  []string{},
		},
		{
			name:  "a bare server's tool that looks prefixed, allowed by its name",
			grant: func(url string) db.ListGrantedMCPServersRow { return urlServer("web", url, true, "web__fetch") },
			tool:  "web__fetch",
			want:  []string{"web__fetch"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			f.store.grants = []db.ListGrantedMCPServersRow{tc.grant(serve(t, tc.tool).url)}

			if got := toolNames(f.build(t)); !slices.Equal(got, tc.want) {
				t.Errorf("tools = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestToolsAreThoseGrantedWhenTheRunStarts(t *testing.T) {
	f := newFixture()
	server := serve(t, "search", "fetch")
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", server.url, true, "search")}
	if got := toolNames(f.build(t)); !slices.Equal(got, []string{"search"}) {
		t.Fatalf("first build: tools = %v, want [search]", got)
	}

	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", server.url, true, "fetch")}
	if got := toolNames(f.build(t)); !slices.Equal(got, []string{"fetch"}) {
		t.Errorf("after the grant changed: tools = %v, want [fetch]", got)
	}
}

func TestACommandServerIsReportedNotBound(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{
		commandServer("filesystem"),
		urlServer("web", serve(t, "fetch").url, true),
	}

	built := f.build(t)

	want := []Unavailable{{Name: "filesystem", Reason: "a command server runs in the workspace, which slice 3 adds"}}
	if !reflect.DeepEqual(built.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", built.Unavailable, want)
	}
	if got := toolNames(built); !slices.Equal(got, []string{"fetch"}) {
		t.Errorf("tools = %v, want only the URL server's", got)
	}
	if !slices.Equal(f.clients.asked, []string{"web"}) {
		t.Errorf("clients were asked for %v, want only web", f.clients.asked)
	}
}

func TestAnUnreachableServerFailsTheBuildNamingIt(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("search-engine", unreachable(t), false)}

	err := f.buildErr(t)
	if err == nil || !strings.Contains(err.Error(), `"search-engine" could not be reached`) {
		t.Errorf("Build = %v, want the server named as unreachable", err)
	}
}

// What discovery refuses — a link-local address, a missing token — the run
// is refused too, naming the server and keeping the reason.
func TestAServerDiscoveryRefusesFailsTheBuild(t *testing.T) {
	f := newFixture()
	refused := errors.New("the address is link-local")
	f.clients.err = refused
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("metadata", "http://169.254.169.254", false)}

	err := f.buildErr(t)
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), `"metadata"`) {
		t.Errorf("Build = %v, want %v naming the server", err, refused)
	}
}

func TestTwoServersOfferingOneToolNameFailTheBuild(t *testing.T) {
	f := newFixture()
	first, second := serve(t, "search"), serve(t, "search")
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("google", first.url, true),
		urlServer("bing", second.url, true),
	}

	err := f.buildErr(t)
	if err == nil || !strings.Contains(err.Error(), `"google" and "bing" both offer "search"`) {
		t.Fatalf("Build = %v, want the clash named", err)
	}
	first.waitClosed(t, 1)
	second.waitClosed(t, 1)
}

func TestAToolCallReachesItsServer(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "fetch").url, true)}

	built := f.build(t)

	got, err := tool(t, built, "fetch").Invoke(t.Context(), json.RawMessage(`{}`))
	if err != nil || got != "fetch ran" {
		t.Errorf("fetch = %q, %v; want the server's answer", got, err)
	}
}

// A server that accepts and never answers holds the build only as long as the
// run's context allows.
//
// It holds every call and acknowledges notifications: go-sdk follows a
// cancelled call with notifications/cancelled on a five-second budget of its
// own, so a server silent to those too holds Build ten seconds past the
// deadline — bounded, measured, and not this package's to shorten.
func TestConnectingGivesUpWhenTheRunsContextEnds(t *testing.T) {
	stop := make(chan struct{})
	var held atomic.Int32
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"method":"notifications/`) {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		held.Add(1)
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(silent.Close)
	t.Cleanup(func() { close(stop) })
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("silent", silent.URL, true)}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		built, err := f.builder.Build(ctx, f.run, noSession{})
		if err == nil {
			built.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), `"silent"`) {
			t.Errorf("Build = %v, want the silent server named", err)
		}
		if held.Load() == 0 {
			t.Error("the server held no call, so this tested nothing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Build was still connecting 5s after the run's context ended")
	}
}

// A server that takes the connection and never answers is what the connect
// timeout is for; the run's own context may allow hours.
func TestConnectingGivesUpAtItsTimeout(t *testing.T) {
	stop := make(chan struct{})
	var held atomic.Int32
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"method":"notifications/`) {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		held.Add(1)
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(silent.Close)
	t.Cleanup(func() { close(stop) })
	f := newFixture()
	f.builder.connectTimeout = 200 * time.Millisecond
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("silent", silent.URL, true)}

	done := make(chan error, 1)
	go func() {
		built, err := f.builder.Build(t.Context(), f.run, noSession{})
		if err == nil {
			built.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), `"silent"`) {
			t.Errorf("Build = %v, want the silent server named", err)
		}
		if held.Load() == 0 {
			t.Error("the server held no call, so this tested nothing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Build was still connecting 5s after its connect timeout")
	}
}

func TestTheConnectTimeoutIsThirtySeconds(t *testing.T) {
	t.Parallel()

	if got := NewBuilder(nil, nil, nil).connectTimeout; got != 30*time.Second {
		t.Errorf("connectTimeout = %v, want 30s", got)
	}
}

// The connect timeout bounds connecting and nothing after: a session whose
// connect context has been cancelled still carries every call of the run.
func TestASessionOutlivesItsConnectTimeout(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "fetch").url, true)}

	ctx, cancel := context.WithCancel(t.Context())
	built, err := f.builder.Build(ctx, f.run, noSession{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)
	cancel()

	if _, err := tool(t, built, "fetch").Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Errorf("a call after the build's context ended: %v", err)
	}
}

// A tool call is bounded by toolCallTimeout, and a caller's own earlier
// deadline still wins.
func TestEachToolCallIsBounded(t *testing.T) {
	f := newFixture()
	seen := &deadlines{}
	f.clients.client = &http.Client{Transport: seen}
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "fetch").url, true)}
	fetch := tool(t, f.build(t), "fetch")

	t.Run("a caller with no deadline", func(t *testing.T) {
		seen.reset()
		before := time.Now()
		if _, err := fetch.Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		after := time.Now()

		got := seen.all()
		if len(got) == 0 {
			t.Fatal("the call carried no deadline")
		}
		for _, dl := range got {
			if dl.Before(before.Add(toolCallTimeout)) || dl.After(after.Add(toolCallTimeout)) {
				t.Errorf("deadline %v is not toolCallTimeout after the call", dl)
			}
		}
	})

	t.Run("a caller with a nearer deadline", func(t *testing.T) {
		seen.reset()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		mine, _ := ctx.Deadline()
		if _, err := fetch.Invoke(ctx, json.RawMessage(`{}`)); err != nil {
			t.Fatalf("fetch: %v", err)
		}

		for _, dl := range seen.all() {
			if !dl.Equal(mine) {
				t.Errorf("deadline %v, want the caller's %v", dl, mine)
			}
		}
	})
}

// --- closing ---

func TestBuiltCloseEndsEverySession(t *testing.T) {
	f := newFixture()
	a, b := serve(t, "one"), serve(t, "two")
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("a", a.url, true), urlServer("b", b.url, true)}

	built, err := f.builder.Build(t.Context(), f.run, noSession{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if a.deletes.Load() != 0 || b.deletes.Load() != 0 {
		t.Fatal("a session was closed before the run was done with it")
	}

	built.Close()
	a.waitClosed(t, 1)
	b.waitClosed(t, 1)
}

func TestBuildClosesWhatItOpenedWhenALaterServerFails(t *testing.T) {
	f := newFixture()
	opened := serve(t, "fetch")
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("web", opened.url, true),
		urlServer("gone", unreachable(t), true),
	}

	if err := f.buildErr(t); err == nil {
		t.Fatal("Build succeeded with an unreachable server")
	}
	opened.waitClosed(t, 1)
}

func TestBuildClosesWhatItOpenedWhenTheSkillsCannotBeRead(t *testing.T) {
	f := newFixture()
	opened := serve(t, "fetch")
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", opened.url, true)}
	down := errors.New("connection refused")
	f.store.skillsErr = down

	if err := f.buildErr(t); !errors.Is(err, down) {
		t.Fatalf("Build = %v, want %v", err, down)
	}
	opened.waitClosed(t, 1)
}

// --- skills ---

func skillFixture() (*fixture, db.Skill, db.Skill) {
	f := newFixture()
	writing := db.Skill{ID: uuid.New(), TeamID: f.run.TeamID, Name: "writing", Description: "House style.", Body: "Write plainly."}
	charts := db.Skill{ID: uuid.New(), TeamID: f.run.TeamID, Name: "charts", Description: "Drawing charts.", Body: "Label the axes."}
	f.store.skills = []db.Skill{writing, charts}
	f.store.files[writing.ID] = []db.SkillFile{
		{SkillID: writing.ID, Path: "guide.md", MediaType: "text/markdown", ObjectKey: "k-guide"},
		{SkillID: writing.ID, Path: "logo.png", MediaType: "image/png", ObjectKey: "k-logo"},
	}
	f.objects.data["k-guide"] = "# Guide"
	return f, writing, charts
}

func TestEachSkillCarriesItsOwnBody(t *testing.T) {
	f, _, _ := skillFixture()
	built := f.build(t)

	want := map[string]string{"writing": "Write plainly.", "charts": "Label the axes."}
	if len(built.Spec.Skills) != len(want) {
		t.Fatalf("skills = %d, want %d", len(built.Spec.Skills), len(want))
	}
	for _, s := range built.Spec.Skills {
		body, err := s.Body(t.Context())
		if err != nil || body != want[s.Name] {
			t.Errorf("%s body = %q, %v; want %q", s.Name, body, err, want[s.Name])
		}
	}
}

func TestASkillsFilesAreReadOnlyWhenAsked(t *testing.T) {
	f, _, _ := skillFixture()
	built := f.build(t)

	if got := f.objects.asked(); len(got) != 0 {
		t.Fatalf("the build read %v before anything asked", got)
	}

	writing := built.Spec.Skills[0]
	names := []string{writing.Resources[0].Name, writing.Resources[1].Name}
	if !slices.Equal(names, []string{"guide.md", "logo.png"}) {
		t.Fatalf("resources = %v, want one per file, named by path", names)
	}

	got, err := writing.Resources[0].Read(t.Context())
	if err != nil || got != "# Guide" {
		t.Errorf("guide.md = %q, %v", got, err)
	}
	if want := []string{f.run.TeamID.String() + "/k-guide"}; !slices.Equal(f.objects.asked(), want) {
		t.Errorf("objects read = %v, want %v", f.objects.asked(), want)
	}
}

// A binary file would reach the model as mojibake, so it is refused by its
// recorded type without being fetched.
func TestASkillsBinaryFileIsRefusedUnread(t *testing.T) {
	f, _, _ := skillFixture()
	built := f.build(t)

	_, err := built.Spec.Skills[0].Resources[1].Read(t.Context())
	if err == nil || !strings.Contains(err.Error(), "logo.png is image/png") {
		t.Errorf("logo.png = %v, want it refused by its type", err)
	}
	if got := f.objects.asked(); len(got) != 0 {
		t.Errorf("objects read = %v, want none", got)
	}
}

func TestASkillsFileThatCannotBeFetchedIsNamed(t *testing.T) {
	f, _, _ := skillFixture()
	down := errors.New("bucket unreachable")
	f.objects.err = down
	built := f.build(t)

	_, err := built.Spec.Skills[0].Resources[0].Read(t.Context())
	if !errors.Is(err, down) || !strings.Contains(err.Error(), "guide.md") {
		t.Errorf("guide.md = %v, want %v naming the file", err, down)
	}
}

// --- loops ---

func TestLoopsNamesEveryRegisteredPatternOnce(t *testing.T) {
	want := []string{"plan", "react", "reflection", "rewoo"}
	if got := Loops(); !slices.Equal(got, want) {
		t.Errorf("Loops() = %v, want %v", got, want)
	}

	registered := make([]string, 0, len(Patterns()))
	for _, p := range Patterns() {
		registered = append(registered, string(p.Name()))
	}
	slices.Sort(registered)
	if !slices.Equal(registered, Loops()) || len(slices.Compact(registered)) != len(Patterns()) {
		t.Errorf("Patterns() names %v; Loops() must list each once", registered)
	}
}
