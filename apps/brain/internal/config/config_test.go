package config

import (
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// noEnv is an empty environment: every field falls back to its envDefault.
// Shared by the other test files in this package.
func noEnv() map[string]string { return map[string]string{} }

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := load(noEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	want := map[string]string{
		"Environment":          string(EnvironmentDevelopment),
		"APIBind":              "127.0.0.1:21120",
		"WebhookBind":          "127.0.0.1:21121",
		"LogLevel":             slog.LevelInfo.String(),
		"PostgresDSN":          "postgres://postgres:postgres@localhost:5432/openarity?sslmode=disable",
		"FalkorDBURL":          "redis://127.0.0.1:6380",
		"RedisURL":             "redis://127.0.0.1:6379",
		"SecretsBackend":       string(SecretsBackendStatic),
		"SecretsAddr":          "http://localhost:8200",
		"SecretsAppRoleID":     "",
		"SecretsAppRoleSecret": "",
		"SecretsKVMount":       "secret",
		"ObjectsBackend":       string(ObjectsBackendMemory),
		"ObjectsPath":          "/var/lib/openarity/objects",
		"ObjectsEndpoint":      "",
		"ObjectsRegion":        "us-east-1",
		"ObjectsBucket":        "openarity",
		"ObjectsAccessKey":     "",
		"ObjectsSecretKey":     "",
		"OmniRouteURL":         "http://localhost:20128/v1",
		"OIDCEnabled":          "false",
		"OIDCIssuer":           "",
		"OIDCAudience":         "openarity",
		"DevToken":             "",
		"SuperAdmins":          "0 entries",
	}
	got := map[string]string{
		"Environment":          string(cfg.Environment),
		"APIBind":              cfg.APIBind,
		"WebhookBind":          cfg.WebhookBind,
		"LogLevel":             cfg.LogLevel.String(),
		"PostgresDSN":          cfg.PostgresDSN,
		"FalkorDBURL":          cfg.FalkorDBURL,
		"RedisURL":             cfg.RedisURL,
		"SecretsBackend":       string(cfg.SecretsBackend),
		"SecretsAddr":          cfg.SecretsAddr,
		"SecretsAppRoleID":     cfg.SecretsAppRoleID,
		"SecretsAppRoleSecret": cfg.SecretsAppRoleSecret,
		"SecretsKVMount":       cfg.SecretsKVMount,
		"ObjectsBackend":       string(cfg.ObjectsBackend),
		"ObjectsPath":          cfg.ObjectsPath,
		"ObjectsEndpoint":      cfg.ObjectsEndpoint,
		"ObjectsRegion":        cfg.ObjectsRegion,
		"ObjectsBucket":        cfg.ObjectsBucket,
		"ObjectsAccessKey":     cfg.ObjectsAccessKey,
		"ObjectsSecretKey":     cfg.ObjectsSecretKey,
		"OmniRouteURL":         cfg.OmniRouteURL,
		"OIDCEnabled":          strconv.FormatBool(cfg.OIDCEnabled),
		"OIDCIssuer":           cfg.OIDCIssuer,
		"OIDCAudience":         cfg.OIDCAudience,
		"DevToken":             cfg.DevToken,
		"SuperAdmins":          fmt.Sprintf("%d entries", len(cfg.SuperAdmins)),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}

	// The table above is written by hand, so a new field is not wrong — it is
	// absent, and this test stays green while the setting goes unchecked. That
	// is the half-wired failure the add-env-var skill warns about, so make it
	// impossible: every field on Config must appear here.
	for i := range reflect.TypeFor[Config]().NumField() {
		name := reflect.TypeFor[Config]().Field(i).Name
		if _, ok := want[name]; !ok {
			t.Errorf("Config.%s has no entry in TestLoadDefaults — add its default", name)
		}
	}
}

func TestLoadOverridesFromEnv(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_API_BIND":  "0.0.0.0:9000",
		"OPENARITY_LOG_LEVEL": "debug",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.APIBind != "0.0.0.0:9000" {
		t.Errorf("APIBind = %q, want the override", cfg.APIBind)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	if cfg.WebhookBind != "127.0.0.1:21121" {
		t.Errorf("WebhookBind = %q, want the untouched default", cfg.WebhookBind)
	}
}

// An empty-but-set variable must not become "". http.Server turns an empty
// Addr into port 80 on every interface.
func TestLoadEmptyValueFallsBackToDefault(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{"OPENARITY_API_BIND": ""})
	if err != nil {
		return // rejecting it outright is also acceptable
	}
	if cfg.APIBind == "" {
		t.Fatal("empty API_BIND produced an empty bind address")
	}
}

// load must not hand back a config that failed validation.
func TestLoadRunsValidate(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{"OPENARITY_API_BIND": "no-port-here"})
	if err == nil {
		t.Fatalf("load accepted an invalid API_BIND, returned %+v", cfg)
	}
	if cfg != nil {
		t.Errorf("load returned non-nil config alongside an error: %+v", cfg)
	}
}

// The prefix is not optional — an unprefixed key must be ignored.
func TestLoadIgnoresUnprefixedKeys(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{"API_BIND": "0.0.0.0:9999"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.APIBind != "127.0.0.1:21120" {
		t.Errorf("APIBind = %q, an unprefixed key should be ignored", cfg.APIBind)
	}
}

// Load is the exported entry point: load(nil), reading the real process
// environment. Asserted loosely so a developer with OPENARITY_* exported in
// their shell does not see a spurious failure.
func TestLoadReadsProcessEnv(t *testing.T) {
	t.Parallel()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned a nil config and no error")
	}
	if cfg.APIBind == "" || cfg.WebhookBind == "" {
		t.Errorf("Load left a bind address empty: %+v", cfg)
	}
}

// Every URL field can carry a password. None may reach a log line.
func TestStringRedactsPasswords(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_POSTGRES_DSN":   "postgres://user:pgsecret@localhost:5432/db?sslmode=disable",
		"OPENARITY_FALKOR_DB_URL":  "redis://user:falkorsecret@127.0.0.1:6380",
		"OPENARITY_REDIS_URL":      "redis://user:redissecret@127.0.0.1:6379",
		"OPENARITY_SECRETS_ADDR":   "http://user:baosecret@localhost:8200",
		"OPENARITY_OMNI_ROUTE_URL": "http://user:omnisecret@localhost:20128/v1",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s := cfg.String()
	for _, secret := range []string{
		"pgsecret", "falkorsecret", "redissecret", "baosecret", "omnisecret",
	} {
		if strings.Contains(s, secret) {
			t.Errorf("String() leaked %q: %s", secret, s)
		}
	}
}

// The AppRole secret is not a URL, so redactURL cannot help. What keeps it
// out of a log line is that String() lists fields by hand — this pins that,
// because adding the field to the format string would be a one-word change
// nobody would question in review.
func TestStringNeverPrintsTheAppRoleSecret(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_SECRETS_APPROLE_ID":     "role-abc",
		"OPENARITY_SECRETS_APPROLE_SECRET": "approlesecret",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if s := cfg.String(); strings.Contains(s, "approlesecret") {
		t.Errorf("String() printed the AppRole secret: %s", s)
	}
}

// Redaction must not eat the parts an operator needs to debug.
func TestStringKeepsNonSecrets(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_POSTGRES_DSN": "postgres://pguser:pgsecret@db.example.com:5432/openarity",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s := cfg.String()
	for _, want := range []string{
		"development",     // Environment
		"127.0.0.1:21120", // APIBind
		"db.example.com",  // host survives
		"pguser",          // username survives
		"5432",            // port survives
	} {
		if !strings.Contains(s, want) {
			t.Errorf("String() dropped %q: %s", want, s)
		}
	}
}

// %v on a *Config must use String(). On a dereferenced Config it will not —
// that path prints raw fields and leaks. Document which is safe.
func TestStringUsedByFmtOnPointer(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_POSTGRES_DSN": "postgres://u:leakme@localhost:5432/db",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if s := fmt.Sprintf("%v", cfg); strings.Contains(s, "leakme") {
		t.Errorf("%%v on *Config leaked the password: %s", s)
	}
}

// A malformed URL must not crash String(), and must not print raw either.
func TestStringHandlesInvalidURL(t *testing.T) {
	t.Parallel()

	cfg := &Config{PostgresDSN: "://bad:secret@host"}
	s := cfg.String()
	if strings.Contains(s, "secret") {
		t.Errorf("String() leaked from an unparseable URL: %s", s)
	}
}
