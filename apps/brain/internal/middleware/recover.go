package middleware

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
)

const (
	// maxLoggedPanic caps the stringified panic value — it is arbitrary
	// program state and can embed anything, including unbounded input.
	maxLoggedPanic = 512

	// maxLoggedStack bounds the captured trace. A panic reachable from the
	// public listener must not become an unbounded log record per request.
	maxLoggedStack = 16 << 10
)

// RecoverPanic turns a handler panic into a structured log record, and into
// a 500 when the response has not started — once a handler has written a
// status or body, the wire is already committed and only the log records
// the failure. Without this middleware, net/http recovers per connection
// but writes no response — a webhook provider sees a reset and retries
// forever — and the stack trace goes to stderr, bypassing slog entirely.
// Outermost in the chain, so it catches panics from every other middleware
// too.
func RecoverPanic(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &startedWriter{ResponseWriter: w}
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
				buf := make([]byte, maxLoggedStack)
				buf = buf[:runtime.Stack(buf, false)]
				logger.Error("panic recovered",
					"method", r.Method,
					"path", clipField(r.URL.Path, maxLoggedPath),
					"panic", clipField(fmt.Sprint(v), maxLoggedPanic),
					"stack", string(buf),
				)
				if !sw.started {
					sw.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

// startedWriter records whether the response has been committed, so the
// recovery path knows a 500 can still reach the client. Flush and Hijack
// are intercepted for the same reason WriteHeader is: both commit the
// response without going through Write, and an Unwrap-only wrapper would
// let ResponseController slip past the tracking.
type startedWriter struct {
	http.ResponseWriter
	started bool
}

func (w *startedWriter) WriteHeader(code int) {
	w.started = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *startedWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}

// A failed flush committed nothing, so only a successful one flips the
// tracking. Hijack is the opposite on purpose: after an attempt — even a
// failed one — the connection's state is unknown, and writing a 500 into it
// would be a guess.
func (w *startedWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err == nil {
		w.started = true
	}
	return err
}

func (w *startedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.started = true
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

// Unwrap exposes the underlying writer to http.NewResponseController for
// everything not intercepted above.
func (w *startedWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
