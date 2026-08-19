package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
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

// testToken is the only credential testVerifier accepts.
const testToken = "test-token"

// staticVerifier stands in for the chain so these tests can drive an
// authenticated route without an identity provider. Real token verification is
// covered in internal/auth; here it only has to tell accepted from rejected.
type staticVerifier struct {
	token string
}

func (s staticVerifier) Verify(_ context.Context, token string) (*auth.Principal, error) {
	if token != s.token {
		return nil, auth.ErrUnauthenticated
	}
	return &auth.Principal{Kind: auth.KindDev, Subject: "test"}, nil
}

func testVerifier() auth.Verifier { return staticVerifier{token: testToken} }

// staticResolver stands in for the store. Resolution is covered in
// internal/store and internal/middleware; here it only has to produce a user.
type staticResolver struct{}

func (staticResolver) Resolve(_ context.Context, p *auth.Principal) (*auth.User, error) {
	return &auth.User{ID: uuid.New(), Issuer: p.Issuer, Subject: p.Subject}, nil
}

// deps is the usual set: one database check, a verifier that accepts
// testToken, and a resolver that always succeeds.
func deps(db Pinger) Deps {
	return depsWith(Check{Name: "postgres", Pinger: db})
}

// depsWith is deps for the tests that care about more than one dependency.
// Keeping deps(db) as it was means no existing test has to change shape.
func depsWith(checks ...Check) Deps {
	return Deps{Checks: checks, Verifier: testVerifier(), Resolver: staticResolver{}}
}

// Swapping these two binds puts the API on a public port and the webhook
// receiver on loopback. That is the whole security boundary, so assert the
// mapping rather than trusting the field order in New.
func TestNewBindsEachListenerToItsOwnAddress(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	srv := New(cfg, discardLogger(), deps(healthyDB()))

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
		srv := New(testConfig(), logger, deps(healthyDB()))

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

	srv := New(testConfig(), discardLogger(), deps(healthyDB()))

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

	if srv := New(cfg, discardLogger(), deps(healthyDB())); srv == nil {
		t.Fatal("New returned nil")
	}
}

// handlers returns both muxes, without the request logger, keyed by listener.
// The API mux carries its own authentication — that is part of the routing
// under test, not middleware wrapped around it.
func handlers(t *testing.T, db Pinger) map[string]http.Handler {
	t.Helper()
	return handlersWith(t, Check{Name: "postgres", Pinger: db})
}

func handlersWith(t *testing.T, checks ...Check) map[string]http.Handler {
	t.Helper()

	srv := New(testConfig(), discardLogger(), depsWith(checks...))
	return map[string]http.Handler{
		"api":     srv.apiHandler(),
		"webhook": srv.webhookHandler(),
	}
}

// request drives one handler. An empty token sends no Authorization header.
func request(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The webhook listener authenticates nothing — providers sign the body instead
// — so an unknown path there is simply not a route.
func TestUnknownWebhookPathIs404(t *testing.T) {
	t.Parallel()

	rec := request(t, handlers(t, healthyDB())["webhook"], http.MethodGet, "/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("webhook GET /nope = %d, want 404", rec.Code)
	}
}

// On the API listener the catch-all pattern sends anything unlisted through
// authentication first, so an unauthenticated caller cannot learn which routes
// exist by watching 401 turn into 404.
func TestUnknownAPIPathIsUnauthorizedBeforeItIsNotFound(t *testing.T) {
	t.Parallel()

	api := handlers(t, healthyDB())["api"]

	if rec := request(t, api, http.MethodGet, "/nope", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /nope without a token = %d, want 401", rec.Code)
	}
	if rec := request(t, api, http.MethodGet, "/nope", testToken); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope with a token = %d, want 404", rec.Code)
	}
}
