// Package secrets is the one seam to a secret backend. Everything else in
// the codebase holds paths into it, never values, and only this package may
// import a backend SDK — see HLD §Secrets.
package secrets

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotFound reports a path with no secret behind it. Callers fail closed:
// a missing secret is a rejection, never an empty string passed onward.
var ErrNotFound = errors.New("secret not found")

type SecretStore interface {
	Get(ctx context.Context, path string) (string, error)
}

// ChannelPath is where a channel's credentials live. Paths are per-tenant so
// one tenant's secrets are isolated and revocable as a unit (HLD §Secrets
// rule 3).
func ChannelPath(tenantID, channelID string) string {
	return fmt.Sprintf("tenants/%s/channels/%s", tenantID, channelID)
}

// Static is an in-memory SecretStore for wiring and tests. It fails closed
// like any real backend: a missing path is ErrNotFound.
type Static map[string]string

func (s Static) Get(_ context.Context, path string) (string, error) {
	v, ok := s[path]
	if !ok {
		return "", fmt.Errorf("%s: %w", path, ErrNotFound)
	}
	return v, nil
}
