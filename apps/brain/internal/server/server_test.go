package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		APIBind:     "127.0.0.1:21120",
		WebhookBind: "0.0.0.0:21121",
	}
}

// Swapping these two binds puts the API on a public port and the webhook
// receiver on loopback. That is the whole security boundary, so assert the
// mapping rather than trusting the field order in New.
func TestNewBindsEachListenerToItsOwnAddress(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv := New(cfg)

	if srv.api.Addr != cfg.APIBind {
		t.Errorf("api bound to %q, want %q", srv.api.Addr, cfg.APIBind)
	}
	if srv.webhook.Addr != cfg.WebhookBind {
		t.Errorf("webhook bound to %q, want %q", srv.webhook.Addr, cfg.WebhookBind)
	}
}

// The two listeners must not share a mux. Once routes diverge, a shared
// handler exposes every API route on the public webhook port.
func TestNewGivesEachListenerItsOwnHandler(t *testing.T) {
	t.Parallel()

	srv := New(testConfig())

	if srv.api.Handler == nil || srv.webhook.Handler == nil {
		t.Fatal("New left a handler nil")
	}
	if srv.api.Handler == srv.webhook.Handler {
		t.Error("both listeners share one handler")
	}
}

// gosec G112 already caught a version of this. Assert every timeout so a
// dropped field fails the test rather than only the linter.
func TestNewSetsAllTimeouts(t *testing.T) {
	t.Parallel()

	srv := New(testConfig())

	for name, s := range map[string]*http.Server{"api": srv.api, "webhook": srv.webhook} {
		if s.ReadHeaderTimeout != readHeaderTimeout {
			t.Errorf("%s ReadHeaderTimeout = %v, want %v", name, s.ReadHeaderTimeout, readHeaderTimeout)
		}
		if s.ReadTimeout != readTimeout {
			t.Errorf("%s ReadTimeout = %v, want %v", name, s.ReadTimeout, readTimeout)
		}
		if s.WriteTimeout != writeTimeout {
			t.Errorf("%s WriteTimeout = %v, want %v", name, s.WriteTimeout, writeTimeout)
		}
		if s.IdleTimeout != idleTimeout {
			t.Errorf("%s IdleTimeout = %v, want %v", name, s.IdleTimeout, idleTimeout)
		}
	}
}

// New must not dial, resolve or listen. It is called before anything is up.
func TestNewDoesNotListen(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = "256.256.256.256:99999" // unresolvable, unbindable

	if srv := New(cfg); srv == nil {
		t.Fatal("New returned nil")
	}
}

// Kubernetes probes this endpoint on the webhook listener. It must answer 200
// on both handlers with no dependencies wired.
func TestHealthzOnBothHandlers(t *testing.T) {
	t.Parallel()

	for name, h := range map[string]http.Handler{
		"api":     apiHandler(),
		"webhook": webhookHandler(),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s /healthz = %d, want 200", name, rec.Code)
		}
		if rec.Body.String() != "ok\n" {
			t.Errorf("%s /healthz body = %q, want %q", name, rec.Body.String(), "ok\n")
		}
	}
}

// The route is registered "GET /healthz". A bare "/healthz" pattern would
// answer every method, turning the probe into an unauthenticated write target
// the day it grows a body.
func TestHealthzRejectsNonGET(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	apiHandler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/healthz", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", rec.Code)
	}
}

// Nothing else is registered yet. A catch-all "/" would make every typo a 200
// and hide routing mistakes.
func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()

	for name, h := range map[string]http.Handler{
		"api":     apiHandler(),
		"webhook": webhookHandler(),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s GET /nope = %d, want 404", name, rec.Code)
		}
	}
}
