package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPrincipalRoundTrip(t *testing.T) {
	t.Parallel()

	want := &Principal{Kind: KindUser, Issuer: "https://idp", Subject: "u1", Email: "a@b.c"}

	got, ok := PrincipalFrom(WithPrincipal(t.Context(), want))
	if !ok {
		t.Fatal("PrincipalFrom reported nothing on a context that carries a principal")
	}
	if *got != *want {
		t.Errorf("Principal = %+v, want %+v", *got, *want)
	}
}

// A handler reading a context the middleware never touched must be told so,
// not handed a zero Principal it would treat as a real caller.
func TestPrincipalFromReportsAbsence(t *testing.T) {
	t.Parallel()

	p, ok := PrincipalFrom(t.Context())
	if ok {
		t.Errorf("PrincipalFrom reported a principal on a bare context: %+v", p)
	}
	if p != nil {
		t.Errorf("PrincipalFrom returned %+v on a bare context, want nil", p)
	}
}

// The key is an unexported struct type precisely so no other package can write
// it. A string key named "principal" is the collision this prevents.
func TestForeignContextKeysCannotImpersonateAPrincipal(t *testing.T) {
	t.Parallel()

	type otherKey struct{}

	ctx := context.WithValue(t.Context(), otherKey{}, &Principal{Subject: "impostor"})
	//nolint:staticcheck // SA1029: using a string key is the mistake under test
	ctx = context.WithValue(ctx, "principal", &Principal{Subject: "impostor"})

	if p, ok := PrincipalFrom(ctx); ok {
		t.Errorf("a foreign key was read as a principal: %+v", p)
	}
}

// Nesting must shadow, not merge: an inner principal replaces the outer one
// for the inner scope only.
func TestWithPrincipalShadows(t *testing.T) {
	t.Parallel()

	outerCtx := WithPrincipal(t.Context(), &Principal{Subject: "outer"})
	innerCtx := WithPrincipal(outerCtx, &Principal{Subject: "inner"})

	inner, _ := PrincipalFrom(innerCtx)
	if inner.Subject != "inner" {
		t.Errorf("inner scope saw %q, want inner", inner.Subject)
	}

	outer, _ := PrincipalFrom(outerCtx)
	if outer.Subject != "outer" {
		t.Errorf("the outer context was modified: saw %q", outer.Subject)
	}
}

// WithPrincipal must not lose the values a caller already put on the context —
// the request context carries deadlines and cancellation the handler needs.
func TestWithPrincipalPreservesCancellation(t *testing.T) {
	t.Parallel()

	base, cancel := context.WithCancel(t.Context())
	ctx := WithPrincipal(base, &Principal{Subject: "u1"})

	cancel()
	if ctx.Err() == nil {
		t.Error("cancelling the parent did not cancel the context carrying the principal")
	}
}

// Storing a typed nil is a caller error, but it must not be reported as a
// present principal — a handler that trusts ok would dereference nil.
func TestPrincipalFromDoesNotReportATypedNil(t *testing.T) {
	t.Parallel()

	p, ok := PrincipalFrom(WithPrincipal(t.Context(), nil))
	if ok && p == nil {
		t.Error("PrincipalFrom reported a principal is present, but returned nil")
	}
}

func TestUserRoundTrip(t *testing.T) {
	t.Parallel()

	want := &User{ID: uuid.New(), Teams: []Membership{{Name: "platform", Role: "admin"}}}

	got, ok := UserFrom(WithUser(t.Context(), want))
	if !ok {
		t.Fatal("UserFrom reported nothing on a context that carries a user")
	}
	if got.ID != want.ID || len(got.Teams) != 1 || got.Teams[0].Role != "admin" {
		t.Errorf("User = %+v, want %+v", got, want)
	}
}

// A handler on a context the identity middleware never touched must be told
// so, not handed a zero User whose empty Teams reads as "registered, no
// access" — which is a real state, and a very different one.
func TestUserFromReportsAbsence(t *testing.T) {
	t.Parallel()

	u, ok := UserFrom(t.Context())
	if ok {
		t.Errorf("UserFrom reported a user on a bare context: %+v", u)
	}
	if u != nil {
		t.Errorf("UserFrom returned %+v on a bare context, want nil", u)
	}
}

func TestUserFromDoesNotReportATypedNil(t *testing.T) {
	t.Parallel()

	u, ok := UserFrom(WithUser(t.Context(), nil))
	if ok && u == nil {
		t.Error("UserFrom reported a user is present, but returned nil")
	}
}

// Two keys, two values, no interference. Reusing one key for both would make
// whichever was written last shadow the other.
func TestPrincipalAndUserDoNotShareAKey(t *testing.T) {
	t.Parallel()

	ctx := WithPrincipal(t.Context(), &Principal{Kind: KindUser, Subject: "u1"})
	ctx = WithUser(ctx, &User{ID: uuid.New()})

	if p, ok := PrincipalFrom(ctx); !ok || p.Subject != "u1" {
		t.Errorf("the principal was lost when a user was added: %+v", p)
	}
	if _, ok := UserFrom(ctx); !ok {
		t.Error("the user is not readable")
	}
}

// A user on the context without a principal is a programming error, but it
// must not make PrincipalFrom answer with the user.
func TestUserIsNotReadableAsAPrincipal(t *testing.T) {
	t.Parallel()

	ctx := WithUser(t.Context(), &User{ID: uuid.New()})
	if p, ok := PrincipalFrom(ctx); ok {
		t.Errorf("a user was read as a principal: %+v", p)
	}
}
