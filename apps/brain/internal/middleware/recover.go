package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverPanic turns a handler panic into a 500 and a structured log record.
// Without it, net/http recovers per connection but writes no response — a
// webhook provider sees a reset and retries forever — and the stack trace
// goes to stderr, bypassing slog entirely. Outermost in the chain, so it
// catches panics from every other middleware too.
func RecoverPanic(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				// net/http's sentinel for aborting a response on purpose.
				// Recovering it would break that contract, so pass it on.
				if v == http.ErrAbortHandler { //nolint:errorlint // the sentinel is panicked as-is, never wrapped
					panic(v)
				}
				logger.Error("panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(v),
					"stack", string(debug.Stack()),
				)
				w.WriteHeader(http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
