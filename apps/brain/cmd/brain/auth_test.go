package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

const devToken = "development-token"

// discoveryOnly serves the one document NewOIDCVerifier fetches at
// construction. No keys are needed: nothing here verifies a token.
func discoveryOnly(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		issuer := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"jwks_uri":                              issuer + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A brain that can authenticate nobody would pass its probes and reject every
// real request. Refusing to start is the only honest answer.
func TestNewVerifierRefusesWhenNothingCanAuthenticate(t *testing.T) {
	t.Parallel()

	v, err := newVerifier(t.Context(), &config.Config{})
	if err == nil {
		t.Fatalf("built a verifier with no authentication configured: %+v", v)
	}
	if v != nil {
		t.Errorf("returned a verifier alongside the error: %+v", v)
	}
}

// The error is read by an operator who has to fix it, so it must name the
// variables they set — with the prefix config.Load applies.
func TestNewVerifierErrorNamesBothWaysToFixIt(t *testing.T) {
	t.Parallel()

	_, err := newVerifier(t.Context(), &config.Config{})
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"OPENARITY_OIDC_ENABLED", "OPENARITY_DEV_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestNewVerifierBuildsADevOnlyChain(t *testing.T) {
	t.Parallel()

	v, err := newVerifier(t.Context(), &config.Config{DevToken: devToken})
	if err != nil {
		t.Fatalf("newVerifier: %v", err)
	}

	p, err := v.Verify(t.Context(), devToken)
	if err != nil {
		t.Fatalf("the dev token was not accepted: %v", err)
	}
	if p.Kind != auth.KindDev {
		t.Errorf("Kind = %q, want %q", p.Kind, auth.KindDev)
	}
}

// OIDC disabled means the issuer is never contacted, even when one is set —
// otherwise a stale OIDC_ISSUER left in the environment becomes a boot
// dependency for a deployment that does not use it.
func TestNewVerifierIgnoresTheIssuerWhenOIDCIsDisabled(t *testing.T) {
	t.Parallel()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := newVerifier(t.Context(), &config.Config{
		OIDCEnabled: false,
		OIDCIssuer:  srv.URL,
		DevToken:    devToken,
	})
	if err != nil {
		t.Fatalf("newVerifier: %v", err)
	}
	if hits != 0 {
		t.Errorf("the issuer was contacted %d times with OIDC disabled", hits)
	}
}

func TestNewVerifierBuildsAnOIDCOnlyChain(t *testing.T) {
	t.Parallel()

	v, err := newVerifier(t.Context(), &config.Config{
		OIDCEnabled:  true,
		OIDCIssuer:   discoveryOnly(t),
		OIDCAudience: "openarity",
	})
	if err != nil {
		t.Fatalf("newVerifier: %v", err)
	}
	if v == nil {
		t.Fatal("newVerifier returned a nil verifier and no error")
	}

	// No dev verifier is in the chain, so the development token is just a
	// string that fails to parse as a JWT.
	if p, err := v.Verify(t.Context(), devToken); err == nil {
		t.Errorf("the dev token was accepted with no dev verifier configured: %+v", p)
	}
}

// Development runs both: OIDC against a real provider, and the static token so
// a developer can curl without logging in.
func TestNewVerifierBuildsBothVerifiers(t *testing.T) {
	t.Parallel()

	v, err := newVerifier(t.Context(), &config.Config{
		OIDCEnabled:  true,
		OIDCIssuer:   discoveryOnly(t),
		OIDCAudience: "openarity",
		DevToken:     devToken,
	})
	if err != nil {
		t.Fatalf("newVerifier: %v", err)
	}

	chain, ok := v.(auth.Chain)
	if !ok {
		t.Fatalf("newVerifier returned %T, want an auth.Chain", v)
	}
	if len(chain) != 2 {
		t.Fatalf("chain has %d verifiers, want 2", len(chain))
	}

	// The dev token must still work: OIDC rejecting it has to fall through
	// rather than end the chain.
	if _, err := v.Verify(t.Context(), devToken); err != nil {
		t.Errorf("the dev token was not accepted alongside OIDC: %v", err)
	}
}

// An unreachable identity provider is a boot failure. A pod that starts and
// then rejects everything is worse than one that crash-loops into an alert.
func TestNewVerifierFailsWhenTheIssuerIsUnreachable(t *testing.T) {
	t.Parallel()

	v, err := newVerifier(t.Context(), &config.Config{
		OIDCEnabled:  true,
		OIDCIssuer:   "http://127.0.0.1:1/nowhere",
		OIDCAudience: "openarity",
	})
	if err == nil {
		t.Fatalf("built a verifier against an unreachable issuer: %+v", v)
	}
}

// The dev token must not paper over a broken identity provider. If OIDC is
// enabled and cannot be built, that is a failure whatever else is configured.
func TestNewVerifierFailsOnABrokenIssuerEvenWithADevToken(t *testing.T) {
	t.Parallel()

	v, err := newVerifier(t.Context(), &config.Config{
		OIDCEnabled:  true,
		OIDCIssuer:   "http://127.0.0.1:1/nowhere",
		OIDCAudience: "openarity",
		DevToken:     devToken,
	})
	if err == nil {
		t.Fatalf("a dev token masked an unreachable issuer: %+v", v)
	}
}

// Discovery happens while newVerifier runs, so a cancelled context must stop
// it rather than wait out the client timeout.
func TestNewVerifierHonoursACancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := newVerifier(ctx, &config.Config{
		OIDCEnabled:  true,
		OIDCIssuer:   discoveryOnly(t),
		OIDCAudience: "openarity",
	})
	if err == nil {
		t.Fatal("discovery ran to completion on a cancelled context")
	}
}
