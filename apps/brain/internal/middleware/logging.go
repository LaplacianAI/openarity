package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
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
			// any other method on those paths falls through to the gateway
			// on the public listener, so it is real traffic — likely someone
			// probing — and stays logged.
			if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
				next.ServeHTTP(rw, r)
				return
			}

			next.ServeHTTP(rw, r)
			logger.Info("Request processed",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
