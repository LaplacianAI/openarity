package config

import (
	"strings"
	"testing"
)

// The defaults must be valid. If they are not, nobody can start the process
// without overriding something first.
func TestValidateAcceptsDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := load(noEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must be valid, got: %v", err)
	}
}

// FalkorDB and Redis are separate servers; one URL means one port for both.
func TestFalkorAndRedisMustDiffer(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{
		"OPENARITY_FALKOR_DB_URL": "redis://127.0.0.1:6379",
		"OPENARITY_REDIS_URL":     "redis://127.0.0.1:6379",
	})
	if err != nil {
		return // rejected at load, fine
	}
	if err := cfg.Validate(); err == nil {
		t.Error("identical FalkorDBURL and RedisURL accepted")
	}
}

// Every URL field must reject a value with the wrong scheme.
func TestValidateRejectsWrongSchemePerField(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"OPENARITY_POSTGRES_DSN":   "redis://127.0.0.1:6379",
		"OPENARITY_REDIS_URL":      "http://127.0.0.1:6379",
		"OPENARITY_FALKOR_DB_URL":  "postgres://127.0.0.1:6380",
		"OPENARITY_SECRETS_ADDR":   "redis://127.0.0.1:8200",
		"OPENARITY_OMNI_ROUTE_URL": "redis://127.0.0.1:20128",
	}
	for key, bad := range tests {
		if cfg, err := load(map[string]string{key: bad}); err == nil {
			t.Errorf("%s=%q accepted, got %+v", key, bad, cfg)
		}
	}
}

func TestValidateRejectsBadBinds(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"OPENARITY_API_BIND", "OPENARITY_WEBHOOK_BIND"} {
		for _, bad := range []string{"localhost", "127.0.0.1:abc", "127.0.0.1:99999"} {
			if _, err := load(map[string]string{key: bad}); err == nil {
				t.Errorf("%s=%q accepted", key, bad)
			}
		}
	}
}

func TestCheckHostPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		wantErr bool
	}{
		{"127.0.0.1:21120", false},
		{"0.0.0.0:8080", false},
		{"[::1]:8080", false},
		{"localhost", true},       // no port
		{"127.0.0.1:", true},      // empty port
		{"127.0.0.1:abc", true},   // not a number
		{"127.0.0.1:99999", true}, // out of range
		{"127.0.0.1:0", true},     // 0 means "any free port"
		{"", true},
	}
	for _, tt := range tests {
		err := checkHostPort("FIELD", tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("checkHostPort(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
		}
	}
}

func TestCheckURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		schemes []string
		wantErr bool
	}{
		{"http://127.0.0.1:20128/v1", httpSchemes, false},
		{"https://example.com", httpSchemes, false},
		{"redis://127.0.0.1:6380", redisSchemes, false},
		{"rediss://127.0.0.1:6380", redisSchemes, false},
		{"postgres://u:p@localhost:5432/db", postgresSchemes, false},
		{"postgresql://u:p@localhost:5432/db", postgresSchemes, false},
		{"nonsense", httpSchemes, true}, // no scheme, no host
		{"http://", httpSchemes, true},  // no host
		{"", httpSchemes, true},         // empty
		// url.Parse itself errors on these two — the rare case it does.
		{"://foo", httpSchemes, true},          // missing protocol scheme
		{"127.0.0.1:6380", redisSchemes, true}, // bare host:port, not a URL
		{"http://ex ample.com", httpSchemes, true},
		{"redis://127.0.0.1:6380", httpSchemes, true}, // wrong scheme
		{"postgres://localhost:5432/x", redisSchemes, true},
		{"HTTP://example.com", httpSchemes, false}, // url.Parse lowercases the scheme
	}
	for _, tt := range tests {
		err := checkURL("FIELD", tt.in, tt.schemes...)
		if (err != nil) != tt.wantErr {
			t.Errorf("checkURL(%q, %v) err=%v, wantErr=%v", tt.in, tt.schemes, err, tt.wantErr)
		}
	}
}

// Error messages must name the field, so an operator knows what to fix.
func TestErrorsNameTheField(t *testing.T) {
	t.Parallel()

	err := checkHostPort("API_BIND", "broken")
	if err == nil || !strings.Contains(err.Error(), "API_BIND") {
		t.Errorf("error should name API_BIND, got: %v", err)
	}

	err = checkURL("SECRETS_ADDR", "nonsense", httpSchemes...)
	if err == nil || !strings.Contains(err.Error(), "SECRETS_ADDR") {
		t.Errorf("error should name SECRETS_ADDR, got: %v", err)
	}
}
