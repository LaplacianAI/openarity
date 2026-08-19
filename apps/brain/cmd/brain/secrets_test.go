package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

// capturingLogger keeps what was logged so a test can assert on the one
// warning that tells a developer their secrets go nowhere.
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

// baoAt returns a config pointing at addr with AppRole credentials set.
func baoAt(addr string) *config.Config {
	return &config.Config{
		SecretsAddr:          addr,
		SecretsAppRoleID:     "role",
		SecretsAppRoleSecret: "secret-id",
		SecretsKVMount:       "secret",
	}
}

// Without AppRole credentials there is no OpenBao session to have, so a
// development brain gets the in-memory store rather than a client that fails
// on first use.
func TestNoAppRoleCredentialsGivesTheStaticStore(t *testing.T) {
	t.Parallel()

	store := newSecretStore(&config.Config{SecretsAddr: "http://localhost:8200"}, discardLogger())
	if _, ok := store.(secrets.Static); !ok {
		t.Errorf("store is %T, want secrets.Static", store)
	}
}

// Config validation rejects half a credential outside development, but in
// development it is accepted and must not produce a store that cannot log in.
func TestHalfAnAppRoleCredentialGivesTheStaticStore(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]*config.Config{
		"id only": {SecretsAddr: "http://localhost:8200", SecretsAppRoleID: "role"},
		"secret only": {
			SecretsAddr: "http://localhost:8200", SecretsAppRoleSecret: "secret-id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if store := newSecretStore(cfg, discardLogger()); !isStatic(store) {
				t.Errorf("store is %T, want secrets.Static", store)
			}
		})
	}
}

func isStatic(s secrets.Store) bool {
	_, ok := s.(secrets.Static)
	return ok
}

// The in-memory store holds nothing, so a channel registered against it
// verifies nothing. That is fine on a laptop and catastrophic anywhere else,
// and this warning is the only thing that says so.
func TestTheStaticStoreSaysItHoldsNothing(t *testing.T) {
	t.Parallel()

	logger, buf := capturingLogger()
	newSecretStore(&config.Config{SecretsAddr: "http://localhost:8200"}, logger)

	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("falling back to the in-memory store was not logged at WARN: %s", out)
	}
}

// A real brain must get the OpenBao store. The concrete type is unexported,
// following internal/auth, so the assertion is about what it can do.
func TestAppRoleCredentialsGiveAnOpenBaoStore(t *testing.T) {
	t.Parallel()

	store := newSecretStore(baoAt("http://localhost:8200"), discardLogger())

	if _, ok := store.(secrets.Static); ok {
		t.Fatal("credentials were set and the store is still the in-memory one")
	}
	if _, ok := store.(secrets.Prober); !ok {
		t.Errorf("store is %T, want one that implements secrets.Prober", store)
	}
	if _, ok := store.(secrets.Writer); !ok {
		t.Errorf("store is %T, want one that implements secrets.Writer", store)
	}
}

// The HLD is explicit: startup fails loudly on an unreachable secret store
// rather than degrading. A brain that comes up against a sealed OpenBao looks
// healthy and 500s the first webhook of every channel, which is the hardest
// version of this outage to diagnose.
func TestStartupFailsWhenTheConfiguredOpenBaoCannotAnswer(t *testing.T) {
	t.Parallel()

	for name, status := range map[string]int{
		"sealed":          http.StatusServiceUnavailable,
		"not initialised": http.StatusNotImplemented,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"errors":["Vault is sealed"]}`))
				}))
			t.Cleanup(srv.Close)

			store := newSecretStore(baoAt(srv.URL), discardLogger())
			if err := checkSecretStore(t.Context(), store); !errors.Is(err, secrets.ErrUnavailable) {
				t.Errorf("err = %v, want ErrUnavailable", err)
			}
		})
	}
}

// An address with nothing behind it is the common case: a typo, or a compose
// stack where the brain started first.
func TestStartupFailsWhenOpenBaoIsUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	store := newSecretStore(baoAt(addr), discardLogger())
	if err := checkSecretStore(t.Context(), store); !errors.Is(err, secrets.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// Every other case here asserts a failure. Without this one, a check that
// always returned an error would pass the whole file.
func TestStartupAcceptsAHealthyOpenBao(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"initialized":true,"sealed":false}`))
		}))
	t.Cleanup(srv.Close)

	store := newSecretStore(baoAt(srv.URL), discardLogger())
	if err := checkSecretStore(t.Context(), store); err != nil {
		t.Errorf("checkSecretStore against a healthy OpenBao: %v", err)
	}
}

// A development brain has no OpenBao at all, and must still start. Static
// does not implement Prober, so there is nothing to probe.
func TestStartupAcceptsTheStaticStore(t *testing.T) {
	t.Parallel()

	store := newSecretStore(&config.Config{SecretsAddr: "http://localhost:8200"}, discardLogger())
	if err := checkSecretStore(t.Context(), store); err != nil {
		t.Errorf("checkSecretStore on the static store: %v", err)
	}
}

// The startup error has to say what failed. "connection refused" alone sends
// an operator to the database.
func TestStartupErrorNamesTheSecretStore(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	err := checkSecretStore(t.Context(), newSecretStore(baoAt(addr), discardLogger()))
	if err == nil {
		t.Fatal("an unreachable secret store started cleanly")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "secret store") {
		t.Errorf("err = %q, want it to name the secret store", err)
	}
}
