package auth

import (
	"errors"
	"strings"
	"testing"
)

const devToken = "s3cret-development-token"

func devVerifierFor(t *testing.T, token string) Verifier {
	t.Helper()

	v, err := NewDevVerifier(token)
	if err != nil {
		t.Fatalf("NewDevVerifier: %v", err)
	}
	return v
}

func TestDevVerifierAcceptsTheConfiguredToken(t *testing.T) {
	t.Parallel()

	p, err := devVerifierFor(t, devToken).Verify(t.Context(), devToken)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	want := Principal{Kind: KindDev, Subject: "dev"}
	if *p != want {
		t.Errorf("Principal = %+v, want %+v", *p, want)
	}
}

// KindDev is what lets everything downstream tell the static token apart from
// a real login. If the dev verifier returned KindUser the audit log would
// record a human who never logged in.
func TestDevVerifierDoesNotImpersonateAUser(t *testing.T) {
	t.Parallel()

	p, err := devVerifierFor(t, devToken).Verify(t.Context(), devToken)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Kind != KindDev {
		t.Errorf("Kind = %q, want %q", p.Kind, KindDev)
	}
	if p.Issuer != "" {
		t.Errorf("Issuer = %q, want empty — no identity provider was involved", p.Issuer)
	}
	if p.Email != "" {
		t.Errorf("Email = %q, want empty", p.Email)
	}
}

func TestDevVerifierRejectsEverythingElse(t *testing.T) {
	t.Parallel()

	v := devVerifierFor(t, devToken)

	for _, tc := range []struct{ name, token string }{
		{"empty", ""},
		{"wrong", "not-the-token"},
		{"prefix", devToken[:len(devToken)-1]},
		{"suffix", devToken + "x"},
		{"leading space", " " + devToken},
		{"trailing space", devToken + " "},
		{"case changed", strings.ToUpper(devToken)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, err := v.Verify(t.Context(), tc.token)
			if err == nil {
				t.Fatalf("accepted %q, returned %+v", tc.token, p)
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("error does not match ErrUnauthenticated: %v", err)
			}
			if p != nil {
				t.Errorf("returned a principal alongside the error: %+v", p)
			}
		})
	}
}

// An empty configured token would authenticate every request arriving with an
// empty bearer. The constructor is the only place this can be caught, because
// the struct is unexported.
func TestNewDevVerifierRejectsAnEmptyToken(t *testing.T) {
	t.Parallel()

	v, err := NewDevVerifier("")
	if err == nil {
		t.Fatalf("constructed a dev verifier with an empty token: %+v", v)
	}
	if v != nil {
		t.Errorf("returned a verifier alongside the error: %+v", v)
	}
}

// The comparison must not short-circuit on the first differing byte. This
// cannot be asserted by timing in a unit test without being flaky, so assert
// the property that makes it true: tokens differing only in the last byte are
// rejected exactly like tokens differing in the first.
func TestDevVerifierComparesTheWholeToken(t *testing.T) {
	t.Parallel()

	v := devVerifierFor(t, devToken)

	first := "X" + devToken[1:]
	last := devToken[:len(devToken)-1] + "X"

	for _, token := range []string{first, last} {
		if _, err := v.Verify(t.Context(), token); err == nil {
			t.Errorf("accepted %q", token)
		}
	}
}

// cmd/brain checks DevToken != "" before calling NewDevVerifier, so the error
// it handles there can never fire. That is only true while an empty token is
// the sole reason to fail. If this constructor grows a second rule — a minimum
// length, a character set — this test fails and the caller has a live branch it
// is not testing.
func TestNewDevVerifierFailsOnlyOnAnEmptyToken(t *testing.T) {
	t.Parallel()

	for _, token := range []string{
		"x", " ", "\n", strings.Repeat("a", 4096),
		"tab\there", "unicode-ü", `{"json":true}`,
	} {
		if _, err := NewDevVerifier(token); err != nil {
			t.Errorf("NewDevVerifier(%q) = %v, want no error — only an empty token may fail", token, err)
		}
	}
}
