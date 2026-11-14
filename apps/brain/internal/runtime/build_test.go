package runtime

import (
	"bytes"
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

	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

// --- fakes ---

type fakeStore struct {
	mu         sync.Mutex
	agents     map[uuid.UUID]db.Agent
	agentErr   error
	grants     []db.ListGrantedMCPServersRow
	grantsErr  error
	grantsRead int
	// catalogue, when set, is what discovery recorded in place of what each
	// granted server offers.
	catalogue []db.ListGrantedMCPToolsRow
	toolsErr  error
	// attached are the attachments of every session, newest first.
	attached    []db.Attachment
	attachedErr error
	skills      []db.Skill
	skillsErr   error
	files       map[uuid.UUID][]db.SkillFile

	settings      *db.TeamSetting
	settingsErr   error
	settingsReads int
	others        int64
	othersErr     error
	othersAsked   []db.CountOtherAgentsParams
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

// ListGrantedMCPTools is what each granted server offers, as a discovery of
// it would have recorded, less what its allow-list leaves out, as the query
// does.
func (f *fakeStore) ListGrantedMCPTools(context.Context, uuid.UUID) ([]db.ListGrantedMCPToolsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.catalogue != nil || f.toolsErr != nil {
		return f.catalogue, f.toolsErr
	}
	var out []db.ListGrantedMCPToolsRow
	for _, g := range f.grants {
		if g.McpServer.Url == nil {
			continue
		}
		names, _ := offered.Load(*g.McpServer.Url)
		tools, _ := names.([]string)
		// Ordered by name, as the query orders them.
		tools = slices.Sorted(slices.Values(tools))
		for _, name := range tools {
			if g.Allow != nil && !slices.Contains(g.Allow, name) {
				continue
			}
			out = append(out, db.ListGrantedMCPToolsRow{
				McpServerID: g.McpServer.ID, Name: name, Description: name + " things",
				InputSchema: []byte(`{"type":"object"}`),
			})
		}
	}
	return out, nil
}

func (f *fakeStore) ListAttachmentsBySession(_ context.Context, arg db.ListAttachmentsBySessionParams) ([]db.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.Attachment
	for _, a := range f.attached {
		if a.SessionID == arg.SessionID && len(out) < int(arg.PageSize) {
			out = append(out, a)
		}
	}
	return out, f.attachedErr
}

func (f *fakeStore) GetAttachmentInSession(_ context.Context, arg db.GetAttachmentInSessionParams) (db.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachedErr != nil {
		return db.Attachment{}, f.attachedErr
	}
	for _, a := range f.attached {
		if a.ID == arg.ID && a.SessionID == arg.SessionID {
			return a, nil
		}
	}
	return db.Attachment{}, pgx.ErrNoRows
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
// DELETEs that end a session, which is how a test sees Close happen, and
// every request, which is how it sees a server never reached.
type mcpServer struct {
	url      string
	deletes  atomic.Int32
	requests atomic.Int32
	// opened counts the sessions begun: each initialize.
	opened atomic.Int32
	// refuse, while set, answers every request 503.
	refuse atomic.Bool
}

// offered is what each test server offers, by its address, for the fake
// store to list as discovery would have.
var offered sync.Map

// offer records tools for an address no test server answers, such as one
// that has gone away since discovery.
func offer(url string, tools ...string) string {
	offered.Store(url, tools)
	return url
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
		s.requests.Add(1)
		if s.refuse.Load() {
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if strings.Contains(string(body), `"method":"initialize"`) {
				s.opened.Add(1)
			}
		}
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
	offer(s.url, tools...)
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
	graph   *graphFixture
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
	f.graph = newGraphFixture()
	f.builder = NewBuilder(f.store, f.objects, f.clients, f.graph.deps(), nil, nil)
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

	built, err := f.builder.Build(t.Context(), f.run, noSession{}, Lineage{})
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

	built, err := f.builder.Build(t.Context(), f.run, noSession{}, Lineage{})
	if err == nil {
		built.Close()
	}
	return err
}

// toolNames is the MCP tools a build bound: a run may also have the brain's
// own graph and delegate tools, which the tests below check on their own.
func toolNames(built Built) []string {
	out := make([]string, 0, len(built.Spec.Tools))
	for _, tl := range heldTools(built) {
		if tl.Description != graphDescription && tl.Description != delegateDescription && tl.Description != planDescription {
			out = append(out, tl.Name)
		}
	}
	slices.Sort(out)
	return out
}

// heldTools is every tool the run may use: those sent and those it finds.
func heldTools(built Built) []agent.Tool {
	tools := slices.Clone(built.Spec.Tools)
	if built.Spec.Discovery != nil {
		tools = append(tools, built.Spec.Discovery.Tools...)
	}
	return tools
}

func tool(t *testing.T, built Built, name string) agent.Tool {
	t.Helper()

	for _, tl := range heldTools(built) {
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
			built, err := f.builder.Build(t.Context(), f.run, session, Lineage{})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if built.Spec.Session != session {
				t.Errorf("the spec's session is %v, want the one passed in", built.Spec.Session)
			}

			got := built.Spec
			if want := (agent.Compaction{AfterMessages: 100, AfterInputTokens: 100_000}); got.Compaction == nil ||
				*got.Compaction != want {
				t.Errorf("compaction = %+v, want %+v: the default team setting and a tenth of the default budget, "+
					"capped", got.Compaction, want)
			}
			got.Tools, got.Skills, got.Session, got.Compaction = nil, nil, nil, nil
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

	// Shown to everyone who reads the run, so it says what the reader can act
	// on and nothing of the brain's own plans.
	want := []Unavailable{{Name: "filesystem", Reason: "command servers can't be used yet"}}
	if !reflect.DeepEqual(built.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", built.Unavailable, want)
	}
	if got := toolNames(built); !slices.Equal(got, []string{"fetch"}) {
		t.Errorf("tools = %v, want only the URL server's", got)
	}
	if len(f.clients.asked) != 0 {
		t.Errorf("clients were asked for %v; a build reaches no server", f.clients.asked)
	}
}

// A server that is down fails the calls that need it, naming it, and nothing
// else: the run builds, and the other servers' tools work.
func TestAServerThatIsDownFailsOnlyTheCallsThatNeedIt(t *testing.T) {
	f := newFixture()
	gone := offer(unreachable(t)+"/mcp?key=sekrit", "search")
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("search-engine", gone, false),
		urlServer("web", serve(t, "fetch").url, true),
	}
	built := f.build(t)

	if got, err := tool(t, built, "fetch").Invoke(t.Context(), json.RawMessage(`{}`)); err != nil || got != "fetch ran" {
		t.Fatalf("fetch = %q, %v; a server that is down took another down with it", got, err)
	}
	_, err := tool(t, built, "search-engine__search").Invoke(t.Context(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), `"search-engine" could not be reached`) {
		t.Fatalf("search = %v, want the server named as unreachable", err)
	}
	// The address can carry a key, and the call's error is read by the model
	// and the whole session.
	if strings.Contains(err.Error(), "sekrit") {
		t.Errorf("the error carries the key in the server's address: %v", err)
	}
	var down *serverDown
	if !errors.As(err, &down) {
		t.Errorf("search = %v, want a serverDown", err)
	}
}

// What discovery refuses — a link-local address, a missing token — the call is
// refused too, naming the server and keeping the reason.
func TestWhatDiscoveryRefusesFailsTheCall(t *testing.T) {
	f := newFixture()
	refused := errors.New("the address is link-local")
	f.clients.err = refused
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("metadata", offer("http://169.254.169.254", "read"), false)}
	built := f.build(t)

	_, err := tool(t, built, "metadata__read").Invoke(t.Context(), json.RawMessage(`{}`))
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), `"metadata"`) {
		t.Errorf("read = %v, want %v naming the server", err, refused)
	}
}

// Building a run reaches no server: they are reached by the calls.
func TestBuildingReachesNoServer(t *testing.T) {
	f := newFixture()
	web := serve(t, "fetch")
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("web", web.url, true),
		urlServer("gone", offer(unreachable(t), "search"), true),
	}

	built := f.build(t)
	if got := toolNames(built); !slices.Equal(got, []string{"fetch", "search"}) {
		t.Fatalf("tools = %v, want both servers' tools as discovery recorded them", got)
	}
	if web.requests.Load() != 0 || len(f.clients.asked) != 0 {
		t.Fatalf("the build sent %d requests and asked for %v clients", web.requests.Load(), f.clients.asked)
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
	if first.requests.Load() != 0 || second.requests.Load() != 0 {
		t.Errorf("finding the clash reached the servers")
	}
}

func TestAToolsCatalogueThatCannotBeReadFailsTheBuild(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "fetch").url, true)}
	down := errors.New("connection refused")
	f.store.toolsErr = down

	if err := f.buildErr(t); !errors.Is(err, down) || !strings.Contains(err.Error(), "MCP tools") {
		t.Fatalf("Build = %v, want %v", err, down)
	}
}

// A granted server discovery found nothing on is said so, rather than left
// out without a word.
func TestAServerWithNoToolsDiscoveredIsReported(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("fresh", unreachable(t), true)}

	built := f.build(t)
	want := []Unavailable{{Name: "fresh", Reason: "no tools of it have been discovered"}}
	if !reflect.DeepEqual(built.Unavailable, want) || len(toolNames(built)) != 0 {
		t.Errorf("unavailable = %+v, tools %v; want %+v", built.Unavailable, toolNames(built), want)
	}
}

// A name a provider would refuse is reported rather than sent, which would
// fail every request of the run.
func TestAToolNameAProviderWouldRefuseIsReported(t *testing.T) {
	f := newFixture()
	server := urlServer("web", serve(t, "fetch").url, true)
	f.store.grants = []db.ListGrantedMCPServersRow{server}
	f.store.catalogue = []db.ListGrantedMCPToolsRow{
		{McpServerID: server.McpServer.ID, Name: "fetch", InputSchema: []byte(`{}`)},
		{McpServerID: server.McpServer.ID, Name: "read file", InputSchema: []byte(`{}`)},
	}

	built := f.build(t)
	if got := toolNames(built); !slices.Equal(got, []string{"fetch"}) {
		t.Errorf("tools = %v, want fetch alone", got)
	}
	if len(built.Unavailable) != 1 || built.Unavailable[0].Name != "read file" {
		t.Errorf("unavailable = %+v, want the badly named tool", built.Unavailable)
	}
}

// What a run is told about a tool is what discovery recorded: its
// description and its schema.
func TestAToolIsDescribedAsDiscoveryRecordedIt(t *testing.T) {
	f := newFixture()
	server := urlServer("web", serve(t, "fetch").url, true)
	f.store.grants = []db.ListGrantedMCPServersRow{server}
	schema := `{"type":"object","properties":{"url":{"type":"string"}}}`
	f.store.catalogue = []db.ListGrantedMCPToolsRow{
		{McpServerID: server.McpServer.ID, Name: "fetch", Description: "Read a page", InputSchema: []byte(schema)},
	}

	fetch := tool(t, f.build(t), "fetch")
	if fetch.Description != "Read a page" || string(fetch.Schema) != schema {
		t.Errorf("fetch is %q with %s", fetch.Description, fetch.Schema)
	}
}

// Discovery may be older than the server: a tool it recorded that the server
// has since dropped says so, and the server's other tools still work.
func TestAToolTheServerNoLongerOffersSaysSo(t *testing.T) {
	f := newFixture()
	server := urlServer("web", serve(t, "fetch").url, true)
	f.store.grants = []db.ListGrantedMCPServersRow{server}
	f.store.catalogue = []db.ListGrantedMCPToolsRow{
		{McpServerID: server.McpServer.ID, Name: "fetch", InputSchema: []byte(`{}`)},
		{McpServerID: server.McpServer.ID, Name: "crawl", InputSchema: []byte(`{}`)},
	}
	built := f.build(t)

	_, err := tool(t, built, "crawl").Invoke(t.Context(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), `the MCP server "web" no longer offers crawl; discover its tools again`) {
		t.Fatalf("crawl = %v", err)
	}
	if got, err := tool(t, built, "fetch").Invoke(t.Context(), json.RawMessage(`{}`)); err != nil || got != "fetch ran" {
		t.Errorf("fetch = %q, %v", got, err)
	}
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

// One session per server however many calls, made one after another or side
// by side.
func TestCallsToOneServerShareOneSession(t *testing.T) {
	f := newFixture()
	web := serve(t, "fetch", "crawl")
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", web.url, true)}
	built, err := f.builder.Build(t.Context(), f.run, noSession{}, Lineage{})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		name := []string{"fetch", "crawl"}[i%2]
		wg.Go(func() {
			if _, err := tool(t, built, name).Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := tool(t, built, "fetch").Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	built.Close()
	web.waitClosed(t, 1)
	if web.opened.Load() != 1 || web.deletes.Load() != 1 {
		t.Errorf("%d sessions opened and %d closed, want one of each", web.opened.Load(), web.deletes.Load())
	}
}

// A server that failed to open is tried again by the next call: it may have
// been restarting.
func TestAServerThatFailedToOpenIsTriedAgain(t *testing.T) {
	f := newFixture()
	web := serve(t, "fetch")
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", web.url, true)}
	fetch := tool(t, f.build(t), "fetch")

	web.refuse.Store(true)
	if _, err := fetch.Invoke(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a call to a server refusing every request succeeded")
	}
	web.refuse.Store(false)
	if got, err := fetch.Invoke(t.Context(), json.RawMessage(`{}`)); err != nil || got != "fetch ran" {
		t.Fatalf("after the server came back: %q, %v", got, err)
	}
}

// A call that arrives after the run closed opens nothing that would outlive
// it.
func TestACallAfterTheRunClosedLeavesNoSession(t *testing.T) {
	f := newFixture()
	web := serve(t, "fetch")
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", web.url, true)}
	built, err := f.builder.Build(t.Context(), f.run, noSession{}, Lineage{})
	if err != nil {
		t.Fatal(err)
	}
	built.Close()
	built.Close()

	if _, err := tool(t, built, "fetch").Invoke(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a call after Close succeeded")
	}
	web.waitClosed(t, web.opened.Load())
}

// A server that accepts and never answers holds the call only as long as the
// call's context allows.
//
// It holds every call and acknowledges notifications: go-sdk follows a
// cancelled call with notifications/cancelled on a five-second budget of its
// own, so a server silent to those too holds the call ten seconds past the
// deadline — bounded, measured, and not this package's to shorten.
func TestConnectingGivesUpWhenTheCallsContextEnds(t *testing.T) {
	silent, held := silentServer(t)
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("silent", offer(silent, "wait"), true)}
	wait := tool(t, f.build(t), "wait")

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := wait.Invoke(ctx, json.RawMessage(`{}`))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), `"silent"`) {
			t.Errorf("wait = %v, want the silent server named", err)
		}
		if held.Load() == 0 {
			t.Error("the server held no call, so this tested nothing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call was still connecting 5s after its context ended")
	}
}

// A server that takes the connection and never answers is what the connect
// timeout is for; the call's own context allows five minutes.
func TestConnectingGivesUpAtItsTimeout(t *testing.T) {
	silent, held := silentServer(t)
	f := newFixture()
	f.builder.connectTimeout = 200 * time.Millisecond
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("silent", offer(silent, "wait"), true)}
	wait := tool(t, f.build(t), "wait")

	done := make(chan error, 1)
	go func() {
		_, err := wait.Invoke(t.Context(), json.RawMessage(`{}`))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), `"silent"`) {
			t.Errorf("wait = %v, want the silent server named", err)
		}
		if held.Load() == 0 {
			t.Error("the server held no call, so this tested nothing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call was still connecting 5s after its connect timeout")
	}
}

// silentServer takes every request and answers none but notifications.
func silentServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	stop := make(chan struct{})
	held := &atomic.Int32{}
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
	return silent.URL, held
}

func TestTheConnectTimeoutIsThirtySeconds(t *testing.T) {
	t.Parallel()

	if got := NewBuilder(nil, nil, nil, nil, nil, nil).connectTimeout; got != 30*time.Second {
		t.Errorf("connectTimeout = %v, want 30s", got)
	}
}

// The connect timeout bounds connecting and nothing after: a session opened
// by a call whose context has ended still carries the run's later calls.
func TestASessionOutlivesTheCallThatOpenedIt(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "fetch").url, true)}
	fetch := tool(t, f.build(t), "fetch")

	ctx, cancel := context.WithCancel(t.Context())
	if _, err := fetch.Invoke(ctx, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("the first call: %v", err)
	}
	cancel()

	if _, err := fetch.Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Errorf("a call after the first call's context ended: %v", err)
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
	// Opened first, so what is measured below is the call and not the
	// connect, which its own timeout bounds.
	if _, err := fetch.Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("fetch: %v", err)
	}

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

// Close ends the sessions the calls opened, and a server never called was
// never reached at all.
func TestBuiltCloseEndsEverySessionTheCallsOpened(t *testing.T) {
	f := newFixture()
	a, b, never := serve(t, "one"), serve(t, "two"), serve(t, "three")
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("a", a.url, true), urlServer("b", b.url, true), urlServer("never", never.url, true),
	}

	built, err := f.builder.Build(t.Context(), f.run, noSession{}, Lineage{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, name := range []string{"one", "two"} {
		if _, err := tool(t, built, name).Invoke(t.Context(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if a.deletes.Load() != 0 || b.deletes.Load() != 0 {
		t.Fatal("a session was closed before the run was done with it")
	}

	built.Close()
	a.waitClosed(t, 1)
	b.waitClosed(t, 1)
	if never.requests.Load() != 0 {
		t.Errorf("a server no call needed was sent %d requests", never.requests.Load())
	}
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

// --- the graph tool ---

func (f *graphFixture) deps() *GraphDeps {
	return &GraphDeps{Search: f.search, Embed: f.embed, Store: f.reach}
}

func graphTools(built Built) []agent.Tool {
	var out []agent.Tool
	for _, tl := range built.Spec.Tools {
		if tl.Name == GraphToolName {
			out = append(out, tl)
		}
	}
	return out
}

// Every run can search its team's graph, scoped to the run's team and to
// what the run's agent may reach.
func TestBuildBindsTheGraphTool(t *testing.T) {
	f := newFixture()
	built := f.build(t)

	g := graphTools(built)
	if len(g) != 1 || g[0].Description != graphDescription {
		t.Fatalf("graph tools = %d, want the brain's one", len(g))
	}
	if len(built.Unavailable) != 0 {
		t.Errorf("unavailable = %+v, want nothing", built.Unavailable)
	}

	if _, err := g[0].Invoke(t.Context(), json.RawMessage(`{"query":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.graph.search.teams; len(got) != 1 || got[0] != f.run.TeamID {
		t.Errorf("searched teams %v, want the run's %s", got, f.run.TeamID)
	}
	want := db.ListReachableAgentIDsParams{TeamID: f.run.TeamID, AgentID: f.agent.ID}
	if got := f.graph.reach.agentsAsked; len(got) != 1 || got[0] != want {
		t.Errorf("reach asked with %+v, want %+v", got, want)
	}
}

func TestBuildWithoutTheGraphSaysWhy(t *testing.T) {
	f := newFixture()
	f.builder = NewBuilder(f.store, f.objects, f.clients, nil, nil, nil)

	built := f.build(t)

	if g := graphTools(built); len(g) != 0 {
		t.Errorf("graph tools = %d with no graph, want none", len(g))
	}
	want := []Unavailable{{Name: "graph", Reason: "no embedding model is configured, so the team's graph is not built"}}
	if !reflect.DeepEqual(built.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", built.Unavailable, want)
	}
}

// Review Focus 4: a bare server's tool named graph would collide with the
// brain's. The brain's keeps the name, the server's is left out and said so,
// and the run goes ahead with everything else.
func TestBuildLeavesOutAnMCPToolNamedGraph(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "graph", "search").url, true)}

	built := f.build(t)

	if got := toolNames(built); !slices.Equal(got, []string{"search"}) {
		t.Errorf("MCP tools = %v, want only search", got)
	}
	if g := graphTools(built); len(g) != 1 || g[0].Description != graphDescription {
		t.Errorf("graph tools = %d, want only the brain's", len(g))
	}
	want := []Unavailable{{Name: "graph", Reason: `the MCP server "web" offers a tool named graph, which is the brain's own`}}
	if !reflect.DeepEqual(built.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", built.Unavailable, want)
	}
}

// Every run compacts, so the SDK adds read_output and refuses a second tool
// with that name: a bare server's read_output would stop the run before its
// first step. It is left out and said so, as graph is.
func TestBuildLeavesOutAnMCPToolNamedReadOutput(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, agent.ReadOutputToolName, "search").url, true)}

	built := f.build(t)

	if got := toolNames(built); !slices.Equal(got, []string{"search"}) {
		t.Errorf("MCP tools = %v, want only search", got)
	}
	want := []Unavailable{{Name: "read_output", Reason: `the MCP server "web" offers a tool named read_output, which is the brain's own`}}
	if !reflect.DeepEqual(built.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", built.Unavailable, want)
	}

	runner, err := agent.New(func(agent.Endpoint) (agent.ModelClient, error) {
		return newScripted(answer("done", 10, 5)), nil
	}, patterns.ReAct())
	if err != nil {
		t.Fatal(err)
	}
	task := []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "go"}}}}
	spec := built.Spec
	spec.Session = nil // the fixture's session is a stand-in; this run needs none
	if _, err := runner.Run(t.Context(), spec, task, agent.Endpoint{}, nil); err != nil {
		t.Fatalf("the run did not start: %v", err)
	}
}

// A prefixed server's graph is web__graph, which collides with nothing.
func TestBuildKeepsAPrefixedToolNamedGraph(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "graph").url, false)}

	built := f.build(t)

	if got := toolNames(built); !slices.Equal(got, []string{"web__graph"}) {
		t.Errorf("MCP tools = %v, want web__graph", got)
	}
	if len(built.Unavailable) != 0 {
		t.Errorf("unavailable = %+v, want nothing", built.Unavailable)
	}
}

// --- the delegate tool ---

func delegateTools(built Built) []agent.Tool {
	var out []agent.Tool
	for _, tl := range built.Spec.Tools {
		if tl.Name == DelegateToolName {
			out = append(out, tl)
		}
	}
	return out
}

// withDelegates gives the fixture's builder what NewExecutor gives it, with
// the fixture's agent set to delegation and listing delegates.
func (f *fixture) withDelegates(delegation string, delegates ...uuid.UUID) *fakeDelegateStore {
	f.agent.Delegation = delegation
	f.store.agents[f.agent.ID] = f.agent
	ds := &fakeDelegateStore{agents: []db.Agent{f.agent}, delegates: map[uuid.UUID][]uuid.UUID{f.agent.ID: delegates}}
	f.builder.delegates = &DelegateDeps{Store: ds, Run: &fakeDelegator{}}
	return ds
}

// A tool that can only be refused is not given (orchestrator spec §4).
func TestNoneGetsNoDelegateTool(t *testing.T) {
	f := newFixture()
	f.withDelegates("none")

	if got := delegateTools(f.build(t)); len(got) != 0 {
		t.Errorf("delegate tools = %d for an agent that works alone", len(got))
	}
}

func TestAnEmptyListGetsNoDelegateTool(t *testing.T) {
	f := newFixture()
	ds := f.withDelegates("listed")

	if got := delegateTools(f.build(t)); len(got) != 0 {
		t.Errorf("delegate tools = %d for an empty list", len(got))
	}
	if ds.listReads != 1 {
		t.Errorf("the list was read %d times at build, want once", ds.listReads)
	}
}

func TestAListWithADelegateGetsTheTool(t *testing.T) {
	f := newFixture()
	f.withDelegates("listed", uuid.New())

	if got := delegateTools(f.build(t)); len(got) != 1 {
		t.Errorf("delegate tools = %d for a listed agent with a delegate, want 1", len(got))
	}
}

func TestATeamSettingGetsTheToolWithoutReadingAList(t *testing.T) {
	f := newFixture()
	ds := f.withDelegates("team")

	got := delegateTools(f.build(t))
	if len(got) != 1 || got[0].Description != delegateDescription {
		t.Fatalf("delegate tools = %d, want the brain's one", len(got))
	}
	if ds.listReads != 0 {
		t.Errorf("a team setting read the list %d times at build", ds.listReads)
	}
}

// A child at depth three could only ever be refused, so it is not offered.
func TestDepthThreeGetsNoDelegateTool(t *testing.T) {
	for depth, want := range map[int32]int{0: 1, 2: 1, 3: 0} {
		f := newFixture()
		f.withDelegates("team")
		f.run.Depth = depth

		if got := delegateTools(f.build(t)); len(got) != want {
			t.Errorf("depth %d: delegate tools = %d, want %d", depth, len(got), want)
		}
	}
}

// The tool Build binds carries the run and the lineage Build was given, so a
// child's cycle check sees the agents above it.
func TestTheBoundToolCarriesTheRunAndItsLineage(t *testing.T) {
	f := newFixture()
	ds := f.withDelegates("team")
	billing := db.Agent{ID: uuid.New(), TeamID: f.run.TeamID, Name: "billing"}
	ds.agents = append(ds.agents, billing)
	runner, ok := f.builder.delegates.Run.(*fakeDelegator)
	if !ok {
		t.Fatal("setup: the builder's delegator is not the fake")
	}
	l := Lineage{Worker: "w9", Chain: []uuid.UUID{billing.ID, f.agent.ID}}

	built, err := f.builder.Build(t.Context(), f.run, noSession{}, l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	tl := delegateTools(built)[0]

	_, err = tl.Invoke(agent.WithToolCallID(t.Context(), "c1"), json.RawMessage(`{"agent":"billing","objective":"x","output":"y"}`))
	if err == nil || err.Error() != `"billing" is already working on this, above you` {
		t.Errorf("err = %v; want the cycle refusal from the lineage Build was given", err)
	}
	if len(runner.delegations()) != 0 {
		t.Error("a refused call started a child")
	}
}

// The brain's delegate keeps the name; a bare server's tool named delegate is
// left out and said so, whatever the agent's own setting.
func TestBuildLeavesOutAnMCPToolNamedDelegate(t *testing.T) {
	for _, setting := range []string{"team", "none"} {
		t.Run(setting, func(t *testing.T) {
			f := newFixture()
			f.withDelegates(setting)
			f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "delegate", "search").url, true)}

			built := f.build(t)

			if got := toolNames(built); !slices.Equal(got, []string{"search"}) {
				t.Errorf("MCP tools = %v, want only search", got)
			}
			want := []Unavailable{{Name: "delegate", Reason: `the MCP server "web" offers a tool named delegate, which is the brain's own`}}
			if !reflect.DeepEqual(built.Unavailable, want) {
				t.Errorf("unavailable = %+v, want %+v", built.Unavailable, want)
			}
		})
	}
}

// A failed Build returns a Built with nothing in it, and closing that is safe.
func TestClosingAnEmptyBuiltIsSafe(t *testing.T) {
	t.Parallel()

	Built{}.Close()
}

// --- found with the graph ---

// The MCP tools are handed to the SDK to find with graph, grouped by server;
// they are not among the tools sent. Whether they are sent after all is the
// SDK's, by their size.
func TestTheMCPToolsAreFoundWithTheGraph(t *testing.T) {
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("github", serve(t, "list_issues", "close_issue").url, false),
		urlServer("web", serve(t, "fetch").url, true),
		// Every tool of it left out, so it is no group to search.
		urlServer("clash", serve(t, GraphToolName).url, true),
	}

	built := f.build(t)
	d := built.Spec.Discovery
	if d == nil || d.Search != GraphToolName {
		t.Fatalf("discovery = %+v, want the MCP tools found with graph", d)
	}
	for _, tl := range built.Spec.Tools {
		if strings.HasPrefix(tl.Name, "github__") || tl.Name == "fetch" {
			t.Errorf("%s is sent as well as found", tl.Name)
		}
	}
	want := []agent.Group{
		{Name: "github", Tools: []string{"github__close_issue", "github__list_issues"}},
		{Name: "web", Tools: []string{"fetch"}},
	}
	if !reflect.DeepEqual(d.Groups, want) {
		t.Errorf("groups = %+v, want %+v", d.Groups, want)
	}
}

// With no graph there is nothing to find tools with, so every one is sent.
func TestWithNoGraphEveryToolIsSent(t *testing.T) {
	f := newFixture()
	f.builder = NewBuilder(f.store, f.objects, f.clients, nil, nil, nil)
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "fetch").url, true)}

	built := f.build(t)
	if built.Spec.Discovery != nil {
		t.Fatalf("discovery = %+v with no graph to find tools with", built.Spec.Discovery)
	}
	if got := toolNames(built); !slices.Equal(got, []string{"fetch"}) {
		t.Errorf("tools = %v, want fetch sent", got)
	}
}

// A run holding no MCP tool has nothing to find.
func TestARunWithNoMCPToolsHasNothingToFind(t *testing.T) {
	f := newFixture()
	if built := f.build(t); built.Spec.Discovery != nil {
		t.Errorf("discovery = %+v for a run with no MCP tools", built.Spec.Discovery)
	}
}

// A bare server's tool under a name the SDK gives its own tools would stop the
// run, so it is reported instead, and the run still builds.
func TestAServersToolUnderAnSDKNameIsReported(t *testing.T) {
	names := []string{agent.DescribeToolsName, agent.CallToolName, agent.SkillToolName, agent.SkillResourceToolName}
	f := newFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("odd", serve(t, append([]string{"fetch"}, names...)...).url, true)}

	built := f.build(t)
	if got := toolNames(built); !slices.Equal(got, []string{"fetch"}) {
		t.Errorf("tools = %v, want fetch alone", got)
	}
	var reported []string
	for _, u := range built.Unavailable {
		reported = append(reported, u.Name)
	}
	slices.Sort(reported)
	want := slices.Sorted(slices.Values(names))
	if !slices.Equal(reported, want) {
		t.Errorf("reported %v, want %v", reported, want)
	}
}

// The graph is told what the run calls each tool it holds, by the tool's id.
func TestTheGraphKnowsWhatTheRunCallsItsTools(t *testing.T) {
	f := newFixture()
	server := urlServer("github", serve(t, "list_issues").url, false)
	f.store.grants = []db.ListGrantedMCPServersRow{server}
	id := uuid.New()
	f.store.catalogue = []db.ListGrantedMCPToolsRow{
		{ID: id, McpServerID: server.McpServer.ID, Name: "list_issues", InputSchema: []byte(`{}`)},
	}
	f.graph.search.hits = []graph.Hit{{Kind: graph.KindTool, ID: id, Name: "list_issues", Score: 0.9}}

	out, err := graphTool(t, f.build(t)).Invoke(t.Context(), json.RawMessage(`{"query":"issues","server":"github"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"use_as":"github__list_issues"`) {
		t.Errorf("graph = %s, want the run's name for the tool", out)
	}
	if got := f.graph.search.asked[0].Reach.Tools; !slices.Equal(got, []uuid.UUID{}) {
		// The fixture's reach holds a different id, so narrowing to github
		// leaves nothing: the filter takes away and never adds.
		t.Errorf("reach = %v, want github's tools within the agent's reach, which is none", got)
	}
}

func graphTool(t *testing.T, built Built) agent.Tool {
	t.Helper()
	for _, tl := range built.Spec.Tools {
		if tl.Name == GraphToolName {
			return tl
		}
	}
	t.Fatal("no graph tool")
	return agent.Tool{}
}
