package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
)

const teamPathValue = "id"

type Authorizer interface {
	IsSuperAdmin(u *auth.User) bool
	Can(ctx context.Context, u *auth.User, action authz.Action, r authz.Resource) (bool, error)
	CanInAnyTeam(ctx context.Context, u *auth.User, action authz.Action) (bool, error)
}

type teamKey struct{}

func withTeam(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, teamKey{}, id)
}

func TeamFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(teamKey{}).(uuid.UUID)
	return id, ok
}

func Caller(r *http.Request) *auth.User {
	u, _ := auth.UserFrom(r.Context())
	return u
}

type Guard struct {
	routes  authz.Routes
	authz   Authorizer
	logger  *slog.Logger
	wrapped map[string]bool
}

func NewGuard(routes authz.Routes, a Authorizer, logger *slog.Logger) *Guard {
	return &Guard{
		routes:  routes,
		authz:   a,
		logger:  logger,
		wrapped: make(map[string]bool, len(routes)),
	}
}

func (g *Guard) Wrap(key string, next http.HandlerFunc) (http.HandlerFunc, error) {
	route, mapped := g.routes[key]
	if !mapped {
		return nil, fmt.Errorf("route %q is not in rbac.json — every protected route needs an entry", key)
	}
	g.wrapped[key] = true

	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok {
			g.logger.Error("route ran without a caller — check the middleware order",
				"path", r.URL.Path)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		g.check(w, r, u, route, next)
	}, nil
}

func (g *Guard) Unused() []string {
	var unused []string
	for key := range g.routes {
		if !g.wrapped[key] {
			unused = append(unused, key)
		}
	}
	slices.Sort(unused)
	return unused
}

func (g *Guard) check(
	w http.ResponseWriter, r *http.Request,
	u *auth.User, route authz.Route, next http.HandlerFunc,
) {
	switch route.Scope {
	case authz.ScopeAuthenticated:
		next(w, r)

	case authz.ScopeSuperAdmin:
		if !g.authz.IsSuperAdmin(u) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)

	case authz.ScopeMember:
		teamID, ok := teamFromPath(w, r)
		if !ok {
			return
		}
		if _, member := u.RoleIn(teamID); !member && !g.authz.IsSuperAdmin(u) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		next(w, r.WithContext(withTeam(r.Context(), teamID)))

	case authz.ScopeTeam:
		teamID, ok := teamFromPath(w, r)
		if !ok {
			return
		}
		allowed, err := g.authz.Can(r.Context(), u, route.Permission, authz.Resource{TeamID: teamID})
		if !g.decide(w, r, u, allowed, err) {
			return
		}
		next(w, r.WithContext(withTeam(r.Context(), teamID)))

	case authz.ScopeAnyTeam:
		allowed, err := g.authz.CanInAnyTeam(r.Context(), u, route.Permission)
		if !g.decide(w, r, u, allowed, err) {
			return
		}
		next(w, r)

	default:
		g.logger.Error("route has a scope with no check", "scope", route.Scope, "path", r.URL.Path)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func teamFromPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(teamPathValue))
	if err != nil {
		http.Error(w, "id must be a uuid", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (g *Guard) decide(
	w http.ResponseWriter, r *http.Request, u *auth.User, allowed bool, err error,
) bool {
	if err != nil {
		g.logger.Error("authorisation check failed",
			"subject", u.Subject, "path", r.URL.Path, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return false
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}
