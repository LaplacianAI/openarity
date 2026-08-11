package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.wrote = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// FlushError intercepts ResponseController flushes: a flush commits the
// response, so it must count as written or a flush-then-panic request would
// log a 500 the client never saw.
func (r *recorder) FlushError() error {
	r.wrote = true
	return http.NewResponseController(r.ResponseWriter).Flush()
}

// Unwrap exposes the underlying writer to http.NewResponseController for
// everything not intercepted above, such as the per-request deadline
// setters.
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func LogRequests(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &recorder{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			// Only the probes themselves are skipped. Kubernetes sends GET;
			// any other method on those paths is answered 405 by the mux and
			// is real traffic — likely someone probing — so it stays logged.
			if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
				next.ServeHTTP(rw, r)
				return
			}

			// The record is emitted from a defer so a panicking handler
			// still produces one — those are the requests whose status and
			// latency matter most. RecoverPanic sits outside this middleware
			// and owns the recovery; the 500 logged here mirrors what it
			// writes when nothing reached the wire.
			panicked := true
			defer func() {
				status := rw.status
				if panicked && !rw.wrote {
					status = http.StatusInternalServerError
				}
				attrs := []any{
					"method", r.Method,
					"path", clipField(r.URL.Path, maxLoggedPath),
					"status", status,
					"dur_ms", time.Since(start).Milliseconds(),
				}
				if panicked {
					attrs = append(attrs, "panicked", true)
				}
				logger.Info("Request processed", attrs...)
			}()
			next.ServeHTTP(rw, r)
			panicked = false
		})
	}
}
