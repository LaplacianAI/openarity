package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/middleware"
)

// resolver stands in for the store: the test says what it answers and can see
// which principal it was asked about.
type resolver struct {
	user         *auth.User
	err          error
	calls        *int
	sawPrincipal **auth.Principal
	sawCtx       *context.Context
}

func (r resolver) Resolve(ctx context.Context, p *auth.Principal) (*auth.User, error) {
	if r.calls != nil {
		*r.calls++
	}
	if r.sawPrincipal != nil {
		*r.sawPrincipal = p
	}
	if r.sawCtx != nil {
		*r.sawCtx = ctx
	}
	return r.user, r.err
}

// userHandler records the user it could see on the context.
type userHandler struct {
	ran   bool
	user  *auth.User
	found bool
}

func (h *userHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.ran = true
	h.user, h.found = auth.UserFrom(r.Context())
	w.WriteHeader(http.StatusOK)
}

func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

// resolve runs one request through ResolveUser with a principal already on the
// context, as Authenticate would have left it.
func resolve(t *testing.T, r middleware.Resolver, p *auth.Principal) (*httptest.ResponseRecorder, *userHandler, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	h := &userHandler{}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}

	rec := httptest.NewRecorder()
	middleware.ResolveUser(r, logTo(&buf))(h).ServeHTTP(rec, req)
	return rec, h, &buf
}

func testPrincipal() *auth.Principal {
	return &auth.Principal{
		Kind: auth.KindUser, Issuer: "https://idp", Subject: "user-42", Email: "a@example.com",
	}
}

func TestResolveUserPutsTheUserOnTheContext(t *testing.T) {
	t.Parallel()

	want := &auth.User{
		ID: uuid.New(), Issuer: "https://idp", Subject: "user-42",
		Teams: []auth.Membership{{TeamID: uuid.New(), Name: "platform", Role: "admin"}},
	}

	rec, h, _ := resolve(t, resolver{user: want}, testPrincipal())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !h.ran {
		t.Fatal("the handler did not run")
	}
	if !h.found {
		t.Fatal("the handler could not read a user from the context")
	}
	if h.user.ID != want.ID || len(h.user.Teams) != 1 {
		t.Errorf("handler saw %+v, want %+v", h.user, want)
	}
}

// The principal the middleware resolves must be the one Authenticate wrote,
// not a fresh one — otherwise the identity being looked up is not the identity
// that was verified.
func TestResolveUserPassesThePrincipalThrough(t *testing.T) {
	t.Parallel()

	var saw *auth.Principal
	p := testPrincipal()

	resolve(t, resolver{user: &auth.User{ID: uuid.New()}, sawPrincipal: &saw}, p)

	if saw == nil {
		t.Fatal("the resolver was not called")
	}
	if saw != p {
		t.Errorf("resolver saw %+v, want the principal from the context", saw)
	}
}

// Order is fixed: Authenticate then ResolveUser. Wired the other way round
// there is no principal to resolve, and every request fails.
func TestResolveUserFailsWithoutAPrincipal(t *testing.T) {
	t.Parallel()

	var calls int
	rec, h, buf := resolve(t, resolver{user: &auth.User{ID: uuid.New()}, calls: &calls}, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if h.ran {
		t.Error("the handler ran with no user")
	}
	if calls != 0 {
		t.Errorf("the resolver was called %d times with no principal", calls)
	}
	if !strings.Contains(buf.String(), "Authenticate") {
		t.Errorf("the log does not say the middleware order is wrong: %s", buf)
	}
}

// A resolution failure is 500, never 401. The token was valid and the database
// is down; 401 sends the client into a login loop that cannot help.
func TestResolveUserAnswers500WhenResolutionFails(t *testing.T) {
	t.Parallel()

	rec, h, _ := resolve(t, resolver{err: errors.New("connection refused")}, testPrincipal())

	if rec.Code == http.StatusUnauthorized {
		t.Fatal("a database failure was reported as an authentication failure")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if h.ran {
		t.Error("the handler ran after resolution failed")
	}
}

// The reason goes to the log, not the response. It names the caller so the
// failure can be traced, and says nothing to the client.
func TestResolveUserLogsTheFailureAndTellsTheClientNothing(t *testing.T) {
	t.Parallel()

	rec, _, buf := resolve(t, resolver{err: errors.New("connection refused")}, testPrincipal())

	if !strings.Contains(buf.String(), "connection refused") {
		t.Errorf("the failure was not logged: %s", buf)
	}
	if !strings.Contains(buf.String(), "user-42") {
		t.Errorf("the log does not name the caller: %s", buf)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the failure reached the client: %s", rec.Body)
	}
}

// The email is a credential-adjacent claim and has no business in a log line
// that fires on every database blip.
func TestResolveUserDoesNotLogTheEmail(t *testing.T) {
	t.Parallel()

	_, _, buf := resolve(t, resolver{err: errors.New("boom")}, testPrincipal())

	if strings.Contains(buf.String(), "a@example.com") {
		t.Errorf("the log leaked the caller's email: %s", buf)
	}
}

// A resolver returning (nil, nil) would put a nil user on the context, and the
// first handler to trust the ok flag panics.
func TestResolveUserRejectsANilUser(t *testing.T) {
	t.Parallel()

	rec, h, _ := resolve(t, resolver{user: nil, err: nil}, testPrincipal())

	if h.ran && h.user == nil {
		t.Error("the handler ran with a nil user on the context")
	}
	if rec.Code == http.StatusOK && h.user == nil {
		t.Error("a nil user produced a successful response")
	}
}

// Resolution is a database read, so it must use the request's context — a
// client that disconnects should stop the query rather than let it run on.
func TestResolveUserPassesTheRequestContext(t *testing.T) {
	t.Parallel()

	var saw context.Context
	type marker struct{}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), testPrincipal()))
	req = req.WithContext(context.WithValue(req.Context(), marker{}, "present"))

	var buf bytes.Buffer
	r := resolver{user: &auth.User{ID: uuid.New()}, sawCtx: &saw}
	middleware.ResolveUser(r, logTo(&buf))(&userHandler{}).ServeHTTP(httptest.NewRecorder(), req)

	if saw == nil {
		t.Fatal("the resolver was not called")
	}
	if saw.Value(marker{}) != "present" {
		t.Error("the resolver did not receive the request context")
	}
}

// One read per request. Resolution is the fixed cost of authenticating, and
// doubling it doubles the load on the database for nothing.
func TestResolveUserResolvesOnce(t *testing.T) {
	t.Parallel()

	var calls int
	resolve(t, resolver{user: &auth.User{ID: uuid.New()}, calls: &calls}, testPrincipal())

	if calls != 1 {
		t.Errorf("the resolver was called %d times, want 1", calls)
	}
}

// The principal has to survive: authz reads the user, but audit and the
// super-admin check read the subject off the principal.
func TestResolveUserKeepsThePrincipalOnTheContext(t *testing.T) {
	t.Parallel()

	var seen *auth.Principal
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = auth.PrincipalFrom(r.Context())
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), testPrincipal()))

	var buf bytes.Buffer
	r := resolver{user: &auth.User{ID: uuid.New()}}
	middleware.ResolveUser(r, logTo(&buf))(h).ServeHTTP(httptest.NewRecorder(), req)

	if seen == nil {
		t.Fatal("the principal was dropped when the user was added")
	}
	if seen.Subject != "user-42" {
		t.Errorf("principal subject = %q, want user-42", seen.Subject)
	}
}

// The two middlewares in the order they are wired, driven end to end: a bearer
// token in, a user on the context out.
func TestAuthenticateThenResolveUser(t *testing.T) {
	t.Parallel()

	want := &auth.User{ID: uuid.New(), Subject: "user-42"}
	h := &userHandler{}

	var buf bytes.Buffer
	chain := middleware.Authenticate(accepts(testPrincipal()))(
		middleware.ResolveUser(resolver{user: want}, logTo(&buf))(h))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer token")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !h.found || h.user.ID != want.ID {
		t.Errorf("handler saw %+v, want %+v", h.user, want)
	}
}

// Reversed, ResolveUser runs first and there is no principal yet. It must fail
// loudly rather than resolve nobody.
func TestReversedOrderFails(t *testing.T) {
	t.Parallel()

	h := &userHandler{}
	var buf bytes.Buffer

	chain := middleware.ResolveUser(resolver{user: &auth.User{ID: uuid.New()}}, logTo(&buf))(
		middleware.Authenticate(accepts(testPrincipal()))(h))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer token")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 — the wrong order must not silently work", rec.Code)
	}
	if h.ran {
		t.Error("the handler ran with the middlewares in the wrong order")
	}
}

// An unauthenticated request must never reach resolution: that would be a
// database read per anonymous request, and a free way to load the database.
func TestResolutionDoesNotRunForARejectedToken(t *testing.T) {
	t.Parallel()

	var calls int
	var buf bytes.Buffer

	chain := middleware.Authenticate(denies())(
		middleware.ResolveUser(resolver{calls: &calls}, logTo(&buf))(&userHandler{}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer bad")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if calls != 0 {
		t.Errorf("the resolver ran %d times for a rejected token", calls)
	}
}
