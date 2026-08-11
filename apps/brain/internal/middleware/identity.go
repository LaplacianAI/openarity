package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

type Resolver interface {
	Resolve(ctx context.Context, p *auth.Principal) (*auth.User, error)
}

func ResolveUser(r Resolver, logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p, ok := auth.PrincipalFrom(req.Context())
			if !ok {
				logger.Error("ResolveUser ran without a principal — it is not behind Authenticate")
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			u, err := r.Resolve(req.Context(), p)
			if err != nil || u == nil {
				logger.Error("failed to resolve user", "subject", p.Subject, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			next.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), u)))
		})
	}
}
