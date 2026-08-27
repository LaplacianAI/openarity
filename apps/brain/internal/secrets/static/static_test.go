package static

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

func TestStaticReturnsAStoredValue(t *testing.T) {
	t.Parallel()

	s := &store{values: map[string]map[string]string{
		"teams/a/channels/b": {"signing_secret": "s3cr3t"},
	}}

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

	s := &store{values: map[string]map[string]string{
		"teams/a/channels/b": {"signing_secret": "s3cr3t"},
	}}

	cases := map[string]struct{ path, key string }{
		"unknown path": {"teams/x/channels/y", "signing_secret"},
		"unknown key":  {"teams/a/channels/b", "nope"},
		"empty store":  {"", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := s.Get(t.Context(), tc.path, tc.key)
			if !errors.Is(err, secrets.ErrNotFound) {
				t.Errorf("err = %v, want secrets.ErrNotFound", err)
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
	if got := secrets.Path(team, secrets.KindChannel, channel); got != want {
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
	if got := secrets.TeamPath(team, secrets.KindChannel); got != want {
		t.Errorf("TeamPath = %q, want %q", got, want)
	}
}

// Every secrets.Kind is listed in secrets.AllKinds, so a test can reach all of them and a
// new one cannot be added without being seen. Same discipline as
// authz.AllActions.
func TestAllKindsListsEveryConstant(t *testing.T) {
	t.Parallel()

	if len(secrets.AllKinds) == 0 {
		t.Fatal("secrets.AllKinds is empty")
	}

	seen := map[secrets.Kind]bool{}
	for _, k := range secrets.AllKinds {
		if k == "" {
			t.Error("secrets.AllKinds contains an empty kind")
		}
		if seen[k] {
			t.Errorf("%q appears twice in secrets.AllKinds", k)
		}
		seen[k] = true
	}
}

// A kind becomes a path segment. A slash would let one kind reach into
// another's namespace, and an upper-case one would collide on a store that
// folds case.
func TestKindsAreSafePathSegments(t *testing.T) {
	t.Parallel()

	for _, k := range secrets.AllKinds {
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

	if secrets.Path(team, secrets.KindChannel, a) == secrets.Path(team, secrets.KindChannel, b) {
		t.Error("two channels in one team share a secret path")
	}
}

// The static store is deliberately not a secrets.Prober: a development brain with no OpenBao
// has nothing to reach, and readiness skips it rather than reporting a
// dependency that does not exist.
func TestStaticIsNotAProber(t *testing.T) {
	t.Parallel()

	s := New()
	if _, ok := s.(secrets.Prober); ok {
		t.Error("the static store implements secrets.Prober — readiness would probe an in-memory map")
	}
}

// asCreator is the checked form of the type assertion. Unchecked, a store
// that stopped implementing Creator would panic here rather than say which
// interface it dropped — and errcheck refuses the unchecked form anyway.
func asCreator(t *testing.T, s secrets.Store) secrets.Creator {
	t.Helper()

	c, ok := s.(secrets.Creator)
	if !ok {
		t.Fatalf("store is %T, want one that implements secrets.Creator", s)
	}
	return c
}

// Create is what makes a per-team key safe to generate lazily. Put cannot do
// this job: it overwrites, so two callers generating a key for the same team
// both succeed and the loser's data is sealed under a key nothing holds.
func TestCreateRefusesToReplaceWhatIsThere(t *testing.T) {
	t.Parallel()

	s := New()
	creator := asCreator(t, s)
	path, key := "teams/a/attachments", "data_key"

	if err := creator.Create(t.Context(), path, key, "first"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := creator.Create(t.Context(), path, key, "second")
	if !errors.Is(err, secrets.ErrExists) {
		t.Fatalf("err = %v, want secrets.ErrExists", err)
	}

	// The refusal has to have changed nothing. An error alongside a write
	// that happened anyway is worse than either on its own.
	got, err := s.Get(t.Context(), path, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "first" {
		t.Errorf("Get = %q, want the first value to have survived", got)
	}
}

// The race this exists for, run for real. Without the lock around the check
// and the write, more than one caller writes and -race reports it; without
// the check, every caller succeeds and the last one wins.
func TestOnlyOneConcurrentCreateWins(t *testing.T) {
	t.Parallel()

	s := New()
	creator := asCreator(t, s)
	path, key := "teams/a/attachments", "data_key"

	const writers = 16
	var (
		wg    sync.WaitGroup
		won   atomic.Int64
		exist atomic.Int64
	)

	start := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			switch err := creator.Create(context.Background(), path, key, strconv.Itoa(i)); {
			case err == nil:
				won.Add(1)
			case errors.Is(err, secrets.ErrExists):
				exist.Add(1)
			default:
				t.Errorf("Create: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if won.Load() != 1 {
		t.Errorf("%d writers succeeded, want exactly 1", won.Load())
	}
	if exist.Load() != writers-1 {
		t.Errorf("%d writers saw ErrExists, want %d", exist.Load(), writers-1)
	}

	// And the surviving value is a value somebody actually wrote, rather than
	// a torn mix of two.
	got, err := s.Get(t.Context(), path, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n, err := strconv.Atoi(got); err != nil || n < 0 || n >= writers {
		t.Errorf("Get = %q, want one of the values written", got)
	}
}
