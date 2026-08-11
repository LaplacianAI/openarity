// Package secrets is the one seam to a secret backend. Everything else in
// the codebase holds paths into it, never values, and only this package may
// import a backend SDK — see HLD §Secrets.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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

// pathSegmentShape is the allowlist for one path segment. A denylist is the
// wrong tool at a namespace boundary: once a backend SDK turns the path
// into a request URL, '?' truncates it, '#' starts a fragment and '%xx' can
// re-introduce a separator after decoding — and the next such character is
// whichever one the list forgot.
var pathSegmentShape = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// checkPathSegment rejects anything that could change the shape of the path
// a segment is spliced into.
func checkPathSegment(s string) error {
	switch {
	case s == "." || s == "..":
		return fmt.Errorf("traversal segment: %w", ErrBadPathSegment)
	case !pathSegmentShape.MatchString(s):
		return fmt.Errorf("segment outside %s: %w", pathSegmentShape, ErrBadPathSegment)
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
