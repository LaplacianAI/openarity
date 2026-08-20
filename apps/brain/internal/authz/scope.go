package authz

import (
	"fmt"
	"slices"
)

type Action string

type Scope string

const (
	ScopeAuthenticated Scope = "authenticated"
	ScopeMember        Scope = "member"
	ScopeTeam          Scope = "team"
	ScopeAnyTeam       Scope = "any_team"
	ScopeSuperAdmin    Scope = "super_admin"
)

var (
	scopesWithAPermission    = []Scope{ScopeTeam, ScopeAnyTeam}
	scopesWithoutAPermission = []Scope{ScopeAuthenticated, ScopeMember, ScopeSuperAdmin}
)

type Route struct {
	Scope      Scope
	Permission Action
}

type Routes map[string]Route

func NewRoutes() Routes { return Routes{} }

func RouteKey(method, path string) string { return method + " " + path }

func (rs Routes) Add(method, path, scope string, permission *string) error {
	key := RouteKey(method, path)
	if _, mapped := rs[key]; mapped {
		return fmt.Errorf("route %q is mapped twice", key)
	}

	s := Scope(scope)
	consults := slices.Contains(scopesWithAPermission, s)
	if !consults && !slices.Contains(scopesWithoutAPermission, s) {
		return fmt.Errorf("route %q has scope %q, which maps to no check", key, scope)
	}

	switch {
	case consults && permission == nil:
		return fmt.Errorf("route %q is scoped %q but names no permission, so it would deny everyone", key, scope)
	case !consults && permission != nil:
		return fmt.Errorf("route %q is scoped %q, which never consults the %q it names", key, scope, *permission)
	}

	var action Action
	if permission != nil {
		action = Action(*permission)
	}

	rs[key] = Route{Scope: s, Permission: action}
	return nil
}

func (rs Routes) Lookup(method, path string) (Route, bool) {
	r, mapped := rs[RouteKey(method, path)]
	return r, mapped
}

func (rs Routes) Keys() []string {
	keys := make([]string, 0, len(rs))
	for k := range rs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
