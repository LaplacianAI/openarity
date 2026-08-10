package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func servePanic(t *testing.T, h http.HandlerFunc) (*httptest.ResponseRecorder, string) {
	t.Helper()

	logger, buf := recordingLogger()
	rec := httptest.NewRecorder()
	RecoverPanic(logger)(h).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/boom", nil))
	return rec, buf.String()
}

// The job: a panic becomes a 500 plus a queryable record with the panic
// value and the stack. Without the response, the provider sees a reset and
// retries forever; without the record, the stack goes to stderr unseen.
func TestRecoverPanicAnswers500AndLogs(t *testing.T) {
	t.Parallel()

	rec, out := servePanic(t, func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	for _, want := range []string{"boom", "stack", `"path":"/boom"`} {
		if !strings.Contains(out, want) {
			t.Errorf("panic record is missing %q: %s", want, out)
		}
	}
}

// A healthy request passes through untouched — status, headers and body.
func TestRecoverPanicDoesNotAlterTheResponse(t *testing.T) {
	t.Parallel()

	rec, out := servePanic(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Custom", "kept")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout"))
	})

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
	if got := rec.Header().Get("X-Custom"); got != "kept" {
		t.Errorf("header = %q, want kept", got)
	}
	if rec.Body.String() != "short and stout" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if out != "" {
		t.Errorf("a healthy request was logged as a panic: %s", out)
	}
}

// A handler that only calls Write sends 200 implicitly; the middleware must
// not turn that into anything else.
func TestRecoverPanicHandlesTheImplicit200(t *testing.T) {
	t.Parallel()

	rec, _ := servePanic(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("body"))
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "body" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "body")
	}
}

// http.ErrAbortHandler is net/http's sentinel for aborting a response on
// purpose. Recovering it would break that contract.
func TestRecoverPanicRepanicsErrAbortHandler(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != http.ErrAbortHandler { //nolint:errorlint // the sentinel is panicked as-is
			t.Error("ErrAbortHandler was swallowed")
		}
	}()

	logger, _ := recordingLogger()
	RecoverPanic(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/abort", nil))
}
