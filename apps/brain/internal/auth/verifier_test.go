package auth

import (
	"context"
	"errors"
	"testing"
)

// stub is a Verifier whose answer the test dictates, and which records that it
// was asked at all.
type stub struct {
	principal *Principal
	err       error
	calls     *int
	sawToken  *string
}

func (s stub) Verify(_ context.Context, token string) (*Principal, error) {
	if s.calls != nil {
		*s.calls++
	}
	if s.sawToken != nil {
		*s.sawToken = token
	}
	return s.principal, s.err
}

func accepts(p *Principal) stub { return stub{principal: p} }
func denies() stub              { return stub{err: ErrUnauthenticated} }

func TestChainReturnsTheFirstAcceptance(t *testing.T) {
	t.Parallel()

	want := &Principal{Kind: KindUser, Subject: "first"}
	c := Chain{accepts(want), accepts(&Principal{Subject: "second"})}

	got, err := c.Verify(t.Context(), "token")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Subject != "first" {
		t.Errorf("Subject = %q, want the first verifier's answer", got.Subject)
	}
}

// Order is the contract. Once a verifier accepts, the rest must not run — a
// later verifier could otherwise be handed a token that is not its own.
func TestChainStopsAtTheFirstAcceptance(t *testing.T) {
	t.Parallel()

	var second int
	c := Chain{
		accepts(&Principal{Subject: "first"}),
		stub{principal: &Principal{Subject: "second"}, calls: &second},
	}

	if _, err := c.Verify(t.Context(), "token"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if second != 0 {
		t.Errorf("the second verifier ran %d times after the first accepted", second)
	}
}

func TestChainFallsThroughToTheNextVerifier(t *testing.T) {
	t.Parallel()

	var firstCalls int
	c := Chain{
		stub{err: ErrUnauthenticated, calls: &firstCalls},
		accepts(&Principal{Kind: KindDev, Subject: "dev"}),
	}

	p, err := c.Verify(t.Context(), "token")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if firstCalls != 1 {
		t.Errorf("the first verifier ran %d times, want 1", firstCalls)
	}
	if p.Kind != KindDev {
		t.Errorf("Kind = %q, want the second verifier's answer", p.Kind)
	}
}

// Which verifier rejected, and why, is information the caller must not get.
// Every rejection looks identical from outside.
func TestChainHidesTheUnderlyingError(t *testing.T) {
	t.Parallel()

	secret := errors.New("kid a1b2c3 not found in https://idp.internal/keys")
	c := Chain{stub{err: secret}, denies()}

	p, err := c.Verify(t.Context(), "token")
	if err == nil {
		t.Fatalf("accepted, returned %+v", p)
	}
	if errors.Is(err, secret) {
		t.Errorf("the underlying error reached the caller: %v", err)
	}
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("error does not match ErrUnauthenticated: %v", err)
	}
}

func TestChainRejectsWhenEveryVerifierRejects(t *testing.T) {
	t.Parallel()

	c := Chain{denies(), denies(), denies()}

	p, err := c.Verify(t.Context(), "token")
	if err == nil {
		t.Fatalf("accepted, returned %+v", p)
	}
	if p != nil {
		t.Errorf("returned a principal alongside the error: %+v", p)
	}
}

// An empty chain is what a misconfigured brain produces: OIDC disabled and no
// dev token. It must reject everything rather than accept everything.
func TestEmptyChainRejectsEverything(t *testing.T) {
	t.Parallel()

	var c Chain

	for _, token := range []string{"", "anything", devToken} {
		p, err := c.Verify(t.Context(), token)
		if err == nil {
			t.Errorf("empty chain accepted %q, returned %+v", token, p)
		}
	}
}

func TestChainPassesTheTokenThrough(t *testing.T) {
	t.Parallel()

	var saw string
	c := Chain{stub{err: ErrUnauthenticated, sawToken: &saw}}

	if _, err := c.Verify(t.Context(), "the-exact-token"); err == nil {
		t.Fatal("expected a rejection")
	}
	if saw != "the-exact-token" {
		t.Errorf("verifier saw %q, want the token verbatim", saw)
	}
}

// A verifier returning (nil, nil) would make Chain hand back a nil Principal
// with no error, and the first handler to read it panics. Whatever Chain does
// with that, it must not be "success with nothing".
func TestChainDoesNotSucceedWithANilPrincipal(t *testing.T) {
	t.Parallel()

	c := Chain{stub{principal: nil, err: nil}}

	p, err := c.Verify(t.Context(), "token")
	if err == nil && p == nil {
		t.Fatal("Chain reported success with a nil principal")
	}
}
