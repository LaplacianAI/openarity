package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// The three registry resolvers share one loop. Each is driven here across a
// page boundary, an empty name, an id, a miss and an endless cursor, so a
// resolver wired to the wrong list, or the loop losing a page, fails here.
func TestTheRegistryResolversFollowEveryPage(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	want := uuid.New()

	for _, tc := range []struct {
		kind, path, hint string
		item             func(name string, id uuid.UUID) string
	}{
		{
			"skill", "/skills", "oa skills list",
			func(name string, id uuid.UUID) string {
				return `{"id":"` + id.String() + `","team_id":"` + team.String() + `","name":"` + name +
					`","description":"d","source":"upload","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`
			},
		},
		{
			"agent", "/agents", "oa agents list",
			func(name string, id uuid.UUID) string {
				return `{"id":"` + id.String() + `","team_id":"` + team.String() + `","name":"` + name +
					`","description":"d","kind":"custom","model":{"name":"m","max_tokens":1},"pattern":"react",` +
					`"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`
			},
		},
		{
			"MCP server", "/mcp-servers", "oa mcp-servers list",
			func(name string, id uuid.UUID) string {
				return `{"id":"` + id.String() + `","team_id":"` + team.String() + `","name":"` + name +
					`","url":"https://a.b","env":{},"bare":false,"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`
			},
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()

			var asked atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("GET /teams/{id}"+tc.path, func(w http.ResponseWriter, r *http.Request) {
				asked.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if r.PathValue("id") != team.String() {
					http.Error(w, "wrong team", http.StatusBadRequest)
					return
				}
				switch r.URL.Query().Get("cursor") {
				case "":
					_, _ = w.Write([]byte(`{"items":[` + tc.item("first", uuid.New()) + `],"next_cursor":"p2"}`))
				case "p2":
					_, _ = w.Write([]byte(`{"items":[` + tc.item("wanted", want) + `]}`))
				default:
					http.Error(w, "no such page", http.StatusBadRequest)
				}
			})
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)

			api, err := NewClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			resolve := map[string]func(string) (uuid.UUID, error){
				"skill":      func(ref string) (uuid.UUID, error) { return ResolveSkill(t.Context(), api, team, ref) },
				"agent":      func(ref string) (uuid.UUID, error) { return ResolveAgent(t.Context(), api, team, ref) },
				"MCP server": func(ref string) (uuid.UUID, error) { return ResolveMCPServer(t.Context(), api, team, ref) },
			}[tc.kind]

			got, err := resolve(" wanted ")
			if err != nil || got != want {
				t.Fatalf("resolve(wanted) = %v, %v; want %v from the second page", got, err, want)
			}

			before := asked.Load()
			if got, err := resolve(want.String()); err != nil || got != want || asked.Load() != before {
				t.Errorf("an id was looked up, or changed: %v %v", got, err)
			}
			if _, err := resolve("  "); err == nil || asked.Load() != before {
				t.Error("an empty name was accepted or sent")
			}

			_, err = resolve("nobody")
			if err == nil || !strings.Contains(err.Error(), `no `+tc.kind+` named "nobody"`) || !strings.Contains(err.Error(), tc.hint) {
				t.Errorf("a miss said: %v", err)
			}
		})
	}
}

// Refused writes say which permission they needed. Anything else passes
// through untouched, so a 404 is never dressed up as a permission problem.
func TestNeedsNamesThePermissionOnlyForARefusal(t *testing.T) {
	t.Parallel()

	refused := Needs(APIError(http.StatusForbidden, nil), "skill:write")
	if !errors.Is(refused, ErrForbidden) || !strings.Contains(refused.Error(), "skill:write") {
		t.Errorf("refused = %v", refused)
	}

	for _, status := range []int{http.StatusNotFound, http.StatusConflict, http.StatusUnauthorized, http.StatusInternalServerError} {
		err := APIError(status, []byte(`{"error":"something"}`))
		if got := Needs(err, "skill:write"); !errors.Is(got, err) || strings.Contains(got.Error(), "skill:write") {
			t.Errorf("%d became %v", status, got)
		}
	}
	if Needs(nil, "skill:write") != nil {
		t.Error("no error became one")
	}
}
