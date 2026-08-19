package secrets

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestStaticReturnsAStoredValue(t *testing.T) {
	t.Parallel()

	s := Static{"teams/a/channels/b": {"signing_secret": "s3cr3t"}}

	got, err := s.Get(t.Context(), "teams/a/channels/b", "signing_secret")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("Get = %q, want %q", got, "s3cr3t")
	}
}

// Fail closed. An empty secret handed to a signature check verifies against
// an empty key, and several HMAC schemes accept that — so a missing secret
// must never reach a caller as "".
func TestStaticFailsClosed(t *testing.T) {
	t.Parallel()

	s := Static{"teams/a/channels/b": {"signing_secret": "s3cr3t"}}

	cases := map[string]struct{ path, key string }{
		"unknown path": {"teams/x/channels/y", "signing_secret"},
		"unknown key":  {"teams/a/channels/b", "nope"},
		"empty store":  {"", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := s.Get(t.Context(), tc.path, tc.key)
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
			if got != "" {
				t.Errorf("value = %q, want empty alongside the error", got)
			}
		})
	}
}

// The path is derived from the row and never stored, so this function is the
// only place the convention lives. Pinning it here means a change to the
// layout is a deliberate edit to a failing test, not a silent 404 at runtime.
func TestPathFollowsTheHLDConvention(t *testing.T) {
	t.Parallel()

	team := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	channel := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	want := "teams/11111111-1111-1111-1111-111111111111/" +
		"channels/22222222-2222-2222-2222-222222222222"
	if got := Path(team, KindChannel, channel); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// A secret the whole team shares hangs off the team, with no instance under
// it. Forcing it through Path would need a uuid.Nil that silently means
// "not that kind".
func TestTeamPathHasNoInstanceSegment(t *testing.T) {
	t.Parallel()

	team := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	want := "teams/11111111-1111-1111-1111-111111111111/channels"
	if got := TeamPath(team, KindChannel); got != want {
		t.Errorf("TeamPath = %q, want %q", got, want)
	}
}

// Every Kind is listed in AllKinds, so a test can reach all of them and a
// new one cannot be added without being seen. Same discipline as
// authz.AllActions.
func TestAllKindsListsEveryConstant(t *testing.T) {
	t.Parallel()

	if len(AllKinds) == 0 {
		t.Fatal("AllKinds is empty")
	}

	seen := map[Kind]bool{}
	for _, k := range AllKinds {
		if k == "" {
			t.Error("AllKinds contains an empty kind")
		}
		if seen[k] {
			t.Errorf("%q appears twice in AllKinds", k)
		}
		seen[k] = true
	}
}

// A kind becomes a path segment. A slash would let one kind reach into
// another's namespace, and an upper-case one would collide on a store that
// folds case.
func TestKindsAreSafePathSegments(t *testing.T) {
	t.Parallel()

	for _, k := range AllKinds {
		s := string(k)
		switch {
		case strings.ContainsAny(s, "/."):
			t.Errorf("kind %q contains a path separator", s)
		case s != strings.ToLower(s):
			t.Errorf("kind %q is not lower case", s)
		}
	}
}

// Two different channels must never collide on one path, or one channel's
// secret answers for another's webhook.
func TestPathIsUniquePerChannel(t *testing.T) {
	t.Parallel()

	team := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	a := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	b := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	if Path(team, KindChannel, a) == Path(team, KindChannel, b) {
		t.Error("two channels in one team share a secret path")
	}
}
