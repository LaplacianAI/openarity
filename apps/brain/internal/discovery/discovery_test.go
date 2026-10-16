package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// --- fakes ---

type fakeSecrets struct {
	mu     sync.Mutex
	values map[string]string
	err    error
	asked  []string
}

func (f *fakeSecrets) Get(_ context.Context, path, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, path+"#"+key)
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.values[path+"#"+key]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

type served struct {
	name, desc string
	schema     any
}

// mcpServer is a real go-sdk server behind a handler that counts requests and
// records their Authorization header, so a test can say what reached it.
type mcpServer struct {
	*httptest.Server
	hits    atomic.Int32
	mu      sync.Mutex
	auth    []string
	methods []string
}

func newMCPServer(t *testing.T, tls bool, opts *sdk.ServerOptions, tools ...served) *mcpServer {
	t.Helper()

	server := sdk.NewServer(&sdk.Implementation{Name: "remote", Version: "v1"}, opts)
	for _, tl := range tools {
		schema := tl.schema
		if schema == nil {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		server.AddTool(&sdk.Tool{Name: tl.name, Description: tl.desc, InputSchema: schema},
			func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{}, nil
			})
	}

	m := &mcpServer{}
	inner := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.hits.Add(1)
		m.mu.Lock()
		m.auth = append(m.auth, r.Header.Get("Authorization"))
		m.methods = append(m.methods, r.Method)
		m.mu.Unlock()
		inner.ServeHTTP(w, r)
	})

	if tls {
		m.Server = httptest.NewTLSServer(handler)
	} else {
		m.Server = httptest.NewServer(handler)
	}
	t.Cleanup(m.Close)
	return m
}

func (m *mcpServer) policy() Policy {
	p := Policy{Allow: allowAll}
	if m.TLS != nil {
		p.RootCAs = trusting(m.Server)
	}
	return p
}

func urlRow(u string) db.McpServer {
	return db.McpServer{Name: "github", Url: &u, Env: []byte("{}")}
}

func names(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

// --- against a real server ---

// The stored name is the one the server uses. Prefixing is the runtime's
// business, and depends on the server's name and bare flag at the time it
// runs, so storing a prefixed name would go stale on a rename.
func TestDiscoveryReturnsRemoteNamesNotPrefixedOnes(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	srv := newMCPServer(t, false, nil,
		served{name: "search_code", desc: "Search code", schema: schema},
		served{name: "create_issue", desc: "Open an issue"},
	)

	got, err := New(&fakeSecrets{}, srv.policy(), 5*time.Second).Discover(t.Context(), urlRow(srv.URL))
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	slices.SortFunc(got, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
	if want := []string{"create_issue", "search_code"}; !slices.Equal(names(got), want) {
		t.Fatalf("names = %v, want %v", names(got), want)
	}
	if got[1].Description != "Search code" {
		t.Errorf("description = %q", got[1].Description)
	}

	var gotSchema, wantSchema any
	if err := json.Unmarshal(got[1].InputSchema, &gotSchema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	_ = json.Unmarshal(schema, &wantSchema)
	if a, b := mustJSON(t, gotSchema), mustJSON(t, wantSchema); a != b {
		t.Errorf("schema = %s, want %s", a, b)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Tools come in pages; every page is read.
func TestEveryPageOfToolsIsRead(t *testing.T) {
	t.Parallel()

	var tools []served
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		tools = append(tools, served{name: n})
	}
	srv := newMCPServer(t, false, &sdk.ServerOptions{PageSize: 2}, tools...)

	got, err := New(&fakeSecrets{}, srv.policy(), 5*time.Second).Discover(t.Context(), urlRow(srv.URL))
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("found %v, want all five across three pages", names(got))
	}
}

// Discovery is one question and one answer. go-sdk opens a standing GET
// stream for server pushes by default, which would hold a connection to the
// server for nothing.
func TestDiscoveryOpensNoStandingStream(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, false, nil, served{name: "search"})
	if _, err := New(&fakeSecrets{}, srv.policy(), 5*time.Second).Discover(t.Context(), urlRow(srv.URL)); err != nil {
		t.Fatalf("discover: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if slices.Contains(srv.methods, http.MethodGet) {
		t.Errorf("the server saw %v, including a GET for a standing stream", srv.methods)
	}
}

func TestAServerWithNoToolsHasNone(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, false, nil)
	got, err := New(&fakeSecrets{}, srv.policy(), 5*time.Second).Discover(t.Context(), urlRow(srv.URL))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want no tools and no error", got, err)
	}
}

// The reference is split on its last '#', the secret read from there, and
// sent on every request of the session — not only the first.
func TestTheAuthSecretIsSentAsABearerToken(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, true, nil, served{name: "search"})
	sec := &fakeSecrets{values: map[string]string{"teams/t1/mcp/github#token": "ghp_live"}}
	row := urlRow(srv.URL)
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref

	if _, err := New(sec, srv.policy(), 5*time.Second).Discover(t.Context(), row); err != nil {
		t.Fatalf("discover: %v", err)
	}

	if !slices.Equal(sec.asked, []string{"teams/t1/mcp/github#token"}) {
		t.Errorf("asked the store for %v", sec.asked)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.auth) < 2 {
		t.Fatalf("the server saw %d requests, want initialize and at least one more", len(srv.auth))
	}
	for i, a := range srv.auth {
		if a != "Bearer ghp_live" {
			t.Errorf("request %d carried Authorization %q", i, a)
		}
	}
}

func TestAMissingSecretFailsBeforeConnecting(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, true, nil, served{name: "search"})
	row := urlRow(srv.URL)
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref

	_, err := New(&fakeSecrets{}, srv.policy(), 5*time.Second).Discover(t.Context(), row)
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("err = %v, want secrets.ErrNotFound", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("the server was contacted without its token")
	}
}

// An empty secret would build no bearer at all, and the server would see an
// unauthenticated client and answer as if the token were wrong.
func TestAnEmptySecretFailsBeforeConnecting(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, true, nil, served{name: "search"})
	row := urlRow(srv.URL)
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref

	_, err := New(&fakeSecrets{values: map[string]string{ref: ""}}, srv.policy(), 5*time.Second).Discover(t.Context(), row)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v, want the empty secret named", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("the server was contacted with no token")
	}
}

func TestAReferenceWithoutAKeyIsRefused(t *testing.T) {
	t.Parallel()

	row := urlRow("https://mcp.example.com")
	ref := "teams/t1/mcp/github"
	row.AuthSecretRef = &ref
	sec := &fakeSecrets{}

	if _, err := New(sec, Policy{Allow: allowNone}, time.Second).Discover(t.Context(), row); err == nil || !strings.Contains(err.Error(), "path#key") {
		t.Errorf("err = %v", err)
	}
	if len(sec.asked) != 0 {
		t.Error("the store was asked for a reference that is not one")
	}
}

// Nothing may start a process in the brain: a command server waits for a
// sandbox. The command here would leave a file behind if it ran.
func TestACommandServerIsRefusedWithoutStartingAProcess(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "ran")
	row := db.McpServer{Name: "fs", Command: []string{"sh", "-c", "touch " + marker}, Env: []byte(`{"T":"teams/t1/mcp/x#k"}`)}
	sec := &fakeSecrets{}

	_, err := New(sec, Policy{Allow: allowAll}, time.Second).Discover(t.Context(), row)
	if !errors.Is(err, ErrCommandServer) {
		t.Errorf("err = %v, want ErrCommandServer", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the command ran")
	}
	if len(sec.asked) != 0 {
		t.Error("the store was read for a server that cannot be discovered")
	}
}

// Nothing listens there, so the dial fails — and that is not a refusal, so it
// must not be reported as the caller's mistake.
func TestAnUnreachableServerIsAnError(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, false, nil)
	addr := srv.URL
	srv.Close()

	_, err := New(&fakeSecrets{}, Policy{Allow: allowAll}, 5*time.Second).Discover(t.Context(), urlRow(addr))
	if err == nil {
		t.Fatal("discovering a closed server succeeded")
	}
	if errors.Is(err, ErrRefused) || errors.Is(err, ErrUnusable) {
		t.Errorf("err = %v, which reads as the caller's mistake", err)
	}
}

// The handler maps ErrRefused to the caller's mistake, so it has to survive
// go-sdk's wrapping — through Connect, not only through the http.Client.
func TestARefusedAddressIsErrRefusedThroughTheSDK(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, false, nil, served{name: "search"})
	_, err := New(&fakeSecrets{}, Policy{Allow: allowNone}, 5*time.Second).Discover(t.Context(), urlRow(srv.URL))
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("a refused address was contacted")
	}
}

func TestATokenOverHTTPIsErrRefusedThroughTheSDK(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, false, nil, served{name: "search"})
	row := urlRow(srv.URL)
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref

	_, err := New(&fakeSecrets{values: map[string]string{ref: "ghp_live"}}, srv.policy(), 5*time.Second).Discover(t.Context(), row)
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("the token went over plain http")
	}
}

func TestARedirectIsErrRefusedThroughTheSDK(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, false, nil, served{name: "search"})
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	_, err := New(&fakeSecrets{}, Policy{Allow: allowAll}, 5*time.Second).Discover(t.Context(), urlRow(redirector.URL))
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

// A server that accepts the connection and never answers holds discovery for
// the timeout and no longer.
func TestDiscoveryHonoursTheTimeout(t *testing.T) {
	t.Parallel()

	// Cleanups run last-registered first, so the handlers are released before
	// Close waits for them.
	release := make(chan struct{})
	stuck := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(stuck.Close)
	t.Cleanup(func() { close(release) })

	start := time.Now()
	_, err := New(&fakeSecrets{}, Policy{Allow: allowAll}, 300*time.Millisecond).Discover(t.Context(), urlRow(stuck.URL))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a server that never answered was discovered")
	}
	if elapsed > 3*time.Second {
		t.Errorf("discovery took %v with a 300ms timeout", elapsed)
	}
}

// The timeout covers reading the secret too: a secret store that hangs is a
// discovery that hangs.
func TestTheTimeoutCoversReadingTheSecret(t *testing.T) {
	t.Parallel()

	row := urlRow("https://mcp.example.com")
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref

	start := time.Now()
	_, err := New(blockingSecrets{}, Policy{Allow: allowNone}, 200*time.Millisecond).Discover(t.Context(), row)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

type blockingSecrets struct{}

func (blockingSecrets) Get(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// --- what is stored, checked without a server ---

func toolsOf(ts ...*sdk.Tool) iter.Seq2[*sdk.Tool, error] {
	return func(yield func(*sdk.Tool, error) bool) {
		for _, t := range ts {
			if !yield(t, nil) {
				return
			}
		}
	}
}

func TestAToolWithNoSchemaTakesAnObject(t *testing.T) {
	t.Parallel()

	got, err := collect(toolsOf(&sdk.Tool{Name: "ping"}))
	if err != nil || len(got) != 1 || string(got[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("got %+v, %v", got, err)
	}
}

// A name has to be usable as a tool name and storable, and a hostile one is
// never repeated back — it could be megabytes long.
func TestAnUnusableNameIsRefusedWithoutEchoingIt(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", strings.Repeat("n", 65), "a.b", "a b", "a/b", "é", "a\x00b"} {
		_, err := collect(toolsOf(&sdk.Tool{Name: name}))
		if !errors.Is(err, ErrUnusable) {
			t.Errorf("name %q: err = %v, want ErrUnusable", name, err)
		}
		if name != "" && err != nil && strings.Contains(err.Error(), name) {
			t.Errorf("name %q was echoed: %v", name, err)
		}
	}

	if _, err := collect(toolsOf(&sdk.Tool{Name: strings.Repeat("n", 64)})); err != nil {
		t.Errorf("a 64-character name was refused: %v", err)
	}
}

func TestATwiceOfferedToolIsRefused(t *testing.T) {
	t.Parallel()

	_, err := collect(toolsOf(&sdk.Tool{Name: "a"}, &sdk.Tool{Name: "b"}, &sdk.Tool{Name: "a"}))
	if !errors.Is(err, ErrUnusable) || !strings.Contains(err.Error(), "a is offered twice") {
		t.Errorf("err = %v", err)
	}
}

func TestAtMostFiveHundredTools(t *testing.T) {
	t.Parallel()

	offer := func(n int) []*sdk.Tool {
		ts := make([]*sdk.Tool, n)
		for i := range ts {
			ts[i] = &sdk.Tool{Name: fmt.Sprintf("t%03d", i)}
		}
		return ts
	}

	if got, err := collect(toolsOf(offer(maxTools)...)); err != nil || len(got) != maxTools {
		t.Errorf("%d tools: got %d, %v", maxTools, len(got), err)
	}
	if _, err := collect(toolsOf(offer(maxTools + 1)...)); !errors.Is(err, ErrUnusable) {
		t.Errorf("%d tools: err = %v, want ErrUnusable", maxTools+1, err)
	}
}

func TestSizeLimitsAreInclusive(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		tool *sdk.Tool
		ok   bool
	}{
		"description at the limit": {&sdk.Tool{Name: "a", Description: strings.Repeat("d", maxDescriptionBytes)}, true},
		"description over":         {&sdk.Tool{Name: "a", Description: strings.Repeat("d", maxDescriptionBytes+1)}, false},
		"schema at the limit":      {&sdk.Tool{Name: "a", InputSchema: schemaOfSize(maxSchemaBytes)}, true},
		"schema over":              {&sdk.Tool{Name: "a", InputSchema: schemaOfSize(maxSchemaBytes + 1)}, false},
	} {
		_, err := collect(toolsOf(tc.tool))
		if tc.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrUnusable) {
			t.Errorf("%s: err = %v, want ErrUnusable", name, err)
		}
	}
}

// schemaOfSize is a JSON object that marshals to exactly n bytes.
func schemaOfSize(n int) json.RawMessage {
	const frame = `{"description":""}`
	return json.RawMessage(`{"description":"` + strings.Repeat("s", n-len(frame)) + `"}`)
}

// Postgres refuses NUL in text and \u0000 in jsonb. Stored anyway, the upsert
// fails with 22021 and the brain reports its own 500 for the server's content.
func TestANULIsRefusedBeforeItReachesPostgres(t *testing.T) {
	t.Parallel()

	for name, tool := range map[string]*sdk.Tool{
		"in the description": {Name: "a", Description: "bad\x00desc"},
		"in the schema":      {Name: "a", InputSchema: map[string]any{"description": "bad\x00schema"}},
	} {
		_, err := collect(toolsOf(tool))
		if !errors.Is(err, ErrUnusable) || !strings.Contains(err.Error(), "NUL") {
			t.Errorf("%s: err = %v, want ErrUnusable naming NUL", name, err)
		}
	}
}

func TestASchemaThatIsNotJSONIsRefused(t *testing.T) {
	t.Parallel()

	_, err := collect(toolsOf(&sdk.Tool{Name: "a", InputSchema: map[string]any{"f": func() {}}}))
	if !errors.Is(err, ErrUnusable) {
		t.Errorf("err = %v, want ErrUnusable", err)
	}
}

// A listing error stops everything: half a tool list stored would drop the
// rest from every agent granted the server.
func TestAListingErrorReturnsNothing(t *testing.T) {
	t.Parallel()

	boom := errors.New("page 2 failed")
	seq := func(yield func(*sdk.Tool, error) bool) {
		if !yield(&sdk.Tool{Name: "a"}, nil) {
			return
		}
		yield(nil, boom)
	}

	got, err := collect(seq)
	if !errors.Is(err, boom) || got != nil {
		t.Errorf("got %v, %v; want nothing and the error", got, err)
	}
}

// --- a run's client ---

func runClient(t *testing.T, sec Secrets, p Policy, row db.McpServer) *http.Client {
	t.Helper()

	c, err := New(sec, p, 5*time.Second).Client(t.Context(), row)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	return c
}

// A run reaches servers a team registered, so it gets exactly the guard
// discovery has: a link-local address is the cloud metadata service.
func TestARunsMCPClientIsRefusedWhatDiscoveryIs(t *testing.T) {
	t.Parallel()

	c := runClient(t, &fakeSecrets{}, Policy{Allow: allowAll}, urlRow("https://169.254.169.254/"))
	if _, err := get(t, c, "https://169.254.169.254/latest/meta-data/"); !errors.Is(err, ErrRefused) {
		t.Errorf("link-local: err = %v, want ErrRefused", err)
	}

	srv := newMCPServer(t, true, nil)
	p := srv.policy()
	p.Allow = allowNone
	c = runClient(t, &fakeSecrets{}, p, urlRow(srv.URL))
	if _, err := get(t, c, srv.URL); !errors.Is(err, ErrRefused) {
		t.Errorf("loopback with nothing allowed: err = %v, want ErrRefused", err)
	}
}

func TestARunsMCPClientSendsTheServersToken(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, true, nil)
	sec := &fakeSecrets{values: map[string]string{"teams/t1/mcp/github#token": "ghp_live"}}
	row := urlRow(srv.URL)
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref

	if _, err := get(t, runClient(t, sec, srv.policy(), row), srv.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.auth) != 1 || srv.auth[0] != "Bearer ghp_live" {
		t.Errorf("the server saw Authorization %q", srv.auth)
	}
}

// The token is read when a run starts, not when the brain does: a token
// rotated in the secret store reaches the next run without a restart.
func TestEachRunReadsTheTokenAfresh(t *testing.T) {
	t.Parallel()

	srv := newMCPServer(t, true, nil)
	sec := &fakeSecrets{values: map[string]string{"teams/t1/mcp/github#token": "old"}}
	row := urlRow(srv.URL)
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref
	d := New(sec, srv.policy(), 5*time.Second)

	for _, token := range []string{"old", "new"} {
		sec.mu.Lock()
		sec.values[ref] = token
		sec.mu.Unlock()

		c, err := d.Client(t.Context(), row)
		if err != nil {
			t.Fatalf("Client: %v", err)
		}
		if _, err := get(t, c, srv.URL); err != nil {
			t.Fatalf("get: %v", err)
		}
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !slices.Equal(srv.auth, []string{"Bearer old", "Bearer new"}) {
		t.Errorf("the server saw %q, want the old token and then the new one", srv.auth)
	}
}

// discovery's client is bounded by a deadline; a zero one is already past,
// so a run's client built that way would fail every call. The run's own
// context is what bounds it, and still does.
func TestARunsMCPClientHasNoDeadlineOfItsOwn(t *testing.T) {
	t.Parallel()

	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slow.Close)
	c := runClient(t, &fakeSecrets{}, Policy{Allow: allowAll, RootCAs: trusting(slow)}, urlRow(slow.URL))

	if code, err := get(t, c, slow.URL); err != nil || code != http.StatusOK {
		t.Errorf("a call that takes 300ms: %d, %v; want 200", code, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, slow.URL, http.NoBody)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if resp, err := c.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Error("a call outliving the run's context succeeded")
	}
}

// A tool call's headers arrive when the tool finishes. Discovery gives up
// after headerTimeout because listing tools should be quick; a run's calls
// wait for a tool, up to callHeaderTimeout, and never without any bound.
func TestARunsCallsWaitForASlowToolButNotForever(t *testing.T) {
	t.Parallel()

	d := New(&fakeSecrets{}, Policy{Allow: allowAll}, 5*time.Second)

	discovery, ok := d.transport.(*http.Transport)
	if !ok || discovery.ResponseHeaderTimeout != headerTimeout {
		t.Errorf("discovery's header timeout = %v, want %v", discovery.ResponseHeaderTimeout, headerTimeout)
	}
	calls, ok := d.calls.(*http.Transport)
	if !ok || calls.ResponseHeaderTimeout != callHeaderTimeout {
		t.Errorf("a run's header timeout = %v, want %v", calls.ResponseHeaderTimeout, callHeaderTimeout)
	}
	if callHeaderTimeout <= headerTimeout {
		t.Errorf("a tool may take %v, no longer than discovery's %v", callHeaderTimeout, headerTimeout)
	}
	if calls == discovery {
		t.Error("runs share discovery's transport, and with it its header timeout")
	}
}

// Every run on a worker shares one pool. A hand-built http.Transport keeps
// idle connections with no limit on count or age, so without these it holds
// every server any run ever reached for the life of the process.
func TestIdleConnectionsAreClosedAndCapped(t *testing.T) {
	t.Parallel()

	d := New(&fakeSecrets{}, Policy{Allow: allowAll}, 5*time.Second)

	for name, rt := range map[string]http.RoundTripper{"discovery": d.transport, "runs": d.calls} {
		tr, ok := rt.(*http.Transport)
		if !ok {
			t.Fatalf("%s: transport is %T", name, rt)
		}
		if tr.IdleConnTimeout != idleTimeout || tr.IdleConnTimeout == 0 {
			t.Errorf("%s: idle connections live %v, want %v", name, tr.IdleConnTimeout, idleTimeout)
		}
		if tr.MaxIdleConns != maxIdleConns || tr.MaxIdleConns == 0 {
			t.Errorf("%s: up to %d idle connections, want %d", name, tr.MaxIdleConns, maxIdleConns)
		}
	}
}

// A command server runs a process, which needs the workspace; a run's client
// for one is refused before any secret is read.
func TestACommandServerHasNoRunClient(t *testing.T) {
	t.Parallel()

	sec := &fakeSecrets{}
	row := db.McpServer{Name: "local", Command: []string{"npx", "server"}, Env: []byte("{}")}
	ref := "teams/t1/mcp/local#token"
	row.AuthSecretRef = &ref

	if _, err := New(sec, Policy{Allow: allowAll}, time.Second).Client(t.Context(), row); !errors.Is(err, ErrCommandServer) {
		t.Errorf("err = %v, want ErrCommandServer", err)
	}
	if len(sec.asked) != 0 {
		t.Errorf("read %v for a server that will not be reached", sec.asked)
	}
}

func TestAMissingTokenRefusesARunsClient(t *testing.T) {
	t.Parallel()

	row := urlRow("https://mcp.example.com")
	ref := "teams/t1/mcp/github#token"
	row.AuthSecretRef = &ref

	if _, err := New(&fakeSecrets{}, Policy{Allow: allowAll}, time.Second).Client(t.Context(), row); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// base unwraps the layers newClient adds, down to the transport underneath.
func base(rt http.RoundTripper) http.RoundTripper {
	for {
		switch l := rt.(type) {
		case bearer:
			rt = l.next
		case capped:
			rt = l.next
		case bounded:
			rt = l.next
		default:
			return rt
		}
	}
}

// The transport with no header timeout exists; this is that a run's client
// actually sits on it, with a token and without.
func TestARunsClientSitsOnTheCallsTransport(t *testing.T) {
	t.Parallel()

	sec := &fakeSecrets{values: map[string]string{"teams/t1/mcp/github#token": "ghp_live"}}
	d := New(sec, Policy{Allow: allowAll}, time.Second)

	bare := urlRow("https://mcp.example.com")
	withToken := urlRow("https://mcp.example.com")
	ref := "teams/t1/mcp/github#token"
	withToken.AuthSecretRef = &ref

	for name, row := range map[string]db.McpServer{"no token": bare, "a token": withToken} {
		c, err := d.Client(t.Context(), row)
		if err != nil {
			t.Fatalf("%s: Client: %v", name, err)
		}
		if got := base(c.Transport); got != d.calls {
			t.Errorf("%s: a run's client sits on %p, not the calls transport %p", name, got, d.calls)
		}
		if _, ok := c.Transport.(bounded); ok {
			t.Errorf("%s: a run's client has a deadline of its own", name)
		}
	}
}
