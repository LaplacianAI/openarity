package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const audience = "openarity"

// newVerifier builds a verifier against a test IdP, failing the test if
// discovery does not succeed.
func newVerifier(t *testing.T, s *idp) Verifier {
	t.Helper()

	v, err := NewOIDCVerifier(t.Context(), s.URL(), audience)
	if err != nil {
		t.Fatalf("NewOIDCVerifier: %v", err)
	}
	return v
}

// rejects asserts a token is refused, and refused as ErrUnauthenticated so the
// middleware can recognise it.
func rejects(t *testing.T, v Verifier, token, why string) {
	t.Helper()

	p, err := v.Verify(t.Context(), token)
	if err == nil {
		t.Fatalf("%s: accepted, returned %+v", why, p)
	}
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("%s: error does not match ErrUnauthenticated: %v", why, err)
	}
	if p != nil {
		t.Errorf("%s: returned a principal alongside the error: %+v", why, p)
	}
}

func TestOIDCAcceptsAValidToken(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(nil))
	p, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	want := Principal{
		Kind:    KindUser,
		Issuer:  s.URL(),
		Subject: "user-42",
		Email:   "someone@example.com",
	}
	if *p != want {
		t.Errorf("Principal = %+v, want %+v", *p, want)
	}
}

// The subject is what step 7 stores in users. It must come from the token, not
// from anywhere else.
func TestOIDCCarriesTheSubjectThrough(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(map[string]any{"sub": "|special|sub|"}))
	p, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Subject != "|special|sub|" {
		t.Errorf("Subject = %q, want the sub claim verbatim", p.Subject)
	}
}

// email is an optional claim. Its absence is not an authentication failure —
// the caller is still who they say they are.
func TestOIDCAcceptsATokenWithoutEmail(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(map[string]any{"email": nil}))
	p, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Email != "" {
		t.Errorf("Email = %q, want empty", p.Email)
	}
	if p.Subject != "user-42" {
		t.Errorf("Subject = %q, want the token still to identify the caller", p.Subject)
	}
}

func TestOIDCRejectsAnExpiredToken(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(map[string]any{
		"exp": time.Now().Add(-time.Second).Unix(),
	}))
	rejects(t, v, token, "expired one second ago")
}

func TestOIDCRejectsATokenThatIsNotYetValid(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(map[string]any{
		"nbf": time.Now().Add(time.Hour).Unix(),
	}))
	rejects(t, v, token, "not valid for another hour")
}

// A token minted by our own IdP, for a different application. Signature valid,
// issuer valid, wrong audience. Without the aud check this authenticates.
func TestOIDCRejectsATokenForAnotherAudience(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(map[string]any{"aud": "some-other-app"}))
	rejects(t, v, token, "audience is another application")
}

func TestOIDCRejectsATokenWithNoAudience(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(map[string]any{"aud": nil}))
	rejects(t, v, token, "audience claim absent")
}

// Two IdPs, one verifier. A token from the wrong one must not be accepted even
// though it is perfectly well formed and correctly signed by its own issuer.
func TestOIDCRejectsATokenFromAnotherIssuer(t *testing.T) {
	t.Parallel()

	ours, theirs := newIDP(t), newIDP(t)
	v := newVerifier(t, ours)

	token := sign(t, theirs.key(0), theirs.claims(nil))
	rejects(t, v, token, "signed and issued by a different provider")
}

// The classic forgery: strip the signature and claim the token needs none.
func TestOIDCRejectsAlgNone(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	header := encodeSegment(t, map[string]any{"alg": "none", "typ": "JWT", "kid": "key-1"})
	payload := encodeSegment(t, s.claims(nil))
	rejects(t, v, header+"."+payload+".", "alg none, empty signature")
}

// Algorithm confusion. The signing key's public half is published in the JWKS,
// so an attacker has it. A verifier that accepts HS256 will use that public key
// as an HMAC secret, and the attacker can then mint any token they like.
func TestOIDCRejectsAnHMACForgeryUsingThePublicKey(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	secret := publicKeyBytes(t, s.key(0).key)
	header := encodeSegment(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": "key-1"})
	payload := encodeSegment(t, s.claims(map[string]any{"sub": "attacker"}))

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(header + "." + payload))
	token := header + "." + payload + "." + b64(mac.Sum(nil))

	rejects(t, v, token, "HS256 signed with the published public key")
}

// The same forgery, but against an IdP whose discovery document advertises
// HS256 — a misconfiguration, or an IdP whose metadata an attacker controls.
//
// Leaving SupportedSigningAlgs empty makes go-oidc adopt whatever the document
// lists (verify.go:137), so without the pin in oidc.go the IdP's own JSON
// decides which algorithms the brain accepts. That the forgery still fails is
// down to a second, structural defence — see the test below.
func TestOIDCRejectsHMACEvenWhenTheIdPAdvertisesIt(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	s.setDiscovery(advertising("RS256", "HS256"))
	v := newVerifier(t, s)

	secret := publicKeyBytes(t, s.key(0).key)
	rejects(t, v, hmacToken(t, s, "key-1", secret),
		"HS256 forgery against an IdP that advertises HS256")
}

// The strongest form of the attack: the IdP advertises HS256 *and* publishes a
// symmetric key for it, so the attacker's HMAC would verify against a key in
// the JWKS. This still fails, and it is worth knowing exactly why.
//
// go-oidc's allAlgs (jose.go:21) lists only asymmetric algorithms, and
// jwkJSON.UnmarshalJSON drops any JWKS entry whose alg is outside that set
// (jwks.go:274). An HS256 key therefore never enters the key set, so no key
// exists that could verify the signature.
//
// So the SupportedSigningAlgs pin is defence in depth here, not the thing
// holding the door shut — removing it does not make any of these tests fail.
// Keep it anyway: it costs nothing and it is what protects us if the library's
// defaults ever widen. This test is the regression guard for that day.
func TestOIDCRejectsHMACEvenWhenTheIdPPublishesASymmetricKey(t *testing.T) {
	t.Parallel()

	secret := []byte("a-shared-symmetric-secret-32-byt")

	s := newIDP(t)
	s.setDiscovery(advertising("RS256", "HS256"))
	s.publishRaw(map[string]any{
		"kty": "oct", "alg": "HS256", "use": "sig", "kid": "hmac-1", "k": b64(secret),
	})
	v := newVerifier(t, s)

	rejects(t, v, hmacToken(t, s, "hmac-1", secret),
		"HS256 forgery against a key the IdP actually published")
}

// advertising builds a discovery document listing the given signing algorithms.
func advertising(algs ...string) func(string) any {
	return func(issuer string) any {
		return map[string]any{
			"issuer":                                issuer,
			"jwks_uri":                              issuer + "/keys",
			"id_token_signing_alg_values_supported": algs,
		}
	}
}

// hmacToken forges an HS256 token for an attacker-chosen subject.
func hmacToken(t *testing.T, s *idp, kid string, secret []byte) string {
	t.Helper()

	header := encodeSegment(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": kid})
	payload := encodeSegment(t, s.claims(map[string]any{"sub": "attacker"}))

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(header + "." + payload))
	return header + "." + payload + "." + b64(mac.Sum(nil))
}

// A valid token with the payload edited afterwards. The signature no longer
// matches, which is the entire security property.
func TestOIDCRejectsATamperedPayload(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(0), s.claims(nil))
	parts := strings.Split(token, ".")

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	claims["sub"] = "root"

	rejects(t, v, parts[0]+"."+encodeSegment(t, claims)+"."+parts[2], "payload edited after signing")
}

// Signed by a key the IdP has never published. go-oidc will refetch the JWKS
// looking for the kid, not find it, and reject.
func TestOIDCRejectsATokenSignedByAnUnpublishedKey(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	token := sign(t, s.key(2), s.claims(nil))
	rejects(t, v, token, "kid is not in the JWKS")
}

// Rotation: the IdP starts signing with a new key and publishes it. Tokens
// from the new key must be accepted without restarting the brain.
func TestOIDCPicksUpARotatedKey(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	if _, err := v.Verify(t.Context(), sign(t, s.key(0), s.claims(nil))); err != nil {
		t.Fatalf("token from the original key: %v", err)
	}

	s.publish(s.key(1))
	token := sign(t, s.key(1), s.claims(nil))
	if _, err := v.Verify(t.Context(), token); err != nil {
		t.Fatalf("token from the rotated key: %v", err)
	}
}

// The verifier must not be pinned to one key forever, but it must also not
// refetch on every single request — that would put the IdP in the hot path.
func TestOIDCCachesTheKeySet(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	for range 5 {
		if _, err := v.Verify(t.Context(), sign(t, s.key(0), s.claims(nil))); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	}
	if got := s.hits(); got != 1 {
		t.Errorf("JWKS fetched %d times for 5 verifications, want 1", got)
	}
}

func TestOIDCRejectsWhenTheJWKSIsDown(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)
	s.setJWKSDown(true)

	rejects(t, v, sign(t, s.key(0), s.claims(nil)), "JWKS returns 500")
}

// A slow JWKS must not hang the request. The caller's context bounds it, which
// means an unresponsive IdP costs a timeout, not a stuck goroutine.
func TestOIDCRespectsTheCallerDeadlineWhenTheJWKSIsSlow(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)
	s.setDelay(2 * time.Second)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := v.Verify(ctx, sign(t, s.key(0), s.claims(nil))); err == nil {
		t.Fatal("a slow JWKS produced a successful verification")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Verify took %v, the caller deadline was 100ms", elapsed)
	}
}

func TestOIDCRejectsMalformedTokens(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	v := newVerifier(t, s)

	for _, tc := range []struct{ name, token string }{
		{"empty", ""},
		{"not a jwt", "hello"},
		{"two segments", "aaa.bbb"},
		{"four segments", "aaa.bbb.ccc.ddd"},
		{"not base64", "!!!.???.***"},
		{"empty segments", ".."},
		{"header only", "eyJhbGciOiJSUzI1NiJ9.."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rejects(t, v, tc.token, tc.name)
		})
	}
}

// Discovery happens at construction, so an unreachable IdP is a startup
// failure rather than a request that fails later.
func TestNewOIDCVerifierFailsWhenDiscoveryIsUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	if _, err := NewOIDCVerifier(t.Context(), srv.URL, audience); err == nil {
		t.Fatal("constructed a verifier against an IdP that cannot serve discovery")
	}
}

func TestNewOIDCVerifierFailsOnAnIssuerMismatch(t *testing.T) {
	t.Parallel()

	s := newIDP(t)
	s.setDiscovery(func(issuer string) any {
		return map[string]any{
			"issuer":   "https://evil.example.com",
			"jwks_uri": issuer + "/keys",
		}
	})

	if _, err := NewOIDCVerifier(t.Context(), s.URL(), audience); err == nil {
		t.Fatal("accepted a discovery document claiming a different issuer")
	}
}

func TestNewOIDCVerifierFailsOnAnUnparseableDiscoveryDocument(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	t.Cleanup(srv.Close)

	if _, err := NewOIDCVerifier(t.Context(), srv.URL, audience); err == nil {
		t.Fatal("accepted a discovery document that is not JSON")
	}
}

// The error must name the issuer. When the brain refuses to boot, the operator
// needs to know which URL it could not reach.
func TestNewOIDCVerifierErrorNamesTheIssuer(t *testing.T) {
	t.Parallel()

	const issuer = "http://127.0.0.1:1/nowhere"
	_, err := NewOIDCVerifier(t.Context(), issuer, audience)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), issuer) {
		t.Errorf("error does not name the issuer: %v", err)
	}
}
