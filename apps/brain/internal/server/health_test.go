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
	srv := New(testConfig(), logger, deps(brokenDB()))

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
	srv := New(testConfig(), logger, deps(healthyDB()))

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
	srv := New(testConfig(), discardLogger(), deps(db))

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

// sealedBao is the failure this whole task exists to make diagnosable.
var errSealed = errors.New("OpenBao is sealed")

func sealedBao() *fakePinger { return &fakePinger{err: errSealed} }

func TestReadyzPassesWhenEveryCheckPasses(t *testing.T) {
	t.Parallel()

	for name, h := range handlersWith(t,
		Check{Name: "postgres", Pinger: healthyDB()},
		Check{Name: "openbao", Pinger: healthyDB()},
	) {
		if rec := get(t, h, "/readyz"); rec.Code != http.StatusOK {
			t.Errorf("%s /readyz = %d, want 200", name, rec.Code)
		}
	}
}

// A sealed OpenBao must fail readiness even though Postgres is fine.
// Checking only the database would leave the pod in the Service, taking
// webhooks it cannot verify.
func TestReadyzFailsWhenOnlyTheSecretStoreIsDown(t *testing.T) {
	t.Parallel()

	for name, h := range handlersWith(t,
		Check{Name: "postgres", Pinger: healthyDB()},
		Check{Name: "openbao", Pinger: sealedBao()},
	) {
		if rec := get(t, h, "/readyz"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s /readyz = %d with OpenBao sealed, want 503", name, rec.Code)
		}
	}
}

// /readyz is served on the public webhook listener. Which dependency broke
// is an operator's business, and naming it in the body tells a stranger
// which part of the stack to come back to.
func TestReadyzDoesNotNameTheDependencyInTheResponse(t *testing.T) {
	t.Parallel()

	for name, h := range handlersWith(t,
		Check{Name: "postgres", Pinger: healthyDB()},
		Check{Name: "openbao", Pinger: sealedBao()},
	) {
		rec := get(t, h, "/readyz")
		body := rec.Body.String()
		for _, leak := range []string{"openbao", "postgres", "sealed"} {
			if strings.Contains(strings.ToLower(body), leak) {
				t.Errorf("%s /readyz body named %q: %q", name, leak, body)
			}
		}
	}
}

// The name has to reach the operator somewhere, and the log is the private
// half. Without this the name is only a struct field nobody reads.
func TestReadyzNamesTheFailingDependencyInTheLog(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	srv := New(testConfig(), logger, depsWith(
		Check{Name: "postgres", Pinger: healthyDB()},
		Check{Name: "openbao", Pinger: sealedBao()},
	))

	get(t, srv.apiHandler(), "/readyz")

	out := buf.String()
	if !strings.Contains(out, "openbao") {
		t.Errorf("the log does not name the failing dependency: %s", out)
	}
	if !strings.Contains(out, errSealed.Error()) {
		t.Errorf("the log does not say why it failed: %s", out)
	}
}

// A failed check stops the probe. Pinging the rest costs a round trip each
// on a pod that is already out of the Service, every ten seconds.
func TestReadyzStopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()

	second := healthyDB()
	srv := New(testConfig(), discardLogger(), depsWith(
		Check{Name: "postgres", Pinger: brokenDB()},
		Check{Name: "openbao", Pinger: second},
	))

	get(t, srv.apiHandler(), "/readyz")

	if got := second.callCount(); got != 0 {
		t.Errorf("the second check was pinged %d times after the first failed", got)
	}
}

// Every check runs when they all pass — a loop that returned after the
// first success would report ready with a sealed OpenBao behind it.
func TestReadyzPingsEveryCheck(t *testing.T) {
	t.Parallel()

	db, bao := healthyDB(), healthyDB()
	srv := New(testConfig(), discardLogger(), depsWith(
		Check{Name: "postgres", Pinger: db},
		Check{Name: "openbao", Pinger: bao},
	))

	get(t, srv.apiHandler(), "/readyz")

	if db.callCount() != 1 || bao.callCount() != 1 {
		t.Errorf("pings: postgres=%d openbao=%d, want 1 each", db.callCount(), bao.callCount())
	}
}

// A server wired with no checks would answer ready forever. Deps.DB could
// not be forgotten without a nil panic; a slice can, silently, and the
// symptom is a pod that never leaves the Service during an outage.
func TestNewRejectsAnEmptyCheckList(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("New accepted Deps with no checks; readiness would always pass")
		}
	}()

	New(testConfig(), discardLogger(), depsWith())
}
