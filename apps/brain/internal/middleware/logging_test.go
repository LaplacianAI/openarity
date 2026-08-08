package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func recordingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

// serve runs one request through LogRequests and returns the response
// recorder and the single log record, or nil if nothing was logged.
func serve(t *testing.T, method, target string, h http.HandlerFunc) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	logger, buf := recordingLogger()
	rec := httptest.NewRecorder()

	LogRequests(logger)(h).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, nil))

	if buf.Len() == 0 {
		return rec, nil
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log line is not JSON: %q", buf.String())
	}
	return rec, record
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// The fields are the whole point — a log nobody can query by status or path
// is a log nobody reads.
func TestLogRequestsRecordsTheRequest(t *testing.T) {
	t.Parallel()

	_, rec := serve(t, http.MethodPost, "/things", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	if rec == nil {
		t.Fatal("nothing was logged")
	}

	want := map[string]any{
		"method": "POST",
		"path":   "/things",
		"status": float64(http.StatusCreated),
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v, want %v", k, rec[k], v)
		}
	}
	if _, found := rec["dur_ms"]; !found {
		t.Error("no duration recorded")
	}
}

// A handler that only calls Write never calls WriteHeader — Go sends 200
// implicitly. Start the recorder at 0 and every successful request logs
// status 0, which reads as a bug that is not there.
func TestLogRequestsDefaultsToStatus200(t *testing.T) {
	t.Parallel()

	res, rec := serve(t, http.MethodGet, "/implicit", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("body"))
	})
	if rec == nil {
		t.Fatal("nothing was logged")
	}

	if rec["status"] != float64(http.StatusOK) {
		t.Errorf("implicit 200 logged as %v", rec["status"])
	}
	if res.Code != http.StatusOK {
		t.Errorf("response code = %d, want 200", res.Code)
	}
	if res.Body.String() != "body" {
		t.Errorf("body = %q, want %q — the recorder swallowed it", res.Body.String(), "body")
	}
}

// The recorder wraps the response writer. It must pass the status, the body
// and the headers through unchanged.
func TestLogRequestsDoesNotAlterTheResponse(t *testing.T) {
	t.Parallel()

	res, _ := serve(t, http.MethodGet, "/teapot", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Custom", "kept")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout"))
	})

	if res.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", res.Code)
	}
	if got := res.Header().Get("X-Custom"); got != "kept" {
		t.Errorf("header = %q, want kept", got)
	}
	if res.Body.String() != "short and stout" {
		t.Errorf("body = %q", res.Body.String())
	}
}

// Kubernetes probes both of these every ten seconds on two listeners. Logging
// them buries everything else. readyz reports its own failures at Warn, which
// is the part worth keeping.
func TestLogRequestsSkipsProbes(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/healthz", "/readyz"} {
		res, rec := serve(t, http.MethodGet, path, ok)
		if rec != nil {
			t.Errorf("%s was logged: %v", path, rec)
		}
		if res.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200 — the skip must not swallow the response", path, res.Code)
		}
	}
}

// The skip is by exact path. A prefix match would silence /healthz-internal or
// any future route that merely starts the same way.
func TestLogRequestsSkipIsExact(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/healthzz", "/healthz/deep", "/readyz-internal"} {
		_, rec := serve(t, http.MethodGet, path, ok)
		if rec == nil {
			t.Errorf("%s was skipped by a prefix match", path)
		}
	}
}

// Credentials travel in query strings. Logging the raw URL puts them in
// whatever ships the logs off the box.
func TestLogRequestsDoesNotLogTheQueryString(t *testing.T) {
	t.Parallel()

	_, rec := serve(t, http.MethodGet, "/things?token=hunter2&api_key=sekrit", ok)
	if rec == nil {
		t.Fatal("nothing was logged")
	}

	for _, secret := range []string{"hunter2", "sekrit", "token"} {
		for k, v := range rec {
			if s, isString := v.(string); isString && bytes.Contains([]byte(s), []byte(secret)) {
				t.Errorf("field %q leaked %q: %v", k, secret, v)
			}
		}
	}
	if rec["path"] != "/things" {
		t.Errorf("path = %v, want /things", rec["path"])
	}
}

// A panicking handler must not lose the request record. Today nothing
// recovers, so the middleware cannot log it — this pins the current behaviour
// so adding RecoverPanic later is a deliberate change, not an accident.
func TestLogRequestsDoesNotSwallowAPanic(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("panic was swallowed by the middleware")
		}
	}()

	logger, _ := recordingLogger()
	LogRequests(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", nil))
}
