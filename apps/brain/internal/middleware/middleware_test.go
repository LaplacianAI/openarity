package middleware

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Chain(h, a, b) must run a outside b. Getting this backwards silently
// swaps RecoverPanic inside the thing it is meant to catch.
func TestChainAppliesOutermostFirst(t *testing.T) {
	t.Parallel()

	var order []string
	tag := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), tag("outer"), tag("inner"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if want := []string{"outer", "inner", "handler"}; !slices.Equal(order, want) {
		t.Errorf("execution order = %v, want %v", order, want)
	}
}

// Chain with nothing to apply is the handler itself, still serving.
func TestChainWithNoMiddleware(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	Chain(http.HandlerFunc(ok)).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// Both wrappers must expose Unwrap: http.NewResponseController reaches
// Flush and the per-request deadline setters (which the streaming endpoints
// will need) by unwrapping, and a wrapper without Unwrap turns every one of
// those into ErrNotSupported.
func TestChainedWrappersExposeTheUnderlyingWriter(t *testing.T) {
	t.Parallel()

	logger, _ := recordingLogger()
	var flushErr error
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
	}), RecoverPanic(logger), LogRequests(logger))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/flush", nil))

	if flushErr != nil {
		t.Errorf("Flush through both wrappers failed: %v", flushErr)
	}
}

// Unwrap must return the wrapped writer itself — ResponseController's
// probing walks this chain for everything the wrappers do not intercept.
func TestWrappersUnwrapToTheUnderlyingWriter(t *testing.T) {
	t.Parallel()

	base := httptest.NewRecorder()
	if got := (&recorder{ResponseWriter: base}).Unwrap(); got != http.ResponseWriter(base) {
		t.Errorf("recorder.Unwrap = %v, want the wrapped writer", got)
	}
	if got := (&startedWriter{ResponseWriter: base}).Unwrap(); got != http.ResponseWriter(base) {
		t.Errorf("startedWriter.Unwrap = %v, want the wrapped writer", got)
	}
}

// A failed flush committed nothing to the wire, so it must not flip either
// wrapper's tracking — otherwise a later panic loses its 500 (startedWriter)
// or its honest logged status (recorder) over a response that never started.
func TestAFailedFlushDoesNotMarkTheResponseStarted(t *testing.T) {
	t.Parallel()

	// Embedding the interface hides httptest's Flusher, so the controller
	// reports ErrNotSupported through this base.
	base := struct{ http.ResponseWriter }{httptest.NewRecorder()}

	r := &recorder{ResponseWriter: base}
	if err := r.FlushError(); err == nil {
		t.Fatal("Flush unexpectedly succeeded on a flushless writer")
	}
	if r.wrote {
		t.Error("a failed flush marked the recorder written")
	}

	sw := &startedWriter{ResponseWriter: base}
	if err := sw.FlushError(); err == nil {
		t.Fatal("Flush unexpectedly succeeded on a flushless writer")
	}
	if sw.started {
		t.Error("a failed flush marked the response started")
	}
}

// A hijack attempt through the full chain must flip both wrappers — the
// connection's state is unknown afterwards, so RecoverPanic must not write
// a 500 into it and the request log must not fabricate one.
func TestChainedHijackKeepsThePanicStatusHonest(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	rec := httptest.NewRecorder()

	Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, _, err := http.NewResponseController(w).Hijack(); err == nil {
			t.Fatal("Hijack unexpectedly succeeded on httptest's recorder")
		}
		panic("boom after hijack")
	}), RecoverPanic(logger), LogRequests(logger)).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/hijack", nil))

	if rec.Code == http.StatusInternalServerError {
		t.Error("a 500 was written after a hijack attempt")
	}
	if !strings.Contains(buf.String(), `"panicked":true`) {
		t.Errorf("the panicking request has no request record: %s", buf.String())
	}
	if strings.Contains(buf.String(), `"status":500`) {
		t.Errorf("a fabricated 500 reached the request log: %s", buf.String())
	}
}
