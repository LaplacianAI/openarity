package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed rbac.json
var rbacJSON []byte

const rbacLockID int64 = 1645966605

var (
	scopesWithAPermission    = []string{"team", "any_team"}
	scopesWithoutAPermission = []string{"authenticated", "member", "super_admin"}
)

type rbacPermission struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type rbacRole struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type rbacRoute struct {
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Permission *string `json:"permission,omitempty"`
	Scope      string  `json:"scope"`
}

type rbacFile struct {
	Comment     json.RawMessage  `json:"$comment,omitempty"`
	Permissions []rbacPermission `json:"permissions"`
	Roles       []rbacRole       `json:"roles"`
	Routes      []rbacRoute      `json:"routes"`
}

func parseRBAC(data []byte) (*rbacFile, error) {
	var f rbacFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	defined := make(map[string]bool, len(f.Permissions))
	for _, p := range f.Permissions {
		if defined[p.Name] {
			return nil, fmt.Errorf("permission %q is defined twice", p.Name)
		}
		defined[p.Name] = true
	}

	for _, r := range f.Roles {
		for _, p := range r.Permissions {
			if !defined[p] {
				return nil, fmt.Errorf("role %q grants %q, which no permission defines", r.Name, p)
			}
		}
	}

	seen := make(map[string]bool, len(f.Routes))
	for _, rt := range f.Routes {
		route := rt.Method + " " + rt.Path

		if seen[route] {
			return nil, fmt.Errorf("route %q is mapped twice", route)
		}
		seen[route] = true

		if rt.Method != strings.ToUpper(rt.Method) {
			return nil, fmt.Errorf("route %q is not upper case, so it maps a route the mux never serves", route)
		}

		consults := slices.Contains(scopesWithAPermission, rt.Scope)
		if !consults && !slices.Contains(scopesWithoutAPermission, rt.Scope) {
			return nil, fmt.Errorf("route %q has scope %q, which maps to no check", route, rt.Scope)
		}

		switch {
		case consults && rt.Permission == nil:
			return nil, fmt.Errorf("route %q is scoped %q but requires no permission, so it checks nothing", route, rt.Scope)
		case !consults && rt.Permission != nil:
			return nil, fmt.Errorf("route %q is scoped %q, which never consults the %q it names", route, rt.Scope, *rt.Permission)
		case consults && !defined[*rt.Permission]:
			return nil, fmt.Errorf("route %q requires %q, which no permission defines", route, *rt.Permission)
		}
	}

	return &f, nil
}

func (s *Store) LoadRBAC(ctx context.Context) error {
	return s.loadRBAC(ctx, rbacJSON)
}

func (s *Store) loadRBAC(ctx context.Context, data []byte) error {
	f, err := parseRBAC(data)
	if err != nil {
		return fmt.Errorf("rbac.json: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rbacLockID); err != nil {
		return fmt.Errorf("lock: %w", err)
	}

	if err := applyPermissions(ctx, tx, f.Permissions); err != nil {
		return err
	}
	if err := applyRoles(ctx, tx, f.Roles); err != nil {
		return err
	}
	if err := applyRoutes(ctx, tx, f.Routes); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func applyPermissions(ctx context.Context, tx pgx.Tx, permissions []rbacPermission) error {
	for _, p := range permissions {
		_, err := tx.Exec(ctx, `
			INSERT INTO permissions (name, description) VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description`,
			p.Name, p.Description)
		if err != nil {
			return fmt.Errorf("permission %q: %w", p.Name, err)
		}
	}
	return nil
}

func applyRoles(ctx context.Context, tx pgx.Tx, roles []rbacRole) error {
	for _, r := range roles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO roles (name, description) VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description`,
			r.Name, r.Description); err != nil {
			return fmt.Errorf("role %q: %w", r.Name, err)
		}

		if _, err := tx.Exec(ctx, `
			DELETE FROM role_permissions WHERE role = $1 AND action <> ALL($2::text[])`,
			r.Name, r.Permissions); err != nil {
			return fmt.Errorf("role %q: %w", r.Name, err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO role_permissions (role, action)
			SELECT $1, unnest($2::text[])
			ON CONFLICT DO NOTHING`,
			r.Name, r.Permissions); err != nil {
			return fmt.Errorf("role %q: %w", r.Name, err)
		}
	}
	return nil
}

func applyRoutes(ctx context.Context, tx pgx.Tx, routes []rbacRoute) error {
	if _, err := tx.Exec(ctx, `DELETE FROM route_permissions`); err != nil {
		return fmt.Errorf("clear routes: %w", err)
	}

	for _, rt := range routes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO route_permissions (method, path, permission, scope)
			VALUES ($1, $2, $3, $4)`,
			rt.Method, rt.Path, rt.Permission, rt.Scope); err != nil {
			return fmt.Errorf("route %s %s: %w", rt.Method, rt.Path, err)
		}
	}
	return nil
}
