package middleware

import (
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"
)

// maxLoggedPath caps the request path in log records. The path is
// attacker-controlled up to the server's MaxHeaderBytes, which is far larger
// than any legitimate route here — logging it uncapped hands an
// unauthenticated caller a log-volume amplifier.
const maxLoggedPath = 256

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the underlying writer to http.NewResponseController, which
// net/http uses to reach Flush and the per-request deadline setters through
// wrappers like this one.
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

			next.ServeHTTP(rw, r)
			logger.Info("Request processed",
				"method", r.Method,
				"path", clipField(r.URL.Path, maxLoggedPath),
				"status", rw.status,
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// clipField caps an unbounded value for logging, cutting on a rune boundary
// so a multi-byte character is dropped whole rather than split into invalid
// UTF-8.
func clipField(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
