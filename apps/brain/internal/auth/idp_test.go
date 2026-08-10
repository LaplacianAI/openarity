package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A test identity provider: discovery document, JWKS, and the private keys to
// sign tokens with. Everything the OIDC verifier talks to, in-process, so the
// tests never need a real IdP and can do things a real one never would.

// RSA key generation is slow, so the whole package shares one set. Three keys:
// two published, one held back to forge tokens from an unknown key.
var testKeys = sync.OnceValue(func() []*rsa.PrivateKey {
	keys := make([]*rsa.PrivateKey, 3)
	for i := range keys {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		keys[i] = k
	}
	return keys
})

type signer struct {
	kid string
	key *rsa.PrivateKey
}

type idp struct {
	srv *httptest.Server

	mu        sync.Mutex
	published []signer // keys served in the JWKS
	raw       []map[string]any
	delay     time.Duration // artificial JWKS latency
	jwksDown  bool
	jwksHits  int
	discovery func(issuer string) any // nil means the standard document
}

func newIDP(t *testing.T) *idp {
	t.Helper()

	k := testKeys()
	s := &idp{published: []signer{{kid: "key-1", key: k[0]}}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *idp) URL() string { return s.srv.URL }

func (s *idp) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		s.mu.Lock()
		build := s.discovery
		s.mu.Unlock()

		var doc any = map[string]any{
			"issuer":                                s.srv.URL,
			"jwks_uri":                              s.srv.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		if build != nil {
			doc = build(s.srv.URL)
		}
		writeJSON(w, doc)

	case "/keys":
		s.mu.Lock()
		s.jwksHits++
		delay, down := s.delay, s.jwksDown
		keys := make([]any, 0, len(s.published))
		for _, p := range s.published {
			keys = append(keys, jwk(p))
		}
		for _, r := range s.raw {
			keys = append(keys, r)
		}
		s.mu.Unlock()

		// Abort the delay as soon as the client gives up, so a slow-JWKS test
		// does not hold httptest.Server.Close open for the full duration.
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		if down {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"keys": keys})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jwk(p signer) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": p.kid,
		"n":   b64(p.key.N.Bytes()),
		"e":   b64(big.NewInt(int64(p.key.E)).Bytes()),
	}
}

// publish replaces the JWKS contents. Used to rotate keys mid-flight.
func (s *idp) publish(keys ...signer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published = keys
}

func (s *idp) publishRaw(keys ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = keys
}

func (s *idp) setDiscovery(build func(issuer string) any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discovery = build
}

func (s *idp) setDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

func (s *idp) setJWKSDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jwksDown = down
}

func (s *idp) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jwksHits
}

// key returns one of the shared keys by index, with the kid the IdP uses for it.
func (s *idp) key(i int) signer {
	return signer{kid: []string{"key-1", "key-2", "key-3"}[i], key: testKeys()[i]}
}

// claims builds a valid claim set, then applies overrides. A nil override
// value deletes the claim, which is how tests drop exp, aud or email.
func (s *idp) claims(overrides map[string]any) map[string]any {
	c := map[string]any{
		"iss":   s.srv.URL,
		"sub":   "user-42",
		"aud":   "openarity",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Add(-time.Minute).Unix(),
		"email": "someone@example.com",
	}
	for k, v := range overrides {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

// sign produces an RS256 JWT. This is deliberately hand-rolled rather than
// done with a JWT library: the tests need to emit tokens no library would
// willingly produce.
func sign(t *testing.T, p signer, claims map[string]any) string {
	t.Helper()
	return signAs(t, p, "RS256", claims)
}

func signAs(t *testing.T, p signer, alg string, claims map[string]any) string {
	t.Helper()

	header := map[string]any{"alg": alg, "typ": "JWT"}
	if p.kid != "" {
		header["kid"] = p.kid
	}
	input := encodeSegment(t, header) + "." + encodeSegment(t, claims)

	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return input + "." + b64(sig)
}

func encodeSegment(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal segment: %v", err)
	}
	return b64(raw)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// publicKeyBytes is what an attacker attempting an alg-confusion forgery has:
// the signing key's public half, which the JWKS hands to anyone who asks.
func publicKeyBytes(t *testing.T, k *rsa.PrivateKey) []byte {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return der
}
