package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestStaticReturnsTheStoredSecret(t *testing.T) {
	t.Parallel()

	store := Static{"teams/t1/channels/c1": "tok"}

	got, err := store.Get(t.Context(), "teams/t1/channels/c1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "tok" {
		t.Errorf("Get = %q, want %q", got, "tok")
	}
}

// Fail closed: a missing path is ErrNotFound, never an empty string with a
// nil error — an empty string would sail into a signature check and pass or
// fail for the wrong reason.
func TestStaticMissingPathIsErrNotFound(t *testing.T) {
	t.Parallel()

	got, err := Static{}.Get(t.Context(), "teams/t1/channels/nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get err = %v, want ErrNotFound", err)
	}
	if got != "" {
		t.Errorf("Get on a miss returned %q, want empty", got)
	}
}

// The zero value must behave like an empty store, not panic — main wires a
// bare Static{} until Vault exists.
func TestStaticZeroValueFailsClosed(t *testing.T) {
	t.Parallel()

	var store Static
	if _, err := store.Get(t.Context(), "any"); !errors.Is(err, ErrNotFound) {
		t.Errorf("zero-value Get err = %v, want ErrNotFound", err)
	}
}

// The path layout is a contract with everything already stored under it.
// Changing the format silently orphans every existing secret.
func TestChannelPathLayout(t *testing.T) {
	t.Parallel()

	got, err := ChannelPath("t1", "c1")
	if err != nil {
		t.Fatalf("ChannelPath: %v", err)
	}
	if got != "teams/t1/channels/c1" {
		t.Errorf("ChannelPath = %q", got)
	}
}

// A segment that could change the shape of the path it is spliced into is
// refused outright. Today every caller passes trusted registrations; the day
// they come from a database row, "../other-team" must not become a path
// that reads across the team namespace.
func TestChannelPathRejectsUnusableSegments(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		teamID, channelID string
	}{
		"empty team":           {"", "c1"},
		"empty channel":        {"t1", ""},
		"slash in team":        {"t1/x", "c1"},
		"bare traversal":       {"..", "c1"},
		"backslash in channel": {"t1", `c1\x`},
		"traversal in team":    {"../t2", "c1"},
		"traversal in channel": {"t1", "c1/../c2"},
		"newline in channel":   {"t1", "c1\n"},
		"delete char in team":  {"t1\x7f", "c1"},

		// The allowlist cases: characters that survive a denylist but break
		// the path the moment a backend SDK turns it into a request URL.
		"single dot team":     {".", "c1"},
		"question mark":       {"t1", "c1?list=true"},
		"hash in team":        {"t1#frag", "c1"},
		"percent encoding":    {"t1", "c1%2Fx"},
		"space in channel":    {"t1", "c 1"},
		"invalid utf8":        {"t1", "c1\xff"},
		"over 128 characters": {"t1", strings.Repeat("c", 129)},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := ChannelPath(tc.teamID, tc.channelID)
			if !errors.Is(err, ErrBadPathSegment) {
				t.Errorf("ChannelPath err = %v, want ErrBadPathSegment", err)
			}
			if got != "" {
				t.Errorf("ChannelPath returned %q alongside the error, want empty", got)
			}
		})
	}
}
