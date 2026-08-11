package middleware

import (
	"net/http"
	"unicode/utf8"
)

type Middleware func(http.Handler) http.Handler

// Chain applies middlewares outermost first: Chain(h, a, b) is a(b(h)).
// Order is behaviour, not style — anything that must observe a request it
// might reject goes outside the thing that rejects it.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// maxLoggedPath caps the request path in log records. The path is
// attacker-controlled up to the server's MaxHeaderBytes, which is far larger
// than any legitimate route here — logging it uncapped hands an
// unauthenticated caller a log-volume amplifier.
const maxLoggedPath = 256

// clipField caps an unbounded value for logging, cutting on a rune boundary
// so a multi-byte character is dropped whole rather than split into invalid
// UTF-8. The gateway carries its own copy under the same name — depguard
// keeps these packages apart, and a shared name keeps the copies greppable.
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
