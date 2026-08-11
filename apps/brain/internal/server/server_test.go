package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

// discardLogger is for tests that assert on behaviour rather than log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingLogger returns a logger and the buffer it writes to. Reads of the
// buffer must happen after whatever wrote to it has finished — every use here
// reads only once Run has returned.
func recordingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func testConfig() *config.Config {
	return &config.Config{
		APIBind:     "127.0.0.1:21120",
		WebhookBind: "0.0.0.0:21121",
	}
}

// fakePinger stands in for the store. It counts calls so a test can prove
// healthz never reaches the database.
type fakePinger struct {
	mu    sync.Mutex
	err   error
	calls int
}

// Ping honours the context, as a real one does — that is what lets a test
// assert readyz stops when the probe disconnects.
func (f *fakePinger) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++

	if err := ctx.Err(); err != nil {
		return err
	}
	return f.err
}

func (f *fakePinger) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// healthyDB is the usual case: a database that answers.
func healthyDB() *fakePinger { return &fakePinger{} }

// Swapping these two binds puts the API on a public port and the webhook
// receiver on loopback. That is the whole security boundary, so assert the
// mapping rather than trusting the field order in New.
func TestNewBindsEachListenerToItsOwnAddress(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv := New(cfg, discardLogger(), healthyDB(), http.NotFoundHandler())

	if srv.api.Addr != cfg.APIBind {
		t.Errorf("api bound to %q, want %q", srv.api.Addr, cfg.APIBind)
	}
	if srv.webhook.Addr != cfg.WebhookBind {
		t.Errorf("webhook bound to %q, want %q", srv.webhook.Addr, cfg.WebhookBind)
	}
}

// Every listener must be wrapped in the request logger. Building the
// middleware and forgetting to apply it leaves an exported function no linter
// flags — `unused` assumes an exported identifier has a caller elsewhere — so
// this is the only thing standing between wired and silently dead.
//
// Handler identity cannot be compared once middleware is applied:
// http.HandlerFunc is a func type, and == on funcs panics at runtime.
func TestNewWrapsBothListenersInTheRequestLogger(t *testing.T) {
	t.Parallel()

	for name, pick := range map[string]func(*Server) *http.Server{
		"api":     func(s *Server) *http.Server { return s.api },
		"webhook": func(s *Server) *http.Server { return s.webhook },
	} {
		logger, buf := recordingLogger()
		srv := New(testConfig(), logger, healthyDB(), http.NotFoundHandler())

		listener := pick(srv)
		if listener.Handler == nil {
			t.Fatalf("New left the %s handler nil", name)
		}

		rec := httptest.NewRecorder()
		listener.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))

		if !strings.Contains(buf.String(), `"path":"/nope"`) {
			t.Errorf("%s listener is not wrapped in the request logger: %q", name, buf.String())
		}
	}
}

// gosec G112 already caught a version of this. Assert every timeout so a
// dropped field fails the test rather than only the linter.
func TestNewSetsAllTimeouts(t *testing.T) {
	t.Parallel()

	srv := New(testConfig(), discardLogger(), healthyDB(), http.NotFoundHandler())

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
		if s.MaxHeaderBytes != maxHeaderBytes {
			t.Errorf("%s MaxHeaderBytes = %d, want %d — the 1 MB default is a log amplifier", name, s.MaxHeaderBytes, maxHeaderBytes)
		}
	}
}

// New must not dial, resolve or listen. It is called before anything is up.
func TestNewDoesNotListen(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = "256.256.256.256:99999" // unresolvable, unbindable

	if srv := New(cfg, discardLogger(), healthyDB(), http.NotFoundHandler()); srv == nil {
		t.Fatal("New returned nil")
	}
}

// handlers returns both muxes, unwrapped by middleware, keyed by listener.
func handlers(t *testing.T, db Pinger) map[string]http.Handler {
	t.Helper()

	srv := New(testConfig(), discardLogger(), db, http.NotFoundHandler())
	return map[string]http.Handler{
		"api":     srv.apiHandler(),
		"webhook": srv.webhookHandler(),
	}
}

// Nothing but the probes is registered on the API listener, and on the
// webhook listener everything else belongs to the gateway — which is a
// NotFoundHandler here. Either way a typo must not become a 200.
func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()

	for name, h := range handlers(t, healthyDB()) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s GET /nope = %d, want 404", name, rec.Code)
		}
	}
}

// The gateway owns the webhookPrefix subtree on the public listener and
// nothing else. This is the only place the server and the gateway have to
// agree on a string, so pin both directions: a real channel path reaches the
// gateway with its full URL intact, and the probes never do. Change the
// gateway's route patterns without changing this prefix and this fails
// instead of 404ing in production.
func TestWebhookHandlerRoutesTheGatewaySubtree(t *testing.T) {
	t.Parallel()

	var seen []string
	gateway := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	h := New(testConfig(), discardLogger(), healthyDB(), gateway).webhookHandler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhook/telegram/ch-1", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("POST /webhook/telegram/ch-1 = %d, want the gateway's 418", rec.Code)
	}

	// The gateway matches on the full path, so the prefix must not be
	// stripped on the way through.
	if want := []string{"POST /webhook/telegram/ch-1"}; !slices.Equal(seen, want) {
		t.Errorf("gateway saw %v, want %v", seen, want)
	}

	for _, probe := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, probe, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 from the server rather than the gateway", probe, rec.Code)
		}
	}
	if len(seen) != 1 {
		t.Errorf("a probe reached the gateway: %v", seen)
	}
}

// panickyPinger triggers a panic inside a route both listeners serve —
// /readyz is the one handler on the API listener that runs real code, which
// is what lets the recovery test below drive both chains.
type panickyPinger struct{}

func (panickyPinger) Ping(context.Context) error { panic("ping boom") }

// The wiring test the add-middleware skill demands: a panic in the gateway
// must come back as a 500 and a structured record. Left unrecovered,
// net/http writes no response at all, so the provider sees a reset and
// retries into silence. Only a request through the built Server catches
// RecoverPanic being built and never applied.
func TestNewRecoversAGatewayPanic(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	srv := New(testConfig(), logger, healthyDB(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("gateway boom")
	}))

	rec := httptest.NewRecorder()
	srv.webhook.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhook/telegram/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panicking gateway = %d, want 500", rec.Code)
	}
	if !strings.Contains(buf.String(), "gateway boom") {
		t.Errorf("panic was not logged: %s", buf.String())
	}
}

// RecoverPanic must be in BOTH listeners' chains — dropping it from one
// still passes every other test. The gateway is webhook-only, so the panic
// comes from the Pinger through /readyz, the one route on the API listener
// that runs real code.
func TestNewRecoversAPanicOnBothListeners(t *testing.T) {
	t.Parallel()

	for name, pick := range map[string]func(*Server) *http.Server{
		"api":     func(s *Server) *http.Server { return s.api },
		"webhook": func(s *Server) *http.Server { return s.webhook },
	} {
		logger, buf := recordingLogger()
		srv := New(testConfig(), logger, panickyPinger{}, http.NotFoundHandler())

		rec := httptest.NewRecorder()
		pick(srv).Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: panicking readyz = %d, want 500", name, rec.Code)
		}
		if !strings.Contains(buf.String(), "ping boom") {
			t.Errorf("%s: panic was not logged: %s", name, buf.String())
		}
	}
}
