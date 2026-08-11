package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ok records that it ran and returns 200.
func ok(ran *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	}
}

// serve mounts a router and drives one request at it.
func serve(t *testing.T, r *Router, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	r.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

// The prefix is the point of the type: routes are written relative to it, so a
// package can be remounted by changing one string.
func TestPrefixIsAppliedToEveryRoute(t *testing.T) {
	t.Parallel()

	var list, get bool
	r := NewRouter("/teams")
	r.Get("", ok(&list))
	r.Get("/{id}", ok(&get))

	if rec := serve(t, r, http.MethodGet, "/teams"); rec.Code != http.StatusOK {
		t.Errorf("GET /teams = %d, want 200", rec.Code)
	}
	if rec := serve(t, r, http.MethodGet, "/teams/abc"); rec.Code != http.StatusOK {
		t.Errorf("GET /teams/abc = %d, want 200", rec.Code)
	}
	if !list || !get {
		t.Errorf("ran list=%v get=%v, want both", list, get)
	}
}

// The unprefixed path must not match. Otherwise moving a package's prefix
// leaves the old routes answering.
func TestRoutesDoNotAnswerWithoutThePrefix(t *testing.T) {
	t.Parallel()

	var ran bool
	r := NewRouter("/teams")
	r.Get("", ok(&ran))

	if rec := serve(t, r, http.MethodGet, "/"); rec.Code == http.StatusOK {
		t.Error("the route answered at the root")
	}
	if ran {
		t.Error("the handler ran for an unprefixed path")
	}
}

// An empty prefix mounts at the root, which is what a package with no natural
// prefix needs.
func TestAnEmptyPrefixMountsAtTheRoot(t *testing.T) {
	t.Parallel()

	var ran bool
	r := NewRouter("")
	r.Get("/healthz", ok(&ran))

	if rec := serve(t, r, http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", rec.Code)
	}
	if !ran {
		t.Error("the handler did not run")
	}
}

// Path values have to survive the prefix, or every route with an id has to
// parse the URL itself.
func TestPathValuesWorkThroughThePrefix(t *testing.T) {
	t.Parallel()

	var saw string
	r := NewRouter("/teams")
	r.Get("/{id}/members/{user}", func(w http.ResponseWriter, req *http.Request) {
		saw = req.PathValue("id") + ":" + req.PathValue("user")
	})

	serve(t, r, http.MethodGet, "/teams/t1/members/u2")

	if saw != "t1:u2" {
		t.Errorf("path values = %q, want t1:u2", saw)
	}
}

// Each verb has to reach its own handler. A helper wired to the wrong method
// would make a GET route answer POSTs, which is how a read endpoint becomes a
// write one.
func TestEachVerbRegistersItsOwnMethod(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		method string
		add    func(*Router, string, http.HandlerFunc)
	}{
		{http.MethodGet, (*Router).Get},
		{http.MethodPost, (*Router).Post},
		{http.MethodPut, (*Router).Put},
		{http.MethodPatch, (*Router).Patch},
		{http.MethodDelete, (*Router).Delete},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()

			var ran bool
			r := NewRouter("/things")
			tc.add(r, "", ok(&ran))

			if rec := serve(t, r, tc.method, "/things"); rec.Code != http.StatusOK {
				t.Fatalf("%s /things = %d, want 200", tc.method, rec.Code)
			}

			// Every other verb must miss.
			for _, other := range []string{
				http.MethodGet, http.MethodPost, http.MethodPut,
				http.MethodPatch, http.MethodDelete,
			} {
				if other == tc.method {
					continue
				}
				if rec := serve(t, r, other, "/things"); rec.Code == http.StatusOK {
					t.Errorf("a %s route also answered %s", tc.method, other)
				}
			}
		})
	}
}

// Nothing is mounted until Register runs. A router built and never passed to
// the server must not leak routes into a mux it was never given.
func TestNothingIsMountedBeforeRegister(t *testing.T) {
	t.Parallel()

	var ran bool
	r := NewRouter("/things")
	r.Get("", ok(&ran))

	mux := http.NewServeMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/things", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d on a mux that was never registered, want 404", rec.Code)
	}
	if ran {
		t.Error("the handler ran without Register")
	}
}

// Two routers on one mux is the normal case — that is how internal/server
// mounts every domain.
func TestSeveralRoutersShareAMux(t *testing.T) {
	t.Parallel()

	var teams, agents bool
	mux := http.NewServeMux()

	t1 := NewRouter("/teams")
	t1.Get("", ok(&teams))
	t1.Register(mux)

	a1 := NewRouter("/agents")
	a1.Get("", ok(&agents))
	a1.Register(mux)

	for _, path := range []string{"/teams", "/agents"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
	if !teams || !agents {
		t.Errorf("ran teams=%v agents=%v, want both", teams, agents)
	}
}

// Two routers claiming the same pattern is a wiring mistake. ServeMux panics,
// which turns it into a failed boot rather than one route quietly shadowing
// the other for the life of the deployment.
func TestADuplicatePatternPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("registering the same pattern twice did not panic")
		}
	}()

	var a, b bool
	mux := http.NewServeMux()

	first := NewRouter("/teams")
	first.Get("", ok(&a))
	first.Register(mux)

	second := NewRouter("/teams")
	second.Get("", ok(&b))
	second.Register(mux)
}

// A prefix without a leading slash produces a pattern ServeMux reads as a host
// match, so the route mounts somewhere nobody intended. Fail at construction,
// which runs at startup, rather than serving the wrong thing.
func TestNewRouterRejectsAMalformedPrefix(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"teams", "teams/", "/teams/"} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Errorf("NewRouter(%q) did not panic", prefix)
				}
			}()
			NewRouter(prefix)
		})
	}
}

// The panic has to name the prefix. A stack trace saying "must start with /"
// with no value is a search through every NewRouter call in the tree.
func TestTheMalformedPrefixPanicNamesTheValue(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "oops") {
			t.Errorf("panic does not name the prefix: %v", r)
		}
	}()
	NewRouter("oops")
}

func TestAValidPrefixDoesNotPanic(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"", "/teams", "/api/v1", "/a"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("NewRouter(%q) panicked: %v", prefix, r)
				}
			}()
			NewRouter(prefix)
		}()
	}
}

// Patterns is what the OpenAPI contract test compares against the spec, so it
// has to report exactly what Register mounts — prefix included.
func TestPatternsReportsWhatIsMounted(t *testing.T) {
	t.Parallel()

	var ran bool
	r := NewRouter("/teams")
	r.Get("", ok(&ran))
	r.Post("", ok(&ran))
	r.Get("/{id}/members", ok(&ran))
	r.Delete("/{id}/members/{userID}", ok(&ran))

	got := r.Patterns()
	want := []string{
		"GET /teams",
		"POST /teams",
		"GET /teams/{id}/members",
		"DELETE /teams/{id}/members/{userID}",
	}

	if len(got) != len(want) {
		t.Fatalf("Patterns() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Patterns()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The pattern Patterns reports and the pattern ServeMux receives come from the
// same joining logic. If they ever diverge, the contract test checks strings
// the server never serves — and passes while the API is undocumented.
func TestPatternsMatchWhatServeMuxIsGiven(t *testing.T) {
	t.Parallel()

	var ran bool
	for _, prefix := range []string{"", "/teams", "/api/v1"} {
		r := NewRouter(prefix)
		r.Get("/{id}", ok(&ran))

		mux := http.NewServeMux()
		r.Register(mux)

		pattern := r.Patterns()[0]
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			t.Fatalf("pattern %q has no method", pattern)
		}

		// The mux resolves the request only if the path it registered is the
		// one Patterns named.
		concrete := strings.Replace(path, "{id}", "42", 1)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, concrete, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("prefix %q: Patterns said %q but the mux answered %d for %s",
				prefix, pattern, rec.Code, concrete)
		}
	}
}

func TestPatternsIsEmptyForAnEmptyRouter(t *testing.T) {
	t.Parallel()

	if got := NewRouter("/things").Patterns(); len(got) != 0 {
		t.Errorf("Patterns() = %v on a router with no routes", got)
	}
}

// Public is what puts a router outside authentication, so its default matters
// more than most: a router that forgot to say is a protected one.
func TestRoutersAreProtectedByDefault(t *testing.T) {
	t.Parallel()

	if NewRouter("/teams").Public() {
		t.Error("NewRouter produced a public router — every data route would skip authentication")
	}
	if !NewPublicRouter("/docs").Public() {
		t.Error("NewPublicRouter produced a protected router")
	}
}

// A public router is a normal router in every other respect. If it validated
// its prefix differently, the one kind of router mounted outside authentication
// would also be the one with the weakest checks.
func TestAPublicRouterIsOtherwiseANormalRouter(t *testing.T) {
	t.Parallel()

	var ran bool
	r := NewPublicRouter("/docs")
	r.Get("/spec", ok(&ran))

	if got := r.Patterns(); len(got) != 1 || got[0] != "GET /docs/spec" {
		t.Errorf("Patterns() = %v", got)
	}
	if rec := serve(t, r, http.MethodGet, "/docs/spec"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	defer func() {
		if recover() == nil {
			t.Error("NewPublicRouter accepted a malformed prefix that NewRouter rejects")
		}
	}()
	NewPublicRouter("docs")
}

// A router with no routes is legal — a domain package might register nothing
// under a feature flag — and must not break the mux it is given.
func TestAnEmptyRouterMountsNothing(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewRouter("/things").Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/things", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
