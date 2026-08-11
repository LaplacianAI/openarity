// Package secrets is the one seam to a secret backend. Everything else in
// the codebase holds paths into it, never values, and only this package may
// import a backend SDK — see HLD §Secrets.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound reports a path with no secret behind it. Callers fail closed:
// a missing secret is a rejection, never an empty string passed onward.
var ErrNotFound = errors.New("secret not found")

// ErrBadPathSegment reports a tenant or channel id that cannot form a secret
// path — empty, or containing characters that would escape its namespace.
var ErrBadPathSegment = errors.New("invalid secret path segment")

// Store hands out secret values by path. Implementations fail closed: a
// missing path is ErrNotFound, never an empty string.
type Store interface {
	Get(ctx context.Context, path string) (string, error)
}

// ChannelPath is where a channel's credentials live. Paths are per-tenant so
// one tenant's secrets are isolated and revocable as a unit (HLD §Secrets
// rule 3). Both ids are validated even though today's callers pass trusted
// registrations: the moment they come from a database row, a segment like
// "../other-tenant" must not read across the namespace boundary.
func ChannelPath(tenantID, channelID string) (string, error) {
	for _, segment := range []string{tenantID, channelID} {
		if err := checkPathSegment(segment); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("tenants/%s/channels/%s", tenantID, channelID), nil
}

// checkPathSegment rejects anything that could change the shape of the path
// a segment is spliced into: separators, "..", and control characters.
func checkPathSegment(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("empty segment: %w", ErrBadPathSegment)
	case strings.ContainsAny(s, `/\`):
		return fmt.Errorf("separator in segment: %w", ErrBadPathSegment)
	case strings.Contains(s, ".."):
		return fmt.Errorf("traversal in segment: %w", ErrBadPathSegment)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("control character in segment: %w", ErrBadPathSegment)
		}
	}
	return nil
}

// Static is an in-memory Store for wiring and tests. It fails closed like
// any real backend: a missing path is ErrNotFound.
type Static map[string]string

func (s Static) Get(_ context.Context, path string) (string, error) {
	v, ok := s[path]
	if !ok {
		return "", fmt.Errorf("%s: %w", path, ErrNotFound)
	}
	return v, nil
}
