package mcpservers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discovered(names ...string) []discovery.Tool {
	out := make([]discovery.Tool, len(names))
	for i, n := range names {
		out[i] = discovery.Tool{Name: n, Description: "Does " + n, InputSchema: json.RawMessage(`{"type":"object"}`)}
	}
	return out
}

func storedNames(s *fakeStore, id uuid.UUID) []string {
	var out []string
	for _, t := range s.tools[id] {
		out = append(out, t.Name)
	}
	slices.Sort(out)
	return out
}

func discoverWith(t *testing.T, s *fakeStore, d *fakeDiscoverer, row db.McpServer) (int, string) {
	t.Helper()

	d.store = s
	rec := callWith(t, s, d, discardLogger(), &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, discoverOf(row.TeamID, row.ID), nil)
	return rec.Code, rec.Body.String()
}

// One discovery adds what is new, rewrites what changed and removes what the
// server no longer offers — and the reply is what was found.
func TestDiscoveryAddsChangesAndRemovesTools(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	s.tools[row.ID] = []db.McpTool{
		{McpServerID: row.ID, Name: "search", Description: "old", InputSchema: []byte(`{"type":"object"}`)},
		{McpServerID: row.ID, Name: "retired", Description: "gone", InputSchema: []byte(`{"type":"object"}`)},
	}

	code, body := discoverWith(t, s, &fakeDiscoverer{tools: discovered("search", "create_issue")}, row)
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s)", code, body)
	}

	if got, want := storedNames(s, row.ID), []string{"create_issue", "search"}; !slices.Equal(got, want) {
		t.Errorf("stored %v, want %v", got, want)
	}
	for _, tool := range s.tools[row.ID] {
		if tool.Name == "search" && tool.Description != "Does search" {
			t.Errorf("search's description is %q, want it rewritten", tool.Description)
		}
		if tool.TeamID != row.TeamID {
			t.Errorf("%s stored in team %s, want %s", tool.Name, tool.TeamID, row.TeamID)
		}
	}
	if s.servers[row.ID].DiscoveredAt == nil {
		t.Error("discovered_at was not set")
	}

	var got toolList
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body: %v (%s)", err, body)
	}
	if len(got.Items) != 2 || got.Items[0].Name != "search" || got.Items[1].Name != "create_issue" ||
		string(got.Items[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("reply = %+v, want the two tools in the order found", got.Items)
	}
}

// A server that now offers nothing loses every tool, and says so with [].
func TestDiscoveringNothingRemovesEverything(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	s.tools[row.ID] = []db.McpTool{{McpServerID: row.ID, Name: "search"}}

	code, body := discoverWith(t, s, &fakeDiscoverer{}, row)
	if code != http.StatusOK || body != "{\"items\":[]}\n" {
		t.Errorf("status %d, body %q; want 200 and []", code, body)
	}
	if len(s.tools[row.ID]) != 0 {
		t.Errorf("still stored %v", storedNames(s, row.ID))
	}
}

// Discovery is a network call of up to 15 seconds. It runs before the
// transaction opens, so no pooled connection is held while a server stalls.
func TestDiscoveryRunsOutsideTheTransaction(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	d := &fakeDiscoverer{tools: discovered("search")}
	discoverWith(t, s, d, row)

	if len(d.calls) != 1 {
		t.Fatalf("Discover was called %d times", len(d.calls))
	}
	if d.inTx {
		t.Error("Discover ran inside the transaction")
	}
	if d.calls[0].ID != row.ID || *d.calls[0].Url != *row.Url {
		t.Errorf("Discover was given %+v, want the stored row", d.calls[0])
	}
}

// Every failure of discovery leaves the tools exactly as they were: an agent
// granted this server keeps what it had.
func TestAFailedDiscoveryLeavesThePreviousTools(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"refused":    fmt.Errorf("connecting: %w: 10.0.0.5:443 is not an address discovery may reach", discovery.ErrRefused),
		"unusable":   fmt.Errorf("%w: search is offered twice", discovery.ErrUnusable),
		"timed out":  fmt.Errorf("connecting: %w", context.DeadlineExceeded),
		"no secret":  fmt.Errorf("reading the server's token: %w", secrets.ErrNotFound),
		"store down": fmt.Errorf("reading the server's token: %w", secrets.ErrUnavailable),
		"not MCP":    errors.New("connecting: unexpected status 404"),
		"command":    discovery.ErrCommandServer,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			row := aURLServer(uuid.New(), "github")
			s := newFakeStore(row)
			s.tools[row.ID] = []db.McpTool{{McpServerID: row.ID, Name: "kept"}}

			code, _ := discoverWith(t, s, &fakeDiscoverer{err: err}, row)
			if code == http.StatusOK {
				t.Fatal("a failed discovery answered 200")
			}
			if got := storedNames(s, row.ID); !slices.Equal(got, []string{"kept"}) {
				t.Errorf("tools are now %v, want [kept]", got)
			}
			if len(s.marked) != 0 || s.committed != 0 || s.servers[row.ID].DiscoveredAt != nil {
				t.Error("a failed discovery reached the transaction")
			}
		})
	}
}

// Each failure is answered by whose it is. The server's address and the
// brain's own errors never reach the body.
func TestEachDiscoveryFailureHasItsStatus(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		code int
		says string
	}{
		"a command server": {discovery.ErrCommandServer, http.StatusConflict, "sandbox"},
		"a refused address": {
			fmt.Errorf("connecting: Post \"https://vault.corp\": %w: 10.2.0.7:443 is not an address discovery may reach", discovery.ErrRefused),
			http.StatusUnprocessableEntity, "may not reach",
		},
		"unusable content":  {fmt.Errorf("%w: search is offered twice", discovery.ErrUnusable), http.StatusUnprocessableEntity, "search is offered twice"},
		"a missing secret":  {fmt.Errorf("reading the server's token: %w", secrets.ErrNotFound), http.StatusUnprocessableEntity, "auth_secret_ref"},
		"the deadline":      {fmt.Errorf("listing tools: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, "in time"},
		"not an MCP server": {errors.New("connecting: broken pipe"), http.StatusBadGateway, "did not answer as an MCP server"},
		"the store is down": {fmt.Errorf("reading the server's token: %w", secrets.ErrUnavailable), http.StatusInternalServerError, "internal server error"},
		"deadline and refusal": {
			fmt.Errorf("%w: %w", context.DeadlineExceeded, discovery.ErrRefused), http.StatusGatewayTimeout, "in time",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			row := aURLServer(uuid.New(), "github")
			code, body := discoverWith(t, newFakeStore(row), &fakeDiscoverer{err: tc.err}, row)
			if code != tc.code || !strings.Contains(body, tc.says) {
				t.Errorf("status %d, body %q; want %d saying %q", code, body, tc.code, tc.says)
			}
			for _, leak := range []string{"10.2.0.7", "vault.corp", "broken pipe", "secret not found", "unavailable"} {
				if strings.Contains(body, leak) {
					t.Errorf("the reply leaks %q: %s", leak, body)
				}
			}
		})
	}
}

// The detail a caller is not told is what an operator needs, so it goes to
// the log — the resolved address of a refusal most of all.
func TestARefusalIsLoggedWithTheDetailTheCallerIsNotGiven(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	d := &fakeDiscoverer{store: s, err: fmt.Errorf("%w: 10.2.0.7:443 is not an address discovery may reach", discovery.ErrRefused)}
	callWith(t, s, d, logger, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, discoverOf(row.TeamID, row.ID), nil)

	if !strings.Contains(logs.String(), "10.2.0.7") || !strings.Contains(logs.String(), row.ID.String()) {
		t.Errorf("log = %s, want the address and the server", logs.String())
	}
}

// A server edited while discovery talked to the old one must not be marked
// discovered with the old one's tools. The versioned mark matches nothing, the
// transaction rolls back, and the caller is told to try again.
func TestAServerEditedDuringDiscoveryKeepsItsToolsAndIsAConflict(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	s.tools[row.ID] = []db.McpTool{{McpServerID: row.ID, Name: "kept"}}

	d := &fakeDiscoverer{tools: discovered("from_the_old_url"), during: func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		edited := s.servers[row.ID]
		elsewhere := "https://elsewhere.example.com"
		edited.Url, edited.UpdatedAt = &elsewhere, edited.UpdatedAt.Add(time.Millisecond)
		s.servers[row.ID] = edited
	}}

	code, body := discoverWith(t, s, d, row)
	if code != http.StatusConflict || !strings.Contains(body, "discover it again") {
		t.Errorf("status %d, body %q; want 409 asking to retry", code, body)
	}
	if got := storedNames(s, row.ID); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("tools are now %v, want [kept]", got)
	}
	if s.servers[row.ID].DiscoveredAt != nil {
		t.Error("the edited server was marked discovered")
	}
	if len(s.marked) != 1 || !s.marked[0].UpdatedAt.Equal(row.UpdatedAt) {
		t.Errorf("marked %+v, want the version that was read", s.marked)
	}
}

// A write that fails part-way rolls the whole discovery back: no half list,
// no mark. It is ours, so a 500 that says nothing.
func TestAFailedWriteRecordsNothing(t *testing.T) {
	t.Parallel()

	for name, breakIt := range map[string]func(*fakeStore){
		"the mark":  func(s *fakeStore) { s.markErr = errAny },
		"an upsert": func(s *fakeStore) { s.upsertErr = errAny },
		"the prune": func(s *fakeStore) { s.pruneErr = errAny },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			row := aURLServer(uuid.New(), "github")
			s := newFakeStore(row)
			s.tools[row.ID] = []db.McpTool{{McpServerID: row.ID, Name: "kept"}}
			breakIt(s)

			code, body := discoverWith(t, s, &fakeDiscoverer{tools: discovered("new")}, row)
			if code != http.StatusInternalServerError || strings.Contains(body, "connection reset") {
				t.Errorf("status %d, body %q; want a 500 that leaks nothing", code, body)
			}
			if got := storedNames(s, row.ID); !slices.Equal(got, []string{"kept"}) || s.servers[row.ID].DiscoveredAt != nil {
				t.Errorf("tools %v, discovered_at %v; want nothing recorded", got, s.servers[row.ID].DiscoveredAt)
			}
		})
	}
}

func TestACommandServerIsAConflict(t *testing.T) {
	t.Parallel()

	row := aCommandServer(uuid.New(), "fs")
	s := newFakeStore(row)
	code, body := discoverWith(t, s, &fakeDiscoverer{err: discovery.ErrCommandServer}, row)

	if code != http.StatusConflict || !strings.Contains(body, "sandbox") {
		t.Errorf("status %d, body %q; want 409 naming the sandbox", code, body)
	}
}

// Another team's server is not found, and discovery never runs: running it
// would dial an address this caller could not even read.
func TestAnotherTeamsServerCannotBeDiscovered(t *testing.T) {
	t.Parallel()

	theirs := aURLServer(uuid.New(), "github")
	mine := uuid.New()
	s := newFakeStore(theirs)
	d := &fakeDiscoverer{store: s, tools: discovered("x")}

	rec := callWith(t, s, d, discardLogger(), &fakeAuthz{allowed: true}, memberOf(mine), http.MethodPost, discoverOf(mine, theirs.ID), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if len(d.calls) != 0 || s.wrote() {
		t.Error("another team's server was discovered")
	}
}

// The body is ignored rather than refused: discover takes no input, and a
// client sending {} should not have to learn that.
func TestDiscoverTakesNoBody(t *testing.T) {
	t.Parallel()

	row := aURLServer(uuid.New(), "github")
	s := newFakeStore(row)
	d := &fakeDiscoverer{store: s, tools: discovered("search")}
	rec := callWith(t, s, d, discardLogger(), &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, discoverOf(row.TeamID, row.ID), "{}")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d (%s)", rec.Code, rec.Body)
	}
}
