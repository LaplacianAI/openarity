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
		"OPENARITY_POSTGRES_DSN":  "redis://127.0.0.1:6379",
		"OPENARITY_REDIS_URL":     "http://127.0.0.1:6379",
		"OPENARITY_FALKOR_DB_URL": "postgres://127.0.0.1:6380",
		"OPENARITY_SECRETS_ADDR":  "redis://127.0.0.1:8200",
		"OPENARITY_LLM_BASE_URL":  "redis://127.0.0.1:20128",
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

// url.Parse quotes the whole URL in its error, and the parts it names are
// often the password itself: one holding a / ends the host early, so the
// parser reports the start of the password as a bad port. An error reaches
// stderr and the log shipper, so a URL that will not parse is refused
// without the parser's words.
func TestAURLThatWillNotParseIsNotRepeated(t *testing.T) {
	t.Parallel()

	for _, field := range []string{
		"POSTGRES_DSN", "REDIS_URL", "FALKOR_DB_URL", "SECRETS_ADDR",
		"LLM_BASE_URL", "EMBEDDING_BASE_URL", "OIDC_ISSUER",
	} {
		for name, raw := range map[string]string{
			"a bad port":                 "http://user:urlsecret@host:abc/v1",
			"a slash in the password":    "http://user:url/secret@host/v1",
			"a bad escape":               "http://user:urlsecret%zz@host/v1",
			"a trailing newline":         "http://user:urlsecret@host/v1\n",
			"a control character inside": "http://user:url\x7fsecret@host/v1",
		} {
			_, err := load(map[string]string{
				"OPENARITY_" + field:             raw,
				"OPENARITY_OIDC_ENABLED":         "true",
				"OPENARITY_EMBEDDING_MODEL":      "m",
				"OPENARITY_EMBEDDING_DIMENSIONS": "3",
			})
			if err == nil || !strings.Contains(err.Error(), field+" cannot be parsed as a URL") {
				t.Errorf("%s, %s: err = %v, want the setting named as unparseable", field, name, err)
				continue
			}
			for _, part := range []string{"secret", "user", "%zz", ":url", "host"} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("%s, %s: the refusal repeats %q from the value: %v", field, name, part, err)
				}
			}
		}
	}
}

// The checks that pass the parser name what is missing and never the value.
func TestAURLRefusedForItsShapeIsNotRepeated(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"no scheme":    "user:urlsecret@gateway.example.com/v1",
		"no host":      "http://user:urlsecret@/v1",
		"wrong scheme": "redis://user:urlsecret@gateway.example.com",
	} {
		_, err := load(map[string]string{"OPENARITY_LLM_BASE_URL": raw})
		if err == nil || !strings.Contains(err.Error(), "LLM_BASE_URL") {
			t.Errorf("%s: err = %v, want a refusal naming the setting", name, err)
			continue
		}
		if strings.Contains(err.Error(), "urlsecret") {
			t.Errorf("%s: the refusal repeats the password: %v", name, err)
		}
	}
}
