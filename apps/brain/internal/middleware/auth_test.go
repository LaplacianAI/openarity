package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/middleware"
)

// verifier is a stand-in for the real chain: the test says what it answers and
// the test can see what it was asked.
type verifier struct {
	principal *Principal
	err       error
	calls     *int
	sawToken  *string
	sawCtx    *context.Context
}

// Principal is aliased so the stub reads cleanly.
type Principal = auth.Principal

func (v verifier) Verify(ctx context.Context, token string) (*Principal, error) {
	if v.calls != nil {
		*v.calls++
	}
	if v.sawToken != nil {
		*v.sawToken = token
	}
	if v.sawCtx != nil {
		*v.sawCtx = ctx
	}
	return v.principal, v.err
}

func accepts(p *Principal) verifier { return verifier{principal: p} }
func denies() verifier              { return verifier{err: auth.ErrUnauthenticated} }

// handler records whether it ran and what principal it could see.
type handler struct {
	ran       bool
	principal *Principal
	found     bool
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.ran = true
	h.principal, h.found = auth.PrincipalFrom(r.Context())
	w.WriteHeader(http.StatusOK)
}

// call runs one request through the middleware and returns the response and
// the wrapped handler.
func call(t *testing.T, v auth.Verifier, header string) (*httptest.ResponseRecorder, *handler) {
	t.Helper()

	h := &handler{}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}

	rec := httptest.NewRecorder()
	middleware.Authenticate(v)(h).ServeHTTP(rec, req)
	return rec, h
}

func TestAuthenticateAcceptsAValidToken(t *testing.T) {
	t.Parallel()

	want := &Principal{Kind: auth.KindUser, Issuer: "https://idp", Subject: "u1"}
	rec, h := call(t, accepts(want), "Bearer good-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !h.ran {
		t.Fatal("the handler did not run")
	}
	if !h.found {
		t.Fatal("the handler could not read a principal from the context")
	}
	if *h.principal != *want {
		t.Errorf("handler saw %+v, want %+v", *h.principal, *want)
	}
}

// The credential handed to the verifier must be the token alone. Leaving the
// scheme attached means every token fails to verify.
func TestAuthenticateStripsTheScheme(t *testing.T) {
	t.Parallel()

	var saw string
	v := verifier{principal: &Principal{Subject: "u1"}, sawToken: &saw}
	call(t, v, "Bearer the.exact.token")

	if saw != "the.exact.token" {
		t.Errorf("verifier saw %q, want the token without the scheme", saw)
	}
}

// RFC 7235 makes the auth scheme case-insensitive. A CLI sending "bearer" is
// not making a mistake.
func TestAuthenticateAcceptsAnyCaseOfBearer(t *testing.T) {
	t.Parallel()

	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "BeArEr"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()

			rec, h := call(t, accepts(&Principal{Subject: "u1"}), scheme+" token")
			if rec.Code != http.StatusOK {
				t.Errorf("scheme %q: status = %d, want 200", scheme, rec.Code)
			}
			if !h.ran {
				t.Errorf("scheme %q: the handler did not run", scheme)
			}
		})
	}
}

// Everything a client can get wrong about the header, and the verifier must
// never even be consulted for any of it.
func TestAuthenticateRejectsBadHeaders(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, header string }{
		{"absent", ""},
		{"scheme only", "Bearer"},
		{"scheme and space", "Bearer "},
		{"no scheme", "just-a-token"},
		{"basic", "Basic dXNlcjpwYXNz"},
		{"wrong scheme", "Token abc123"},
		{"leading space", " Bearer abc123"},
		{"empty", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var calls int
			v := verifier{principal: &Principal{Subject: "u1"}, calls: &calls}

			rec, h := call(t, v, tc.header)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if h.ran {
				t.Error("the handler ran despite a rejected header")
			}
			if calls != 0 {
				t.Errorf("the verifier was called %d times for a malformed header", calls)
			}
		})
	}
}

func TestAuthenticateRejectsWhenTheVerifierRejects(t *testing.T) {
	t.Parallel()

	rec, h := call(t, denies(), "Bearer bad-token")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if h.ran {
		t.Error("the handler ran on a rejected token")
	}
}

// A verifier returning (nil, nil) must not put a nil principal on the context.
// The first handler to trust the ok flag would panic.
func TestAuthenticateRejectsANilPrincipal(t *testing.T) {
	t.Parallel()

	rec, h := call(t, verifier{principal: nil, err: nil}, "Bearer token")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if h.ran {
		t.Error("the handler ran with no principal")
	}
}

// Which check failed tells an attacker whether a token is valid-but-wrong or
// simply garbage. Every failure must look identical.
func TestUnauthorizedRepliesAreIndistinguishable(t *testing.T) {
	t.Parallel()

	detailed := errors.New("kid a1b2c3 not found in https://idp.internal/keys")

	noHeader, _ := call(t, denies(), "")
	badToken, _ := call(t, verifier{err: detailed}, "Bearer bad")

	if noHeader.Code != badToken.Code {
		t.Errorf("status differs: %d vs %d", noHeader.Code, badToken.Code)
	}
	if noHeader.Body.String() != badToken.Body.String() {
		t.Errorf("body differs: %q vs %q", noHeader.Body, badToken.Body)
	}
	if strings.Contains(badToken.Body.String(), "kid") ||
		strings.Contains(badToken.Body.String(), "idp.internal") {
		t.Errorf("the verifier's error reached the client: %q", badToken.Body)
	}
}

// RFC 7235 requires WWW-Authenticate on a 401. It is what tells a CLI to go
// obtain a token rather than give up.
func TestUnauthorizedSetsWWWAuthenticate(t *testing.T) {
	t.Parallel()

	rec, _ := call(t, denies(), "Bearer bad")

	got := rec.Header().Get("WWW-Authenticate")
	if got == "" {
		t.Fatal("401 with no WWW-Authenticate header")
	}
	if !strings.HasPrefix(strings.ToLower(got), "bearer") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}
}

// The verifier must get the request's context, not a fresh one: a client that
// disconnects should stop the JWKS fetch waiting on its behalf.
func TestAuthenticatePassesTheRequestContext(t *testing.T) {
	t.Parallel()

	var saw context.Context
	v := verifier{principal: &Principal{Subject: "u1"}, sawCtx: &saw}

	type marker struct{}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer token")
	req = req.WithContext(context.WithValue(req.Context(), marker{}, "present"))

	middleware.Authenticate(v)(&handler{}).ServeHTTP(httptest.NewRecorder(), req)

	if saw == nil {
		t.Fatal("the verifier was not called")
	}
	if saw.Value(marker{}) != "present" {
		t.Error("the verifier did not receive the request context")
	}
}

// The middleware must call the verifier exactly once. Twice would double every
// JWKS lookup and every audit entry.
func TestAuthenticateVerifiesOnce(t *testing.T) {
	t.Parallel()

	var calls int
	v := verifier{principal: &Principal{Subject: "u1"}, calls: &calls}
	call(t, v, "Bearer token")

	if calls != 1 {
		t.Errorf("the verifier was called %d times, want 1", calls)
	}
}

// One middleware value is used concurrently by every request. Sharing state
// across them would leak one caller's identity into another's request.
func TestAuthenticateDoesNotShareStateBetweenRequests(t *testing.T) {
	t.Parallel()

	alice := &Principal{Kind: auth.KindUser, Subject: "alice"}
	bob := &Principal{Kind: auth.KindUser, Subject: "bob"}

	_, first := call(t, accepts(alice), "Bearer a")

	// A second request, through its own middleware, must not disturb the
	// principal the first handler already read.
	h := &handler{}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer b")
	middleware.Authenticate(accepts(bob))(h).ServeHTTP(httptest.NewRecorder(), req)

	if first.principal.Subject != "alice" {
		t.Errorf("the first handler now sees %q", first.principal.Subject)
	}
	if h.principal.Subject != "bob" {
		t.Errorf("the second handler sees %q, want bob", h.principal.Subject)
	}
}
