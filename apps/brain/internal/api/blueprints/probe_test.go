package blueprints

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeDiscoverer records what it was asked, so a refusal can be shown to
// have sent nothing.
type fakeDiscoverer struct {
	mu    sync.Mutex
	asked []db.McpServer
	err   error
}

func (f *fakeDiscoverer) Discover(_ context.Context, s db.McpServer) ([]discovery.Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, s)
	if f.err != nil {
		return nil, f.err
	}
	return []discovery.Tool{{Name: "refund", Description: "Refund a charge"}}, nil
}

func (f *fakeDiscoverer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func ptr(s string) *string { return &s }

func TestTheBuilderMayAskAServerWithNoSecretForItsTools(t *testing.T) {
	t.Parallel()
	w := newWorld(t)

	tools, err := NewWorkshop(w, &w.probe, loops).ToolsOf(t.Context(), w.team, "https://billing.example/mcp", nil)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "refund" {
		t.Errorf("tools = %+v", tools)
	}
	got := w.probe.asked[0]
	if got.Url == nil || *got.Url != "https://billing.example/mcp" || got.AuthSecretRef != nil || got.TeamID != w.team {
		t.Errorf("asked %+v; want the url, the team, and no secret", got)
	}
}

// The guard that matters: a token goes only where the team already sends it.
func TestASecretIsSentOnlyWhereTheTeamAlreadySendsIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if rec := w.apply(t, supportTeam()); rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	ws := NewWorkshop(w, &w.probe, loops)

	if _, err := ws.ToolsOf(t.Context(), w.team, "https://helpdesk.example/mcp", ptr("helpdesk#token")); err != nil {
		t.Fatalf("the team's own server, with its own secret: %v", err)
	}
	if ref := w.probe.asked[0].AuthSecretRef; ref == nil || *ref != "teams/"+w.team.String()+"/mcp/helpdesk#token" {
		t.Errorf("sent %v, want the team's own reference", ref)
	}

	for name, tc := range map[string]struct{ url, ref string }{
		"another host":         {"https://attacker.example/mcp", "helpdesk#token"},
		"the same host's path": {"https://helpdesk.example/other", "helpdesk#token"},
		"another secret":       {"https://helpdesk.example/mcp", "billing#token"},
		"another team's":       {"https://helpdesk.example/mcp", "teams/00000000-0000-0000-0000-000000000001/mcp/helpdesk#token"},
	} {
		before := w.probe.count()
		_, err := ws.ToolsOf(t.Context(), w.team, tc.url, &tc.ref)
		if !errors.Is(err, ErrProbe) {
			t.Errorf("%s: %v, want ErrProbe", name, err)
		}
		if w.probe.count() != before {
			t.Errorf("%s: the server was asked, with the secret", name)
		}
	}
}

func TestAnAddressThatIsNotAServersIsRefusedBeforeItIsAsked(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ws := NewWorkshop(w, &w.probe, loops)

	for _, url := range []string{"", "billing.example/mcp", "ftp://billing.example/mcp"} {
		if _, err := ws.ToolsOf(t.Context(), w.team, url, nil); !errors.Is(err, ErrProbe) {
			t.Errorf("%q: %v, want ErrProbe", url, err)
		}
	}
	if n := w.probe.count(); n != 0 {
		t.Errorf("asked %d servers", n)
	}
}

func TestAServerThatCannotBeAskedSaysWhyWithoutTheDetail(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.probe.err = fmt.Errorf("%w: dial 10.0.0.7:443 is private", discovery.ErrRefused)

	_, err := NewWorkshop(w, &w.probe, loops).ToolsOf(t.Context(), w.team, "https://intranet.example/mcp", nil)
	if !errors.Is(err, ErrProbe) || !strings.Contains(err.Error(), "may not reach") || strings.Contains(err.Error(), "10.0.0.7") {
		t.Errorf("%v; want the reason, not the address", err)
	}
	for _, e := range []error{context.DeadlineExceeded, discovery.ErrUnusable, errors.New("401")} {
		w.probe.err = e
		if _, err := NewWorkshop(w, &w.probe, loops).ToolsOf(t.Context(), w.team, "https://x.example/mcp", nil); !errors.Is(err, ErrProbe) {
			t.Errorf("%v: %v, want ErrProbe", e, err)
		}
	}
}

func TestABrainThatDiscoversNothingCannotBeAsked(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := NewWorkshop(w, nil, loops).ToolsOf(t.Context(), w.team, "https://x.example/mcp", nil); !errors.Is(err, ErrProbe) {
		t.Errorf("%v, want ErrProbe", err)
	}
}
