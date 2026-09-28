package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
)

// recordingImporter refuses every source, and remembers that it was asked.
type recordingImporter struct {
	mu      sync.Mutex
	sources []string
}

func (r *recordingImporter) Fetch(_ context.Context, source string) (hub.Import, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, source)
	return hub.Import{}, fmt.Errorf("%w: refused by the test", hub.ErrInvalid)
}

type allowEverything struct{}

func (allowEverything) IsSuperAdmin(*auth.User) bool { return false }

func (allowEverything) Can(context.Context, *auth.User, authz.Action, authz.Resource) (bool, error) {
	return true, nil
}

func (allowEverything) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	return true, nil
}

type registrable interface {
	Patterns() []string
	Register(*http.ServeMux, api.RouteGuard)
}

// skillsMux mounts whichever router newRouters built for skills behind a real
// guard that allows everything, so a request reaches the handler exactly as
// it would in serve.
func skillsMux(t *testing.T, importer *recordingImporter) *http.ServeMux {
	t.Helper()

	cfg := &config.Config{Environment: config.EnvironmentDevelopment}
	for _, r := range newRouters(cfg, discardLogger(), nil, nil, nil, nil, nil, importer) {
		rr, ok := r.(registrable)
		if !ok || !strings.Contains(strings.Join(rr.Patterns(), " "), "/skills/import") {
			continue
		}

		routes := authz.NewRoutes()
		write := "skill:write"
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
	t.Fatal("newRouters built no router serving /skills/import")
	return nil
}

// newRouters takes the importer and has to hand it on. A parameter that is
// accepted and dropped compiles, lints clean, and satisfies every test that
// only reads route patterns — and the route then panics on its first call.
// Only a request driven through the wired router can tell.
func TestTheImporterServeBuildsIsTheOneTheRoutesUse(t *testing.T) {
	t.Parallel()

	importer := &recordingImporter{}
	mux := skillsMux(t, importer)
	teamID := uuid.New()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/teams/"+teamID.String()+"/skills/import", strings.NewReader(`{"source":"github:acme/skills/pdf"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: teamID, Name: "platform", Role: "admin"}},
	}))

	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("the import route panicked — the importer never reached it: %v", p)
			}
		}()
		mux.ServeHTTP(rec, req)
	}()

	if rec.Code != http.StatusBadRequest || len(importer.sources) != 1 {
		t.Errorf("status %d, importer asked %q; want 400 from the importer this test gave newRouters", rec.Code, importer.sources)
	}
}

// --- newImporter ---

func importConfig(hosts []string, ref string) *config.Config {
	return &config.Config{SkillImportHosts: hosts, SkillImportGitHubTokenRef: ref}
}

// With no reference there is no token: GitHub is asked anonymously, and
// nothing reads the secret store for one.
func TestWithoutATokenRefTheImporterHasNoToken(t *testing.T) {
	t.Parallel()

	h, err := newImporter(t.Context(), importConfig(nil, ""), static.New())
	if err != nil {
		t.Fatalf("newImporter: %v", err)
	}
	if h.Token != nil {
		t.Error("an importer with no token ref has a token function")
	}
	if h.GitHub.API != hub.GitHubAPI {
		t.Errorf("GitHub API = %q, want %q", h.GitHub.API, hub.GitHubAPI)
	}
}

// A reference that names nothing fails serve, naming the reference, rather
// than the first private import answering "no such repository".
func TestATokenRefThatCannotBeReadFailsTheBoot(t *testing.T) {
	t.Parallel()

	_, err := newImporter(t.Context(), importConfig(nil, "platform/github#token"), static.New())
	if err == nil || !strings.Contains(err.Error(), "SKILL_IMPORT_GITHUB_TOKEN_REF") || !strings.Contains(err.Error(), "platform/github#token") {
		t.Errorf("err = %v, want it to name the variable and the reference", err)
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("err = %v, want the store's own error kept", err)
	}
}

// The reference splits at the #: the path is where the secret lives and the
// key is which field of it. Swapped, it reads a secret that does not exist.
// And the token is read on every call, so a rotated one is used next time.
func TestTheTokenIsReadFromTheReferenceOnEveryCall(t *testing.T) {
	t.Parallel()

	store := static.New()
	writer, ok := store.(secrets.Writer)
	if !ok {
		t.Fatalf("%T cannot write", store)
	}
	if err := writer.Put(t.Context(), "platform/github", "token", "first"); err != nil {
		t.Fatal(err)
	}

	h, err := newImporter(t.Context(), importConfig(nil, "platform/github#token"), store)
	if err != nil {
		t.Fatalf("newImporter: %v", err)
	}
	if got, err := h.Token(t.Context()); got != "first" || err != nil {
		t.Errorf("Token = %q, %v; want first", got, err)
	}

	if err := writer.Put(t.Context(), "platform/github", "token", "rotated"); err != nil {
		t.Fatal(err)
	}
	if got, err := h.Token(t.Context()); got != "rotated" || err != nil {
		t.Errorf("after rotation Token = %q, %v; want rotated", got, err)
	}
}

// Two clients, so neither reaches the other's hosts: listing a zip host
// never lets a GitHub fetch be redirected there, and GitHub is not a zip
// host unless the operator says so.
func TestEachClientReachesOnlyItsOwnHosts(t *testing.T) {
	t.Parallel()

	h, err := newImporter(t.Context(), importConfig([]string{"hub.example.com"}, ""), static.New())
	if err != nil {
		t.Fatalf("newImporter: %v", err)
	}

	for name, tc := range map[string]struct {
		client *http.Client
		url    string
	}{
		"GitHub to a zip host":  {h.GitHub.Client, "https://hub.example.com/pdf.zip"},
		"a zip from GitHub":     {h.Zips.Client, "https://api.github.com/repos/acme/skills/tarball/main"},
		"a zip from codeload":   {h.Zips.Client, "https://codeload.github.com/acme/skills/legacy.tar.gz/main"},
		"GitHub, not over TLS":  {h.GitHub.Client, "http://api.github.com/repos/acme/skills"},
		"a zip host, not TLS":   {h.Zips.Client, "http://hub.example.com/pdf.zip"},
		"an address off either": {h.Zips.Client, "https://evil.example.com/pdf.zip"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := tc.client.Do(req)
		if res != nil {
			_ = res.Body.Close()
		}
		if !errors.Is(err, hub.ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused before any connection", name, err)
		}
	}
}
