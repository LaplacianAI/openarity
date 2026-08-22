package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
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

func (r stubRouter) Register(mux *http.ServeMux, g api.RouteGuard) {
	h := func(w http.ResponseWriter, req *http.Request) {
		if r.ran != nil {
			*r.ran = true
		}
		if r.sawUser != nil {
			u, _ := auth.UserFrom(req.Context())
			*r.sawUser = u
		}
		w.WriteHeader(http.StatusOK)
	}

	// The same split apiHandler makes: a public router is outside
	// authentication, so there is no caller to authorise.
	if !r.public {
		guarded, err := g.Wrap(r.pattern, h)
		if err != nil {
			panic(err)
		}
		h = guarded
	}

	mux.HandleFunc(r.pattern, h)
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

// The gateway is reachable from the internet, so it goes on the listener that
// authenticates by signature. Mounting it on the API port would put it behind
// a bearer token no provider can send.
func TestWebhookRoutersAreMountedOnTheWebhookListener(t *testing.T) {
	t.Parallel()

	var ran bool
	d := deps(healthyDB())
	d.WebhookRouters = []Router{stubRouter{pattern: "POST /hooks/custom/{channel_id}", public: true, ran: &ran}}

	srv := New(testConfig(), discardLogger(), d)

	rec := httptest.NewRecorder()
	srv.webhookHandler().ServeHTTP(rec,
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hooks/custom/abc", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the gateway was not mounted: %s", rec.Code, rec.Body)
	}
	if !ran {
		t.Error("the hook did not run")
	}
}

// And nowhere else. A route on both listeners is a route with two different
// sets of rules, and only one of them was reviewed.
func TestWebhookRoutersAreNotMountedOnTheAPIListener(t *testing.T) {
	t.Parallel()

	var ran bool
	d := deps(healthyDB())
	d.WebhookRouters = []Router{stubRouter{pattern: "POST /hooks/custom/{channel_id}", public: true, ran: &ran}}

	srv := New(testConfig(), discardLogger(), d)

	rec := request(t, srv.apiHandler(), http.MethodPost, "/hooks/custom/abc", testToken)
	if rec.Code != http.StatusNotFound {
		t.Errorf("api POST /hooks/... = %d, want 404", rec.Code)
	}
	if ran {
		t.Error("a webhook route ran on the API listener")
	}
}

// A provider holds a signing secret, not a token. The hook has to answer
// without an Authorization header or nothing ever reaches it.
func TestAWebhookRouteNeedsNoToken(t *testing.T) {
	t.Parallel()

	var ran bool
	d := deps(healthyDB())
	d.WebhookRouters = []Router{stubRouter{pattern: "POST /hooks/custom/{channel_id}", public: true, ran: &ran}}

	srv := New(testConfig(), discardLogger(), d)

	rec := httptest.NewRecorder()
	srv.webhookHandler().ServeHTTP(rec,
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hooks/custom/abc", nil))

	if !ran {
		t.Errorf("the hook did not run without a token: %d %s", rec.Code, rec.Body)
	}
}

// There is no guard on this listener, so a router that expects one would be
// registered with nil and panic on the first route — at boot, in a message
// about a nil pointer. Refusing it by name says what is actually wrong.
func TestANonPublicWebhookRouterIsRefused(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a guarded router was accepted on the unguarded listener")
		}
		// Specifically at construction, and specifically by name. Without the
		// check it still panics — on the nil guard, later, with a message
		// about a nil pointer that says nothing about which router or why.
		msg, isString := r.(string)
		if !isString || !strings.Contains(msg, "public") {
			t.Errorf("panic = %v, want one naming the router as not public", r)
		}
	}()

	d := deps(healthyDB())
	d.WebhookRouters = []Router{stubRouter{pattern: "POST /hooks/custom/{channel_id}"}}

	New(testConfig(), discardLogger(), d)
}

// The probes answer on both listeners whether or not a gateway exists. A
// webhook router taking one over must fail the boot rather than shadow it.
func TestAWebhookRouterCannotShadowTheProbes(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("a webhook router claimed GET /healthz without a panic")
		}
	}()

	d := deps(healthyDB())
	d.WebhookRouters = []Router{stubRouter{pattern: "GET /healthz", public: true}}

	New(testConfig(), discardLogger(), d).webhookHandler()
}

// A brain with no adapters compiled in is a normal deployment.
func TestNoWebhookRoutersStillServesTheProbes(t *testing.T) {
	t.Parallel()

	srv := New(testConfig(), discardLogger(), deps(healthyDB()))

	rec := httptest.NewRecorder()
	srv.webhookHandler().ServeHTTP(rec,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("webhook healthz = %d with no gateway, want 200", rec.Code)
	}
}
