package config

import (
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

// What validate.go guards. A test-only import of internal/auth pins it to the
// value the dev verifier really produces; production config still imports
// nothing.
const devSubject = "dev"

// devEnv is a development environment with a dev token set — the only
// combination that is legal.
func devEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"OPENARITY_ENVIRONMENT": string(EnvironmentDevelopment),
		"OPENARITY_DEV_TOKEN":   "local-dev-token",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// The whole point of the dev token is that it is a credential with no
// verification behind it. Anywhere but a laptop that is a backdoor, so the
// process must refuse to start rather than serve — a request-time check would
// mean a misconfigured deployment runs happily until someone tries it.
func TestDevTokenRefusedOutsideDevelopment(t *testing.T) {
	t.Parallel()

	for _, envName := range []Environment{
		EnvironmentProduction, EnvironmentStaging, EnvironmentTest,
	} {
		cfg, err := load(map[string]string{
			"OPENARITY_ENVIRONMENT": string(envName),
			"OPENARITY_DEV_TOKEN":   "shhh",
		})
		if err == nil {
			t.Errorf("ENVIRONMENT=%s accepted a DEV_TOKEN, got %+v", envName, cfg)
			continue
		}
		if !strings.Contains(err.Error(), "DEV_TOKEN") {
			t.Errorf("ENVIRONMENT=%s: error does not name DEV_TOKEN: %v", envName, err)
		}
		// An operator's first question is what the process thinks the
		// environment is — usually the answer is that it was never set.
		if !strings.Contains(err.Error(), string(envName)) {
			t.Errorf("ENVIRONMENT=%s: error does not say which environment: %v", envName, err)
		}
	}
}

func TestDevTokenAcceptedInDevelopment(t *testing.T) {
	t.Parallel()

	cfg, err := load(devEnv(nil))
	if err != nil {
		t.Fatalf("development with a dev token was rejected: %v", err)
	}
	if cfg.DevToken != "local-dev-token" {
		t.Errorf("DevToken = %q, want the value that was set", cfg.DevToken)
	}
}

// No dev token is the normal production shape and must pass everywhere.
func TestNoDevTokenIsValidInEveryEnvironment(t *testing.T) {
	t.Parallel()

	for _, envName := range []Environment{
		EnvironmentDevelopment, EnvironmentProduction, EnvironmentStaging, EnvironmentTest,
	} {
		env := map[string]string{"OPENARITY_ENVIRONMENT": string(envName)}
		if envName != EnvironmentDevelopment {
			// The secret store and the object store are both mandatory outside
			// development. Supplying them keeps this test about the DEV_TOKEN
			// rule rather than about whichever requirement was added most
			// recently.
			env["OPENARITY_SECRETS_BACKEND"] = "openbao"
			env["OPENARITY_SECRETS_APPROLE_ID"] = "role-abc"
			env["OPENARITY_SECRETS_APPROLE_SECRET"] = "approlesecret"
			env["OPENARITY_OBJECTS_BACKEND"] = "s3"
			env["OPENARITY_OBJECTS_ENDPOINT"] = "http://minio:9000"
		}

		if _, err := load(env); err != nil {
			t.Errorf("ENVIRONMENT=%s with no DEV_TOKEN was rejected: %v", envName, err)
		}
	}
}

// The two verifiers are independent: OIDC for real deployments, the dev token
// for local work without an identity provider. Both on is a development
// machine exercising the real path and still able to curl.
func TestDevTokenAndOIDCCoexistInDevelopment(t *testing.T) {
	t.Parallel()

	cfg, err := load(devEnv(map[string]string{
		"OPENARITY_OIDC_ENABLED":  "true",
		"OPENARITY_OIDC_ISSUER":   "https://auth.example.com/application/o/openarity/",
		"OPENARITY_OIDC_AUDIENCE": "openarity",
	}))
	if err != nil {
		t.Fatalf("OIDC and a dev token together in development: %v", err)
	}
	if !cfg.OIDCEnabled || cfg.DevToken == "" {
		t.Errorf("expected both verifiers configured, got OIDCEnabled=%t DevToken=%q",
			cfg.OIDCEnabled, cfg.DevToken)
	}
}

// The issuer is only meaningful once OIDC is on. Checking it unconditionally
// would make the default config invalid, since the default issuer is empty.
func TestOIDCIssuerCheckedOnlyWhenEnabled(t *testing.T) {
	t.Parallel()

	bad := map[string]string{
		"empty":        "",
		"no scheme":    "auth.example.com",
		"no host":      "https://",
		"wrong scheme": "redis://auth.example.com",
	}

	for name, issuer := range bad {
		_, err := load(map[string]string{
			"OPENARITY_OIDC_ENABLED": "true",
			"OPENARITY_OIDC_ISSUER":  issuer,
		})
		if err == nil {
			t.Errorf("OIDC enabled with a %s issuer %q was accepted", name, issuer)
			continue
		}
		if !strings.Contains(err.Error(), "OIDC_ISSUER") {
			t.Errorf("%s issuer: error does not name OIDC_ISSUER: %v", name, err)
		}
	}

	// Same values, OIDC off: nothing reads the issuer, so nothing may reject it.
	for name, issuer := range bad {
		if _, err := load(map[string]string{
			"OPENARITY_OIDC_ENABLED": "false",
			"OPENARITY_OIDC_ISSUER":  issuer,
		}); err != nil {
			t.Errorf("OIDC disabled rejected a %s issuer %q: %v", name, issuer, err)
		}
	}
}

// An empty audience means every token for every application on that issuer is
// accepted — the check that stops a token minted for the dashboard being
// replayed against something else.
//
// Validate is called directly rather than through load, because an
// empty-but-set variable falls back to its envDefault: OPENARITY_OIDC_AUDIENCE=""
// yields "openarity", so the environment cannot produce this state. The branch
// still earns its place — it guards a Config built in code, which is exactly
// what a test or a future non-env loader does.
func TestOIDCAudienceRequiredWhenEnabled(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Environment:  EnvironmentDevelopment,
		APIBind:      "127.0.0.1:21120",
		WebhookBind:  "127.0.0.1:21121",
		PostgresDSN:  "postgres://localhost:5432/openarity",
		FalkorDBURL:  "redis://127.0.0.1:6380",
		RedisURL:     "redis://127.0.0.1:6379",
		SecretsAddr:  "http://localhost:8200",
		OmniRouteURL: "http://localhost:20128/v1",
		OIDCEnabled:  true,
		OIDCIssuer:   "https://auth.example.com/application/o/openarity/",
		OIDCAudience: "",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("OIDC enabled with an empty audience was accepted")
	}
	if !strings.Contains(err.Error(), "OIDC_AUDIENCE") {
		t.Errorf("error does not name OIDC_AUDIENCE: %v", err)
	}
}

// Setting the variable to empty is not a way to clear it — env falls back to
// the default. Worth pinning, because it is the reason the test above cannot
// go through load, and the same surprise applies to every field.
func TestEmptyAudienceVariableFallsBackToTheDefault(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OIDC_ENABLED":  "true",
		"OPENARITY_OIDC_ISSUER":   "https://auth.example.com/application/o/openarity/",
		"OPENARITY_OIDC_AUDIENCE": "",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.OIDCAudience != "openarity" {
		t.Errorf("OIDCAudience = %q, want the default", cfg.OIDCAudience)
	}
}

// A valid OIDC configuration must load, or none of the above proves anything —
// a Validate that always errored would satisfy every rejection test here.
func TestOIDCEnabledAcceptsAValidConfiguration(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OIDC_ENABLED":  "true",
		"OPENARITY_OIDC_ISSUER":   "https://auth.example.com/application/o/openarity/",
		"OPENARITY_OIDC_AUDIENCE": "openarity",
	})
	if err != nil {
		t.Fatalf("a valid OIDC configuration was rejected: %v", err)
	}
	if cfg.OIDCIssuer == "" || cfg.OIDCAudience == "" {
		t.Errorf("issuer or audience lost in load: %+v", cfg)
	}
}

// SUPER_ADMINS grants platform administration, so how it splits matters.
// Recorded behaviour, not endorsed behaviour — see the whitespace case.
func TestSuperAdminsSplitting(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw  string
		want []string
	}{
		"empty":  {"", nil},
		"single": {"sub-abc", []string{"sub-abc"}},
		"three":  {"a,b,c", []string{"a", "b", "c"}},
	}

	for name, tt := range tests {
		cfg, err := load(map[string]string{"OPENARITY_SUPER_ADMINS": tt.raw})
		if err != nil {
			t.Errorf("%s: load(%q): %v", name, tt.raw, err)
			continue
		}
		if len(cfg.SuperAdmins) != len(tt.want) {
			t.Errorf("%s: %q gave %q, want %q", name, tt.raw, cfg.SuperAdmins, tt.want)
			continue
		}
		for i := range tt.want {
			if cfg.SuperAdmins[i] != tt.want[i] {
				t.Errorf("%s: entry %d = %q, want %q", name, i, cfg.SuperAdmins[i], tt.want[i])
			}
		}
	}
}

// env splits on commas and does nothing else, so a list written the way a
// human writes one produces " b" — an entry that can never match an OIDC
// subject. Accepting it would mean a super admin who silently has no power,
// with no error anywhere, and the recovery for having no working super admin
// is editing Postgres by hand. So it is refused at startup instead.
func TestSuperAdminsRejectsPaddedAndEmptyEntries(t *testing.T) {
	t.Parallel()

	bad := map[string]string{
		"space after a comma": "a, b",
		"space before":        " a,b",
		"empty entry":         "a,,b",
		"only whitespace":     "  ",
		"trailing comma":      "a,b,",
	}

	for name, raw := range bad {
		cfg, err := load(map[string]string{"OPENARITY_SUPER_ADMINS": raw})
		if err == nil {
			t.Errorf("%s: %q accepted, got %q", name, raw, cfg.SuperAdmins)
			continue
		}
		if !strings.Contains(err.Error(), "SUPER_ADMINS") {
			t.Errorf("%s: error does not name SUPER_ADMINS: %v", name, err)
		}
	}
}

// The error has to quote the entry and give its index — " b" and "b" are
// indistinguishable in a terminal otherwise, which is the entire failure being
// reported.
func TestSuperAdminsErrorIdentifiesTheEntry(t *testing.T) {
	t.Parallel()

	_, err := load(map[string]string{"OPENARITY_SUPER_ADMINS": "a, b"})
	if err == nil {
		t.Fatal("a padded entry was accepted")
	}
	for _, want := range []string{`" b"`, "entry 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %s: %v", want, err)
		}
	}
}

// Clean lists must still load, or the rejection tests above would pass against
// a check that refused everything.
func TestSuperAdminsAcceptsCleanEntries(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "sub-abc", "a,b,c"} {
		if _, err := load(map[string]string{"OPENARITY_SUPER_ADMINS": raw}); err != nil {
			t.Errorf("%q rejected: %v", raw, err)
		}
	}
}

// The list is read on every authentication path, not only the OIDC one — the
// dev-token verifier consults it too. A check nested inside `if c.OIDCEnabled`
// would skip it in local development and would let a typo sit unnoticed until
// somebody turned OIDC on, at which point startup fails for a reason unrelated
// to what they changed.
func TestSuperAdminsCheckedWithOIDCDisabled(t *testing.T) {
	t.Parallel()

	if _, err := load(map[string]string{
		"OPENARITY_OIDC_ENABLED": "false",
		"OPENARITY_SUPER_ADMINS": "a, b",
	}); err == nil {
		t.Error("a padded entry was accepted while OIDC was disabled")
	}
}

// The dev token is the one genuine secret in this struct, and String() exists
// so that printing a config is safe. It must never appear, whether the field
// is formatted and redacted or deliberately left out.
func TestStringNeverContainsTheDevToken(t *testing.T) {
	t.Parallel()

	const token = "dev-token-that-must-not-leak"

	cfg, err := load(devEnv(map[string]string{"OPENARITY_DEV_TOKEN": token}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if s := cfg.String(); strings.Contains(s, token) {
		t.Errorf("String() leaked the dev token: %s", s)
	}
}

// String() does not print the authentication settings at all today, so a test
// asserting "the issuer password does not appear" would pass without redaction
// ever running — a test that is green whether or not the feature exists.
//
// Assert the state that is actually true instead, and make the omission
// deliberate. If the fields are added to String(), this fails and is replaced
// by a redaction test with a positive assertion: the host survives, the
// password does not.
func TestStringOmitsTheAuthenticationSettings(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_OIDC_ENABLED": "true",
		"OPENARITY_OIDC_ISSUER":  "https://user:issuersecret@auth.example.com/o/openarity/",
		"OPENARITY_SUPER_ADMINS": "sub-abc,sub-def",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s := cfg.String()

	// Whatever else changes, the credential never appears.
	if strings.Contains(s, "issuersecret") {
		t.Errorf("String() leaked the issuer password: %s", s)
	}

	for _, field := range []string{"OIDCEnabled", "OIDCIssuer", "OIDCAudience", "SuperAdmins"} {
		if strings.Contains(s, field) {
			t.Errorf("String() now prints %s — add a redaction assertion for it "+
				"and delete this test: %s", field, s)
		}
	}
}

// oidcEnv is the combination that makes the two subject namespaces overlap:
// one identity provider, and a dev token whose subject is fixed.
func oidcEnv(extra map[string]string) map[string]string {
	return devEnv(mergeEnv(map[string]string{
		"OPENARITY_OIDC_ENABLED":  "true",
		"OPENARITY_OIDC_ISSUER":   "https://auth.example.com/application/o/openarity/",
		"OPENARITY_OIDC_AUDIENCE": "openarity",
	}, extra))
}

func mergeEnv(base, extra map[string]string) map[string]string {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// SUPER_ADMINS lists subjects with no issuer, which is right where a brain has
// one identity provider — but the dev token is a second issuer with the fixed
// subject "dev". With both verifiers on, an identity-provider account named
// "dev" satisfies the entry meant for the shared local token and is granted
// every action in every team.
//
// No documented setup sets both at once — the quickstart's local section runs
// the dev token alone and its authentik section runs OIDC alone. But the
// combination is deliberately supported: TestDevTokenAndOIDCCoexistInDevelopment
// blesses it as "a development machine exercising the real path and still able
// to curl", which is precisely where somebody carries SUPER_ADMINS=dev over
// from the local section.
func TestSuperAdminsRefusesTheDevSubjectWhenOIDCIsOn(t *testing.T) {
	t.Parallel()

	// load validates, so a rejected configuration never becomes a Config.
	cfg, err := load(oidcEnv(map[string]string{"OPENARITY_SUPER_ADMINS": "dev"}))
	if err == nil {
		t.Fatalf("an identity-provider account named \"dev\" can become a super admin: %+v", cfg)
	}
	if !strings.Contains(err.Error(), "SUPER_ADMINS") {
		t.Errorf("the error does not name the variable to change: %v", err)
	}
	// The reason has to travel with it. "not allowed" sends somebody looking
	// for a typo rather than at the two namespaces overlapping.
	if !strings.Contains(err.Error(), "development token") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// It has to be rejected however it is written, or the check is advice rather
// than a guard.
func TestTheDevSubjectIsRefusedAnywhereInTheList(t *testing.T) {
	t.Parallel()

	for _, list := range []string{"dev", "akadmin,dev", "dev,akadmin", "a,dev,b"} {
		if cfg, err := load(oidcEnv(map[string]string{"OPENARITY_SUPER_ADMINS": list})); err == nil {
			t.Errorf("SUPER_ADMINS=%q was accepted with OIDC enabled: %+v", list, cfg)
		}
	}
}

// The documented local setup — a dev token and no identity provider — has no
// second namespace to collide with and must keep working untouched.
func TestTheDevSubjectIsFineWithoutOIDC(t *testing.T) {
	t.Parallel()

	if _, err := load(devEnv(map[string]string{"OPENARITY_SUPER_ADMINS": "dev"})); err != nil {
		t.Fatalf("SUPER_ADMINS=dev with no identity provider was rejected: %v", err)
	}
}

// The guard is about one reserved subject, not about running OIDC with super
// admins at all. Rejecting the normal case would push people to unset
// SUPER_ADMINS entirely, which leaves a brain nobody can administer.
func TestAnOrdinarySubjectIsAcceptedWithOIDC(t *testing.T) {
	t.Parallel()

	if _, err := load(oidcEnv(map[string]string{"OPENARITY_SUPER_ADMINS": "akadmin,shrijeeth"})); err != nil {
		t.Fatalf("ordinary super admins with OIDC were rejected: %v", err)
	}
}

// The guard above matches the literal "dev", and internal/auth decides what
// the development token's subject actually is. Nothing links the two, so a
// rename there would leave this package guarding a string no principal can
// have — the guard would pass every test it owns and protect nothing.
//
// The same shape shipped once already in this repository: a role renamed in
// SQL while the Go constant still said the old name, which produced 403s that
// explained nothing. This is the cheap version of that lesson.
func TestTheGuardWatchesTheSubjectTheDevTokenActuallyHas(t *testing.T) {
	t.Parallel()

	const token = "local-dev-token"

	v, err := auth.NewDevVerifier(token)
	if err != nil {
		t.Fatalf("NewDevVerifier: %v", err)
	}

	p, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if p.Subject != devSubject {
		t.Errorf("the dev token's subject is %q but config guards %q — "+
			"SUPER_ADMINS would accept the guarded value and grant super admin "+
			"to an identity-provider account with that name", p.Subject, devSubject)
	}
}
