package middleware

import "net/http"

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
