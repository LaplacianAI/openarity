package authz

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

type Permissions interface {
	ActionsFor(ctx context.Context, role string) ([]string, error)
}

type Resource struct {
	TeamID uuid.UUID
}

type Authorizer struct {
	permissions Permissions
	superAdmins map[string]bool
}

func New(p Permissions, superAdmins []string) *Authorizer {
	set := make(map[string]bool, len(superAdmins))
	for _, sub := range superAdmins {
		set[sub] = true
	}
	return &Authorizer{permissions: p, superAdmins: set}
}

func (a *Authorizer) Can(ctx context.Context, u *auth.User, action Action, r Resource) (bool, error) {
	if u == nil {
		return false, nil
	}

	if a.superAdmins[u.Subject] {
		return true, nil
	}

	role, ok := u.RoleIn(r.TeamID)
	if !ok {
		return false, nil
	}

	actions, err := a.permissions.ActionsFor(ctx, role)
	if err != nil {
		return false, fmt.Errorf("authz: actions for role %q: %w", role, err)
	}

	return slices.Contains(actions, string(action)), nil
}
