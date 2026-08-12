package authconfig

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

const devToken = "s3cr3t-shared-token"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func call(t *testing.T, cfg *config.Config, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), cfg).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

func get(t *testing.T, cfg *config.Config) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	rec := call(t, cfg, http.MethodGet, "/auth/config")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, rec.Body.String())
	}
	return rec, body
}

func developmentConfig() *config.Config {
	return &config.Config{
		Environment: config.EnvironmentDevelopment,
		DevToken:    devToken,
	}
}

func oidcConfigured() *config.Config {
	return &config.Config{
		Environment:  config.EnvironmentProduction,
		OIDCEnabled:  true,
		OIDCIssuer:   "https://idp.example.com/application/o/openarity/",
		OIDCAudience: "a-client-id",
	}
}

// The point of the endpoint: a client with no credential learns how to get
// one. If it needed a token first, it could never be the first call.
func TestTheRouterIsPublic(t *testing.T) {
	t.Parallel()

	if !New(discardLogger(), developmentConfig()).Public() {
		t.Error("the auth config router is not public, so a client with no token cannot read it")
	}
}

// Being public is exactly why the surface stays at one route. Anything added
// here is served to anyone who can reach the port.
func TestOnlyOneRouteIsPublic(t *testing.T) {
	t.Parallel()

	got := New(discardLogger(), developmentConfig()).Patterns()
	want := []string{"GET /auth/config"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Patterns() = %v, want %v", got, want)
	}
}

// Nothing here writes, so nothing but GET should answer. A POST falling
// through to a handler would be an unauthenticated write.
func TestUndeclaredMethodsDoNotAnswer(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := call(t, developmentConfig(), method, "/auth/config"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /auth/config = %d, want 405", method, rec.Code)
		}
	}
}

// This is the one that matters. The response is served to anyone who can
// reach the port, and the configuration it is built from holds a shared
// token, a database password and a Vault address. A field added to Config and
// copied here by reflex would publish all three.
func TestTheResponseCarriesNoSecret(t *testing.T) {
	t.Parallel()

	cfg := developmentConfig()
	cfg.PostgresDSN = "postgres://postgres:hunter2@localhost:5432/openarity"
	cfg.VaultAddr = "http://vault.internal:8200"
	cfg.SuperAdmins = []string{"akadmin"}

	rec, _ := get(t, cfg)
	body := rec.Body.String()

	for _, secret := range []string{devToken, "hunter2", "vault.internal", "akadmin"} {
		if strings.Contains(body, secret) {
			t.Errorf("the response contains %q: %s", secret, body)
		}
	}
}

// The keys are the contract. A rename is a breaking change for every client
// already deployed, and the spec is what says so.
func TestTheResponseCarriesExactlyTheDocumentedKeys(t *testing.T) {
	t.Parallel()

	_, body := get(t, developmentConfig())

	want := map[string]bool{"environment": true, "dev_token_accepted": true}
	for key := range body {
		if !want[key] {
			t.Errorf("undocumented key %q in the response: %v", key, body)
		}
	}
	for key := range want {
		if _, ok := body[key]; !ok {
			t.Errorf("key %q is missing: %v", key, body)
		}
	}
}

// A client in development can authenticate with the shared token it already
// has in its environment, and this flag is how it knows to try. Reporting it
// as accepted when it is not sends the CLI down a path that returns 401 with
// nothing explaining why.
func TestDevTokenIsReportedAcceptedInDevelopment(t *testing.T) {
	t.Parallel()

	_, body := get(t, developmentConfig())

	if body["dev_token_accepted"] != true {
		t.Errorf("dev_token_accepted = %v, want true", body["dev_token_accepted"])
	}
	if body["environment"] != string(config.EnvironmentDevelopment) {
		t.Errorf("environment = %v, want development", body["environment"])
	}
}

// No token configured is the normal case, and a client told one is accepted
// would send an empty string and get a 401.
func TestDevTokenIsNotAcceptedWhenUnset(t *testing.T) {
	t.Parallel()

	cfg := developmentConfig()
	cfg.DevToken = ""

	_, body := get(t, cfg)

	if body["dev_token_accepted"] != false {
		t.Errorf("dev_token_accepted = %v, want false with no token configured", body["dev_token_accepted"])
	}
}

// config.Load already refuses this combination at startup, so it cannot
// happen through the front door. It is asserted anyway because this response
// is what a client trusts: if the check here were only implied by the one in
// Load, moving Load would silently move this too.
func TestDevTokenIsNeverAcceptedOutsideDevelopment(t *testing.T) {
	t.Parallel()

	for _, environment := range []config.Environment{config.EnvironmentStaging, config.EnvironmentProduction} {
		cfg := &config.Config{Environment: environment, DevToken: devToken}

		_, body := get(t, cfg)

		if body["dev_token_accepted"] != false {
			t.Errorf("%s: dev_token_accepted = %v, want false", environment, body["dev_token_accepted"])
		}
	}
}

// The CLI needs an issuer and a client id to start a device flow. Hard-coding
// them in the client is a second copy of the brain's configuration that drifts
// the moment either moves — which is exactly what happened by hand during QA.
func TestOIDCParametersAreServedWhenEnabled(t *testing.T) {
	t.Parallel()

	cfg := oidcConfigured()

	_, body := get(t, cfg)

	oidc, ok := body["oidc"].(map[string]any)
	if !ok {
		t.Fatalf("oidc is missing or not an object: %v", body)
	}
	if oidc["issuer"] != cfg.OIDCIssuer {
		t.Errorf("issuer = %v, want %q", oidc["issuer"], cfg.OIDCIssuer)
	}
	if oidc["client_id"] != cfg.OIDCAudience {
		t.Errorf("client_id = %v, want %q", oidc["client_id"], cfg.OIDCAudience)
	}
}

// Absent rather than an empty object. A client checking "is oidc set" on a
// present-but-blank issuer builds a discovery URL of ".well-known/..." and
// fails somewhere far from the cause.
func TestOIDCIsAbsentWhenDisabled(t *testing.T) {
	t.Parallel()

	_, body := get(t, developmentConfig())

	if _, ok := body["oidc"]; ok {
		t.Errorf("oidc is present with OIDC disabled: %v", body)
	}
}

// The audience is configured with a default of "openarity", so it is never
// empty — but the issuer is, and an enabled provider with no issuer cannot
// start a flow. config.Load refuses that combination; this pins that the
// response does not invent one.
func TestTheIssuerIsServedVerbatim(t *testing.T) {
	t.Parallel()

	cfg := oidcConfigured()
	cfg.OIDCIssuer = "https://idp.example.com/application/o/openarity/"

	_, body := get(t, cfg)

	oidc, ok := body["oidc"].(map[string]any)
	if !ok {
		t.Fatalf("oidc is missing or not an object: %v", body)
	}
	if oidc["issuer"] != cfg.OIDCIssuer {
		t.Errorf("issuer = %v, want it verbatim including the trailing slash", oidc["issuer"])
	}
}

// Built once at construction from a copy, so a later mutation of the config
// cannot leak through a retained pointer. Nothing mutates Config today; this
// fails the day something does.
func TestTheResponseDoesNotFollowLaterConfigChanges(t *testing.T) {
	t.Parallel()

	cfg := developmentConfig()
	router := New(discardLogger(), cfg)

	cfg.DevToken = ""
	cfg.OIDCEnabled = true
	cfg.OIDCIssuer = "https://later.example.com/"

	mux := http.NewServeMux()
	router.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/config", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body["dev_token_accepted"] != true {
		t.Error("the response changed after the config was mutated — it is holding a pointer")
	}
	if _, ok := body["oidc"]; ok {
		t.Error("the response gained an oidc block after the config was mutated")
	}
}

func TestTheResponseIsJSON(t *testing.T) {
	t.Parallel()

	rec, _ := get(t, developmentConfig())

	if ct := rec.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
