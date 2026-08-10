package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var errDBDown = errors.New("connection refused")

func brokenDB() *fakePinger { return &fakePinger{err: errDBDown} }

// get drives one probe request against one listener's mux.
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

// Liveness must answer 200 with the database on fire. This is the guardrail on
// the whole liveness/readiness split: a healthz that checks Postgres restarts
// every pod at once during a blip, which fixes nothing and adds a reconnect
// storm to the outage.
func TestHealthzIgnoresTheDatabase(t *testing.T) {
	t.Parallel()

	db := brokenDB()
	for name, h := range handlers(t, db) {
		rec := get(t, h, "/healthz")

		if rec.Code != http.StatusOK {
			t.Errorf("%s /healthz = %d with the database down, want 200", name, rec.Code)
		}
		if rec.Body.String() != "ok\n" {
			t.Errorf("%s /healthz body = %q, want %q", name, rec.Body.String(), "ok\n")
		}
	}
	if db.callCount() != 0 {
		t.Errorf("healthz pinged the database %d times, want 0", db.callCount())
	}
}

// Readiness is the opposite: a database that is down must take the pod out of
// the Service. 503 is the signal Kubernetes acts on.
func TestReadyzFailsWhenTheDatabaseIsDown(t *testing.T) {
	t.Parallel()

	for name, h := range handlers(t, brokenDB()) {
		rec := get(t, h, "/readyz")

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s /readyz = %d with the database down, want 503", name, rec.Code)
		}
	}
}

func TestReadyzSucceedsWhenTheDatabaseIsUp(t *testing.T) {
	t.Parallel()

	db := healthyDB()
	for name, h := range handlers(t, db) {
		rec := get(t, h, "/readyz")

		if rec.Code != http.StatusOK {
			t.Errorf("%s /readyz = %d, want 200", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ready") {
			t.Errorf("%s /readyz body = %q", name, rec.Body.String())
		}
	}
	if db.callCount() != 2 {
		t.Errorf("readyz pinged the database %d times across two listeners, want 2", db.callCount())
	}
}

// The 503 body reaches whoever is debugging a pod that will not go ready. The
// underlying error must not: it can carry the DSN, and this response is
// unauthenticated.
func TestReadyzDoesNotLeakTheDatabaseError(t *testing.T) {
	t.Parallel()

	db := &fakePinger{err: errors.New("failed to connect to `user=brain password=hunter2 host=db`")}

	for name, h := range handlers(t, db) {
		rec := get(t, h, "/readyz")

		if strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("%s /readyz leaked credentials to an unauthenticated caller: %q", name, rec.Body.String())
		}
	}
}

// The failure has to be visible somewhere, and the middleware skips these
// paths. readyz logs it itself, at Warn.
func TestReadyzLogsItsFailure(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	srv := New(testConfig(), logger, brokenDB(), testVerifier())

	get(t, srv.apiHandler(), "/readyz")

	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("readiness failure was not logged at WARN: %s", out)
	}
	if !strings.Contains(out, errDBDown.Error()) {
		t.Errorf("log does not say why it is not ready: %s", out)
	}
}

// A successful readiness check must be silent. Kubernetes probes every ten
// seconds on two listeners; logging the happy path buries everything else.
func TestReadyzIsSilentWhenReady(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	srv := New(testConfig(), logger, healthyDB(), testVerifier())

	get(t, srv.apiHandler(), "/readyz")
	get(t, srv.apiHandler(), "/healthz")

	if buf.Len() != 0 {
		t.Errorf("probes logged on the happy path: %s", buf.String())
	}
}

// readyz derives its context from the request. When the probe gives up and
// disconnects, the Ping must stop too rather than holding a connection.
func TestReadyzStopsWhenTheProbeDisconnects(t *testing.T) {
	t.Parallel()

	db := healthyDB()
	srv := New(testConfig(), discardLogger(), db, testVerifier())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec := httptest.NewRecorder()
	srv.apiHandler().ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil))

	if db.callCount() != 1 {
		t.Fatalf("Ping called %d times, want 1", db.callCount())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d after the probe disconnected, want 503 — the request context is not reaching Ping", rec.Code)
	}
}

// The bound must be readyz's own, not the caller's. A probe with no timeout
// must not let a Ping hang past readyTimeout.
func TestReadyTimeoutIsBounded(t *testing.T) {
	t.Parallel()

	if readyTimeout <= 0 {
		t.Fatal("readyTimeout is not set, so a hung Ping blocks the probe forever")
	}
	if readyTimeout >= 10*time.Second {
		t.Errorf("readyTimeout %v is longer than a typical probe interval, so checks pile up", readyTimeout)
	}
}

// Both probes are registered "GET /...". A bare pattern answers every method,
// turning an unauthenticated probe into a write target the day it grows a body.
//
// The two listeners refuse differently and both are correct. The webhook mux
// has no catch-all, so a matching path with the wrong method is a 405. The API
// mux sends anything unlisted through authentication first, so the same
// request never reaches the probe at all. What matters on both is that the
// handler does not run.
func TestProbesRejectNonGET(t *testing.T) {
	t.Parallel()

	refused := map[string]int{
		"api":     http.StatusUnauthorized,
		"webhook": http.StatusMethodNotAllowed,
	}

	for name, h := range handlers(t, healthyDB()) {
		for _, path := range []string{"/healthz", "/readyz"} {
			rec := request(t, h, http.MethodPost, path, "")

			if rec.Code != refused[name] {
				t.Errorf("%s POST %s = %d, want %d", name, path, rec.Code, refused[name])
			}
			if body := rec.Body.String(); strings.Contains(body, "ok") || strings.Contains(body, "ready") {
				t.Errorf("%s POST %s ran the probe handler: %q", name, path, body)
			}
		}
	}
}

// Even authenticated, POST is not the probe. It falls through to the mux of
// real routes and finds nothing there.
func TestProbesRejectNonGETEvenWithAToken(t *testing.T) {
	t.Parallel()

	api := handlers(t, healthyDB())["api"]

	for _, path := range []string{"/healthz", "/readyz"} {
		rec := request(t, api, http.MethodPost, path, testToken)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s with a token = %d, want 404: %s", path, rec.Code, rec.Body)
		}
	}
}
