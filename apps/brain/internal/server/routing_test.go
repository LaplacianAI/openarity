package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

// stubRouter registers one route and records what it saw on the context, which
// is how these tests prove the chain ran in the right order.
type stubRouter struct {
	pattern string
	ran     *bool
	sawUser **auth.User
}

func (r stubRouter) Register(mux *http.ServeMux) {
	mux.HandleFunc(r.pattern, func(w http.ResponseWriter, req *http.Request) {
		if r.ran != nil {
			*r.ran = true
		}
		if r.sawUser != nil {
			u, _ := auth.UserFrom(req.Context())
			*r.sawUser = u
		}
		w.WriteHeader(http.StatusOK)
	})
}

// A router that is accepted and never mounted is the failure this whole seam
// exists to avoid: it compiles, no linter complains, and the route 404s.
func TestRoutersAreMounted(t *testing.T) {
	t.Parallel()

	var ran bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /things", ran: &ran})

	if rec := request(t, srv.apiHandler(), http.MethodGet, "/things", testToken); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the router was not mounted: %s", rec.Code, rec.Body)
	}
	if !ran {
		t.Error("the route did not run")
	}
}

func TestEveryRouterIsMounted(t *testing.T) {
	t.Parallel()

	var first, second bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /first", ran: &first},
		stubRouter{pattern: "GET /second", ran: &second})

	request(t, srv.apiHandler(), http.MethodGet, "/first", testToken)
	request(t, srv.apiHandler(), http.MethodGet, "/second", testToken)

	if !first || !second {
		t.Errorf("mounted first=%v second=%v, want both", first, second)
	}
}

// Registered routes are authenticated by construction. A router cannot opt out,
// which is the point of mounting them behind the catch-all rather than beside
// the public routes.
func TestRegisteredRoutesAreAuthenticated(t *testing.T) {
	t.Parallel()

	var ran bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /things", ran: &ran})

	rec := request(t, srv.apiHandler(), http.MethodGet, "/things", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if ran {
		t.Error("the route ran without a token")
	}
}

// And resolved. A handler reading auth.UserFrom must always find one, or every
// route needs its own nil check.
func TestRegisteredRoutesSeeAResolvedUser(t *testing.T) {
	t.Parallel()

	var saw *auth.User
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /things", sawUser: &saw})

	request(t, srv.apiHandler(), http.MethodGet, "/things", testToken)

	if saw == nil {
		t.Fatal("the route ran with no user on the context — ResolveUser is not in the chain")
	}
	if saw.Subject != "test" {
		t.Errorf("user subject = %q, want the verified principal's", saw.Subject)
	}
}

// A server with no routers still serves its probes. That is the state every
// existing test runs in, and the state a misconfigured deployment lands in.
func TestNoRoutersStillServesTheProbes(t *testing.T) {
	t.Parallel()

	srv := New(testConfig(), discardLogger(), deps(healthyDB()))

	if rec := request(t, srv.apiHandler(), http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("healthz = %d with no routers, want 200", rec.Code)
	}
	if rec := request(t, srv.apiHandler(), http.MethodGet, "/things", testToken); rec.Code != http.StatusNotFound {
		t.Errorf("an unregistered route = %d, want 404", rec.Code)
	}
}

// Routers are mounted on the API listener only. The webhook port is public.
func TestRoutersAreNotMountedOnTheWebhookListener(t *testing.T) {
	t.Parallel()

	var ran bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /things", ran: &ran})

	rec := httptest.NewRecorder()
	srv.webhookHandler().ServeHTTP(rec,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/things", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("webhook GET /things = %d, want 404", rec.Code)
	}
	if ran {
		t.Error("an API route ran on the public webhook listener")
	}
}
