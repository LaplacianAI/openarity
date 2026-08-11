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

// A panic after the response has started cannot become a 500 — the status
// and body are already on the wire. The middleware must log the panic and
// leave the response alone: a WriteHeader here would be net/http's
// "superfluous WriteHeader" and a lie in the log.
func TestRecoverPanicAfterAPartialWriteKeepsTheResponse(t *testing.T) {
	t.Parallel()

	rec, out := servePanic(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("partial"))
		panic("boom after write")
	})

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the already-committed 418", rec.Code)
	}
	if rec.Body.String() != "partial" {
		t.Errorf("body = %q, want the partial write kept", rec.Body.String())
	}
	if !strings.Contains(out, "boom after write") {
		t.Errorf("the panic after the write was not logged: %s", out)
	}
}

// The panic value is arbitrary program state and can embed unbounded input.
// The record must stay bounded even when the value does not.
func TestRecoverPanicCapsTheLoggedValue(t *testing.T) {
	t.Parallel()

	_, out := servePanic(t, func(http.ResponseWriter, *http.Request) {
		panic(strings.Repeat("a", 10*maxLoggedPanic))
	})

	if strings.Contains(out, strings.Repeat("a", maxLoggedPanic+1)) {
		t.Error("the logged panic value exceeds maxLoggedPanic")
	}
	if !strings.Contains(out, strings.Repeat("a", maxLoggedPanic)) {
		t.Error("the capped panic value is missing from the record")
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
