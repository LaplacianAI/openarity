package docs

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	apispec "github.com/LaplacianAI/openarity/apps/brain/api"
)

// openGuard maps every route and changes nothing. The guard's own behaviour is
// tested in internal/api; these tests are about the handler.
type openGuard struct{}

func (openGuard) Wrap(_ string, next http.HandlerFunc) (http.HandlerFunc, error) {
	return next, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func call(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger()).Register(mux, openGuard{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

// The spec is embedded, so the copy served can never drift from the committed
// one. A handler that read it from disk would serve whatever was next to the
// binary, or nothing at all in a distroless image.
func TestSpecIsServedFromTheEmbeddedCopy(t *testing.T) {
	t.Parallel()

	rec := call(t, http.MethodGet, "/openapi.yaml")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != string(apispec.Spec) {
		t.Error("the served document is not the embedded one")
	}
}

// An empty embed compiles and serves nothing, which would leave the contract
// test comparing against a document that does not exist.
func TestTheEmbeddedSpecIsNotEmpty(t *testing.T) {
	t.Parallel()

	if len(apispec.Spec) == 0 {
		t.Fatal("api/openapi.yaml embedded as zero bytes")
	}
	if !strings.HasPrefix(string(apispec.Spec), "openapi:") {
		t.Errorf("the embedded file does not begin like an OpenAPI document: %.40q", apispec.Spec)
	}
}

// The content type is what makes a browser and a client generator read it as a
// document rather than download it.
func TestSpecCarriesTheYAMLContentType(t *testing.T) {
	t.Parallel()

	rec := call(t, http.MethodGet, "/openapi.yaml")

	if ct := rec.Result().Header.Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
}

func TestDocsServeHTML(t *testing.T) {
	t.Parallel()

	rec := call(t, http.MethodGet, "/docs")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "<rapi-doc") {
		t.Error("the page carries no renderer")
	}
}

// The page points at the route this package actually serves. A mismatch loads
// an empty viewer, which looks like a broken spec rather than a broken link.
func TestDocsPointAtTheSpecRoute(t *testing.T) {
	t.Parallel()

	body := call(t, http.MethodGet, "/docs").Body.String()

	if !strings.Contains(body, `spec-url="/openapi.yaml"`) {
		t.Error("the page does not load /openapi.yaml")
	}
	if call(t, http.MethodGet, "/openapi.yaml").Code != http.StatusOK {
		t.Error("the page points at a route that does not answer")
	}
}

// "Try it" has to call the origin the page came from. The spec names an
// absolute address, and any other route to the stack — a LAN address, a port
// forward, a tunnel — makes that a cross-origin request the brain sends no
// CORS headers for. The browser then reports a bare network failure.
func TestDocsCallTheOriginTheyWereLoadedFrom(t *testing.T) {
	t.Parallel()

	body := call(t, http.MethodGet, "/docs").Body.String()

	if !strings.Contains(body, `server-url="/"`) {
		t.Error(`the page does not set server-url="/", so "try it" is cross-origin whenever the host differs from the spec`)
	}
	if !strings.Contains(body, `allow-server-selection="false"`) {
		t.Error("server selection is enabled, so a reader can pick the absolute address and hit CORS again")
	}
}

// The renderer comes from a CDN, which is a third-party script on a page
// someone pastes a bearer token into. Pinning plus an integrity hash is what
// makes that acceptable: a compromised CDN cannot substitute the bundle, and a
// breaking release cannot arrive overnight.
func TestTheRendererIsPinnedAndIntegrityChecked(t *testing.T) {
	t.Parallel()

	body := call(t, http.MethodGet, "/docs").Body.String()

	script := regexp.MustCompile(`src="([^"]+)"`).FindStringSubmatch(body)
	if script == nil {
		t.Fatal("the page loads no script")
	}

	src := script[1]
	if !strings.HasPrefix(src, "https://") {
		t.Errorf("the renderer is loaded over %q, not https", src)
	}
	if !regexp.MustCompile(`@\d+\.\d+\.\d+/`).MatchString(src) {
		t.Errorf("the renderer is not pinned to a version: %s", src)
	}
	if strings.Contains(src, "latest") {
		t.Errorf("the renderer tracks latest: %s", src)
	}

	if !strings.Contains(body, `integrity="sha384-`) {
		t.Error("the script tag carries no integrity hash, so a compromised CDN can replace it")
	}
	if !strings.Contains(body, `crossorigin="anonymous"`) {
		t.Error("integrity is ignored without crossorigin on a cross-origin script")
	}
}

// The whole point of this router is that a browser can reach it without a
// token. If it were protected, a navigation would 401 and the page would never
// load.
func TestTheDocsRouterIsPublic(t *testing.T) {
	t.Parallel()

	if !New(discardLogger()).Public() {
		t.Error("the docs router is not public, so a browser cannot open it")
	}
}

// Being public is exactly why the surface has to stay small. Anything added
// here is served to anyone who can reach the port.
func TestOnlyTwoRoutesArePublic(t *testing.T) {
	t.Parallel()

	got := New(discardLogger()).Patterns()
	want := map[string]bool{"GET /openapi.yaml": true, "GET /docs": true}

	if len(got) != len(want) {
		t.Fatalf("Patterns() = %v, want exactly %v", got, want)
	}
	for _, route := range got {
		if !want[route] {
			t.Errorf("%s is served without authentication", route)
		}
	}
}

// Neither route writes, so nothing else should answer. A POST that fell
// through to a handler would be an unauthenticated write.
func TestUndeclaredMethodsDoNotAnswer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/docs"},
		{http.MethodPut, "/docs"},
		{http.MethodDelete, "/openapi.yaml"},
		{http.MethodPost, "/openapi.yaml"},
	} {
		rec := call(t, tc.method, tc.path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", tc.method, tc.path, rec.Code)
		}
	}
}

// The spec names every endpoint and its authorisation rules. It must not name
// a credential as well — a default token or an example password in the
// document would be served to anyone who can reach /openapi.yaml.
func TestTheSpecCarriesNoCredentials(t *testing.T) {
	t.Parallel()

	body := strings.ToLower(string(apispec.Spec))

	for _, secret := range []string{"letmein", "password:", "postgres://", "secret_key", "bearer ey"} {
		if strings.Contains(body, secret) {
			t.Errorf("the spec contains %q", secret)
		}
	}
}
