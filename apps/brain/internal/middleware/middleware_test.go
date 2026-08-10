package middleware

import (
	"net/http"
	"net/http/httptest"
	"slices"
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
