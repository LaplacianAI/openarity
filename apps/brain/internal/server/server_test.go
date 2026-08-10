package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Swapping these two binds puts the API on a public port and the webhook
// receiver on loopback. That is the whole security boundary, so assert the
// mapping rather than trusting the field order in New.
func TestNewBindsEachListenerToItsOwnAddress(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv := New(cfg, discardLogger(), http.NotFoundHandler())

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
		srv := New(testConfig(), logger, http.NotFoundHandler())

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

	srv := New(testConfig(), discardLogger(), http.NotFoundHandler())

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

	if srv := New(cfg, discardLogger(), http.NotFoundHandler()); srv == nil {
		t.Fatal("New returned nil")
	}
}

// Kubernetes probes this endpoint on the webhook listener. It must answer 200
// on both handlers with no dependencies wired.
func TestHealthzOnBothHandlers(t *testing.T) {
	t.Parallel()

	for name, h := range map[string]http.Handler{
		"api":     apiHandler(),
		"webhook": webhookHandler(http.NotFoundHandler()),
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

// The gateway owns everything on the public listener except the GET probe.
// Mux specificity does the routing: GET /healthz stays here, and every other
// request — POST /healthz included — falls through to the gateway. Pinned so
// nobody "fixes" POST /healthz into an unrouted 405.
func TestWebhookHandlerRoutesEverythingElseToTheGateway(t *testing.T) {
	t.Parallel()

	var gotPaths []string
	gateway := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	h := webhookHandler(gateway)

	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/webhook/telegram/ch-1"},
		{http.MethodPost, "/healthz"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), req.method, req.path, nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s %s = %d, want the gateway's 418", req.method, req.path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200 from the server, not the gateway", rec.Code)
	}

	want := []string{"POST /webhook/telegram/ch-1", "POST /healthz"}
	if len(gotPaths) != len(want) || gotPaths[0] != want[0] || gotPaths[1] != want[1] {
		t.Errorf("gateway saw %v, want %v", gotPaths, want)
	}
}

// The wiring test the add-middleware skill demands: a panic inside the
// gateway must come back as a 500 and a structured record, not a dropped
// connection. Only a request through the built Server catches RecoverPanic
// being built but never applied.
func TestNewRecoversAGatewayPanic(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	srv := New(testConfig(), logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
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

// Nothing else is registered yet. A catch-all "/" would make every typo a 200
// and hide routing mistakes.
func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()

	for name, h := range map[string]http.Handler{
		"api":     apiHandler(),
		"webhook": webhookHandler(http.NotFoundHandler()),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s GET /nope = %d, want 404", name, rec.Code)
		}
	}
}
