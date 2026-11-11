package blueprints

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/mcpservers"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeRefresher answers for each server by name: its tools, or its error.
type fakeRefresher struct {
	mu    sync.Mutex
	seen  []db.McpServer
	tools map[string][]string
	errs  map[string]error
}

func (f *fakeRefresher) Refresh(_ context.Context, row db.McpServer) ([]discovery.Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, row)
	if err := f.errs[row.Name]; err != nil {
		return nil, err
	}
	var out []discovery.Tool
	for _, n := range f.tools[row.Name] {
		out = append(out, discovery.Tool{Name: n})
	}
	return out, nil
}

func (f *fakeRefresher) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.seen))
	for i, r := range f.seen {
		out[i] = r.Name
	}
	slices.Sort(out)
	return out
}

func withServers(bp Blueprint, names ...string) Blueprint {
	for _, n := range names {
		url := "https://" + n + ".example/mcp"
		bp.MCPServers = append(bp.MCPServers, serverSpec{Name: n, URL: &url})
	}
	return bp
}

func appliedOf(t *testing.T, body []byte) applied {
	t.Helper()
	var out applied
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("applied: %v", err)
	}
	return out
}

func discoveredBy(out applied) map[string]Discovered {
	m := map[string]Discovered{}
	for _, d := range out.Discovered {
		m[d.Server] = d
	}
	return m
}

func TestAnApplyAsksEveryServerItWroteForItsTools(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.refresh.tools = map[string][]string{"helpdesk": {"create_ticket", "close_ticket"}, "billing": {"refund"}}

	rec := w.apply(t, withServers(supportTeam(), "billing"))
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	out := appliedOf(t, rec.Body.Bytes())
	got := discoveredBy(out)
	if d := got["helpdesk"]; !slices.Equal(d.Tools, []string{"create_ticket", "close_ticket"}) || d.Error != "" {
		t.Errorf("helpdesk: %+v", d)
	}
	if d := got["billing"]; !slices.Equal(d.Tools, []string{"refund"}) || d.Error != "" {
		t.Errorf("billing: %+v", d)
	}

	// Each server is asked as it was committed: the row a discover records
	// against is the one the apply made.
	ids := map[string]uuid.UUID{}
	for _, c := range out.Changes {
		if c.Kind == KindMCPServer {
			ids[c.Name] = *c.ID
		}
	}
	for _, row := range w.refresh.seen {
		if row.ID != ids[row.Name] || row.UpdatedAt.IsZero() {
			t.Errorf("%s was discovered as %v, want the written row %v", row.Name, row.ID, ids[row.Name])
		}
	}
}

func TestAServerThatCannotBeDiscoveredIsReportedAndTheApplyStands(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.refresh.tools = map[string][]string{"billing": {"refund"}}
	w.refresh.errs = map[string]error{
		"helpdesk": &mcpservers.Failure{Message: "auth_secret_ref names a secret that is not in the secret store, or is empty", Status: 422},
		"crm":      errors.New("dial tcp 10.0.0.7:5432: secret internal detail"),
		"notes":    mcpservers.ErrChanged,
	}

	rec := w.apply(t, withServers(supportTeam(), "billing", "crm", "notes"))
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	got := discoveredBy(appliedOf(t, rec.Body.Bytes()))

	if d := got["helpdesk"]; d.Error == "" || len(d.Tools) != 0 || d.Tools == nil {
		t.Errorf("helpdesk: %+v; want its reason and an empty list", d)
	}
	if d := got["crm"]; d.Error != "the brain could not discover it; discover it again" {
		t.Errorf("crm: %q; the brain's own error must not reach the caller", d.Error)
	}
	if d := got["notes"]; d.Error == "" {
		t.Errorf("notes: %+v; want discover it again", d)
	}
	if d := got["billing"]; !slices.Equal(d.Tools, []string{"refund"}) {
		t.Errorf("billing: %+v; one failure must not cost another its tools", d)
	}
	if n := w.written(t)["mcp_servers"]; n != 4 {
		t.Errorf("%d servers written, want all 4 kept", n)
	}
}

func TestAServerTheApplyLeftAloneIsNotAskedAgain(t *testing.T) {
	t.Parallel()
	w := newWorld(t)

	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("first apply: %d %s", rec.Code, rec.Body)
	}
	w.refresh.mu.Lock()
	w.refresh.seen = nil
	w.refresh.mu.Unlock()

	rec := w.apply(t, supportTeam())
	if rec.Code != http.StatusOK {
		t.Fatalf("second apply: %d %s", rec.Code, rec.Body)
	}
	if got := w.refresh.names(); len(got) != 0 {
		t.Errorf("asked %v again, though nothing changed", got)
	}
	if out := appliedOf(t, rec.Body.Bytes()); out.Discovered == nil {
		t.Error("discovered is null; want an empty list")
	}
}

func TestARefusedApplyAsksNoServer(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.authz.holds["agent:write"] = false

	rec := w.apply(t, supportTeam())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("apply: %d %s, want 422", rec.Code, rec.Body)
	}
	if got := w.refresh.names(); len(got) != 0 {
		t.Errorf("asked %v for a plan that was refused", got)
	}
}

func TestAnApplyThatFailsAsksNoServer(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.failOn = "delegates"

	if rec := w.apply(t, supportTeam()); rec.Code == http.StatusOK {
		t.Fatalf("apply: %d, want a failure", rec.Code)
	}
	if got := w.refresh.names(); len(got) != 0 {
		t.Errorf("asked %v for servers that were rolled back", got)
	}
}
