package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

const (
	byName = "https://auth.example.com/application/o/openarity/"
	byIP   = "http://10.0.0.5:9000/application/o/openarity/"
)

type fakeIssuers struct {
	issuers []string
	err     error

	calls int
}

func (f *fakeIssuers) ListUserIssuers(context.Context) ([]string, error) {
	f.calls++
	return f.issuers, f.err
}

// JSON so the assertions read the fields a log aggregator would, rather than
// matching substrings of a sentence that may be reworded.
func warn(t *testing.T, cfg *config.Config, s issuerLister) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	warnIfIssuerIsNew(t.Context(), cfg, logger, s)

	if buf.Len() == 0 {
		return nil
	}

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("the warning is not one JSON object: %v (%s)", err, buf.String())
	}
	return got
}

func oidcConfig(issuer string) *config.Config {
	return &config.Config{OIDCEnabled: true, OIDCIssuer: issuer}
}

// The case that cost six days. The issuer moved, every login after it created a
// new principal, and the admin role stayed on the old one — with nothing said
// at any point.
func TestAChangedIssuerIsReportedAtStartup(t *testing.T) {
	t.Parallel()

	got := warn(t, oidcConfig(byName), &fakeIssuers{issuers: []string{byIP}})

	if got == nil {
		t.Fatal("an issuer matching no existing user started silently")
	}
	if got["configured_issuer"] != byName {
		t.Errorf("configured_issuer = %v, want %q", got["configured_issuer"], byName)
	}

	// Naming what is already there is the actionable half: it says which issuer
	// the existing memberships belong to, which is where they have to be moved
	// from.
	known, ok := got["known_issuers"].([]any)
	if !ok || len(known) != 1 || known[0] != byIP {
		t.Errorf("known_issuers = %v, want [%q]", got["known_issuers"], byIP)
	}
	if got["level"] != "WARN" {
		t.Errorf("level = %v, want WARN — this must survive a production log filter", got["level"])
	}
}

// The normal restart, which happens far more often than a change. A warning
// here would be printed on every deploy and learned as noise, and then the real
// one would not be read either.
func TestAKnownIssuerIsSilent(t *testing.T) {
	t.Parallel()

	if got := warn(t, oidcConfig(byName), &fakeIssuers{issuers: []string{byIP, byName}}); got != nil {
		t.Errorf("a configured issuer that already has users warned anyway: %v", got)
	}
}

// A first deployment has no users at all. Everything is about to be new, which
// is not a change and must not be reported as one.
func TestAnEmptyDatabaseIsSilent(t *testing.T) {
	t.Parallel()

	for _, issuers := range [][]string{nil, {}} {
		if got := warn(t, oidcConfig(byName), &fakeIssuers{issuers: issuers}); got != nil {
			t.Errorf("a fresh install warned: %v", got)
		}
	}
}

// Nothing to compare against, so the query is not worth making. It also keeps
// the warning honest: with OIDC off, every principal comes from the dev token
// and its issuer never changes.
func TestTheCheckIsSkippedWhenOIDCIsDisabled(t *testing.T) {
	t.Parallel()

	s := &fakeIssuers{issuers: []string{byIP}}
	cfg := &config.Config{OIDCEnabled: false, OIDCIssuer: byName}

	if got := warn(t, cfg, s); got != nil {
		t.Errorf("a brain with OIDC disabled warned about its issuer: %v", got)
	}
	if s.calls != 0 {
		t.Errorf("the database was queried %d times with OIDC disabled", s.calls)
	}
}

// A diagnostic must never be the reason a brain fails to start. It says what it
// could not check rather than pretending the issuer is known.
func TestAFailedLookupWarnsAndDoesNotStopStartup(t *testing.T) {
	t.Parallel()

	got := warn(t, oidcConfig(byName), &fakeIssuers{err: errors.New("connection refused")})

	if got == nil {
		t.Fatal("a failed check passed in silence — it looks identical to a known issuer")
	}
	if got["error"] != "connection refused" {
		t.Errorf("error = %v, want the cause", got["error"])
	}
	// The failure must not be dressed up as the finding: acting on
	// "your issuer changed" when nothing was read would send somebody
	// migrating memberships that are fine.
	if _, claimed := got["known_issuers"]; claimed {
		t.Errorf("a failed lookup reported known_issuers anyway: %v", got)
	}
}

// The predicate on its own, including the boundary the wiring cannot reach:
// an unset issuer with users present is a misconfiguration for a different
// check to report, not a change of issuer.
func TestIssuerIsNew(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		configured string
		known      []string
		want       bool
	}{
		{"absent from a populated database", byName, []string{byIP}, true},
		{"present", byName, []string{byIP, byName}, false},
		{"the only one", byName, []string{byName}, false},
		{"no users yet", byName, nil, false},
		{"unset issuer, no users", "", nil, false},
		{"unset issuer with users", "", []string{byIP}, true},
	} {
		if got := issuerIsNew(tc.configured, tc.known); got != tc.want {
			t.Errorf("%s: issuerIsNew(%q, %v) = %v, want %v",
				tc.name, tc.configured, tc.known, got, tc.want)
		}
	}
}
