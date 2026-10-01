package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// --- reachable ---

func TestReachableIsPublicPlusTheAllowlist(t *testing.T) {
	t.Parallel()

	allow := reachable([]netip.Prefix{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("fd00::/8")})
	for addr, want := range map[string]bool{
		"8.8.8.8":         true,
		"2606:4700::1111": true,
		"10.20.3.4":       true,
		"10.21.0.1":       false,
		"192.168.1.1":     false,
		"127.0.0.1":       false,
		"fd00::1":         true,
		"fc00::1":         false,
	} {
		if got := allow(netip.MustParseAddr(addr)); got != want {
			t.Errorf("reachable(%s) = %v, want %v", addr, got, want)
		}
	}
}

// With no allowlist, nothing private is reachable — the default an operator
// gets without asking.
func TestWithNoAllowlistOnlyPublicIsReachable(t *testing.T) {
	t.Parallel()

	allow := reachable(nil)
	for _, addr := range []string{"10.0.0.1", "172.16.0.1", "192.168.0.1", "127.0.0.1", "::1", "fd00::1", "100.64.0.1"} {
		if allow(netip.MustParseAddr(addr)) {
			t.Errorf("%s is reachable with no allowlist", addr)
		}
	}
}

// The discoverer serve builds refuses what reachable refuses: the policy is
// the one it was handed, not a permissive default.
func TestTheDiscovererServeBuildsRefusesLoopback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("discovery reached a loopback server it should have refused")
	}))
	t.Cleanup(srv.Close)

	u := srv.URL
	_, err := newDiscoverer(&config.Config{}, static.New()).Discover(t.Context(), db.McpServer{Name: "x", Url: &u})
	if !errors.Is(err, discovery.ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

// The allowlist reaches the discoverer serve builds: with loopback listed,
// the same server is dialled rather than refused. (It is not an MCP server,
// so discovery still fails — just not as a refusal.)
func TestTheDiscovererServeBuildsUsesTheConfiguredAllowlist(t *testing.T) {
	t.Parallel()

	var hits sync.WaitGroup
	hits.Add(1)
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(hits.Done)
		http.Error(w, "not MCP", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{MCPPrivateNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	u := srv.URL
	_, err := newDiscoverer(cfg, static.New()).Discover(t.Context(), db.McpServer{Name: "x", Url: &u})
	if err == nil || errors.Is(err, discovery.ErrRefused) {
		t.Fatalf("err = %v, want a failure that is not a refusal", err)
	}
	hits.Wait()
}

// --- wiring ---

type recordingDiscoverer struct {
	mu    sync.Mutex
	asked []uuid.UUID
}

func (r *recordingDiscoverer) Discover(_ context.Context, s db.McpServer) ([]discovery.Tool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, s.ID)
	return []discovery.Tool{{Name: "search", Description: "Search", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}

// newRouters takes the discoverer and the store, and has to hand both on: the
// discoverer to the route, and the store through the adapter that gives the
// route its transaction. Either dropped compiles and passes every test that
// reads route patterns. So this drives a discovery through the router
// newRouters built, against real Postgres, and reads the result back.
func TestTheDiscovererServeBuildsIsTheOneTheRoutesUse(t *testing.T) {
	s, err := store.New(t.Context(), schemaDSN(t))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	team, err := s.CreateTeam(t.Context(), "platform")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	u := "https://mcp.example.com"
	row, err := s.CreateMCPServer(t.Context(), db.CreateMCPServerParams{TeamID: team.ID, Name: "github", Url: &u, Env: []byte("{}")})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}

	d := &recordingDiscoverer{}
	mux := mcpMux(t, s, d)
	user := &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: team.ID, Name: "platform", Role: "admin"}},
	}

	path := "/teams/" + team.ID.String() + "/mcp-servers/" + row.ID.String()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path+"/discover", http.NoBody)
	req = req.WithContext(auth.WithUser(req.Context(), user))
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("the discover route panicked — something newRouters was given never reached it: %v", p)
			}
		}()
		mux.ServeHTTP(rec, req)
	}()

	if rec.Code != http.StatusOK || len(d.asked) != 1 || d.asked[0] != row.ID {
		t.Fatalf("status %d (%s), discoverer asked %v; want 200 from the discoverer this test gave newRouters", rec.Code, rec.Body, d.asked)
	}

	tools, err := s.ListMCPToolsByServer(t.Context(), row.ID)
	if err != nil || len(tools) != 1 || tools[0].Name != "search" {
		t.Errorf("stored tools %+v, %v; want search, written through the adapter's transaction", tools, err)
	}
	got, err := s.GetMCPServer(t.Context(), row.ID)
	if err != nil || got.DiscoveredAt == nil {
		t.Errorf("discovered_at = %v, %v; want it set", got.DiscoveredAt, err)
	}
}

func mcpMux(t *testing.T, s *store.Store, d *recordingDiscoverer) *http.ServeMux {
	t.Helper()

	cfg := &config.Config{Environment: config.EnvironmentDevelopment}
	for _, r := range newRouters(cfg, discardLogger(), s, nil, nil, nil, nil, nil, d) {
		rr, ok := r.(registrable)
		if !ok || !strings.Contains(strings.Join(rr.Patterns(), " "), "/discover") {
			continue
		}

		routes := authz.NewRoutes()
		write := "tool:write"
		for _, p := range rr.Patterns() {
			method, path, _ := strings.Cut(p, " ")
			if err := routes.Add(method, path, "team", &write); err != nil {
				t.Fatalf("Add %s: %v", p, err)
			}
		}
		mux := http.NewServeMux()
		rr.Register(mux, api.NewGuard(routes, allowEverything{}, discardLogger()))
		return mux
	}
	t.Fatal("newRouters built no router serving /discover")
	return nil
}
