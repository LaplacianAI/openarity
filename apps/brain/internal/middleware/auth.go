package middleware

import (
	"net/http"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="openarity"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func Authenticate(v auth.Verifier) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				unauthorized(w)
				return
			}

			p, err := v.Verify(r.Context(), token)
			if err != nil || p == nil {
				unauthorized(w)
				return
			}

			ctx := auth.WithPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
