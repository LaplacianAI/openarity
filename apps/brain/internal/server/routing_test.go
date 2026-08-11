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
	public  bool
	ran     *bool
	sawUser **auth.User
}

func (r stubRouter) Public() bool { return r.public }

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

// A public router answers without a token. That is the whole reason the flag
// exists: a browser navigating to a documentation page cannot send one.
func TestAPublicRouterSkipsAuthentication(t *testing.T) {
	t.Parallel()

	var ran bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /docs", public: true, ran: &ran})

	rec := request(t, srv.apiHandler(), http.MethodGet, "/docs", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a public route asked for a token: %s", rec.Code, rec.Body)
	}
	if !ran {
		t.Error("the route did not run")
	}
}

// Being public must not leak across routers. One public router in the list
// cannot be allowed to open the rest.
func TestOnePublicRouterDoesNotOpenTheOthers(t *testing.T) {
	t.Parallel()

	var public, protected bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /docs", public: true, ran: &public},
		stubRouter{pattern: "GET /things", ran: &protected})

	if rec := request(t, srv.apiHandler(), http.MethodGet, "/things", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("a protected route answered %d without a token, want 401", rec.Code)
	}
	if protected {
		t.Error("the protected handler ran for an unauthenticated request")
	}

	if rec := request(t, srv.apiHandler(), http.MethodGet, "/docs", ""); rec.Code != http.StatusOK {
		t.Errorf("the public route answered %d", rec.Code)
	}
}

// A public route does not resolve a user, so nothing downstream may assume one
// is on the context.
func TestAPublicRouteHasNoUser(t *testing.T) {
	t.Parallel()

	var saw *auth.User
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /docs", public: true, sawUser: &saw})

	request(t, srv.apiHandler(), http.MethodGet, "/docs", "")

	if saw != nil {
		t.Errorf("a public route saw user %+v", saw)
	}
}

// A valid token on a public route is ignored rather than rejected. A client
// that sends one everywhere should not have to special-case the docs.
func TestAPublicRouteAcceptsATokenItDoesNotNeed(t *testing.T) {
	t.Parallel()

	var ran bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /docs", public: true, ran: &ran})

	if rec := request(t, srv.apiHandler(), http.MethodGet, "/docs", testToken); rec.Code != http.StatusOK {
		t.Errorf("status = %d with a valid token, want 200", rec.Code)
	}
	if !ran {
		t.Error("the route did not run")
	}
}

// The probes are mounted by the server itself. A public router must not be
// able to take one over — the mux panics on a duplicate, so this fails the
// boot rather than shadowing readiness.
func TestAPublicRouterCannotShadowTheProbes(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("a router claimed GET /healthz without a panic")
		}
	}()

	var ran bool
	srv := New(testConfig(), discardLogger(), deps(healthyDB()),
		stubRouter{pattern: "GET /healthz", public: true, ran: &ran})
	srv.apiHandler()
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
