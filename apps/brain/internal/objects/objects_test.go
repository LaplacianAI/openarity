package objects

import (
	"testing"

	"github.com/google/uuid"
)

// The stored key is the one thing a tampered row could change. Every read
// asserts the prefix, so a row pointing at another team is refused before
// any byte is fetched.
func TestInTeamRejectsAnotherTeamsPrefix(t *testing.T) {
	t.Parallel()

	mine := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	theirs := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	for name, tc := range map[string]struct {
		key  string
		want bool
	}{
		"own prefix":       {TeamPrefix(mine) + "objects/abc", true},
		"other team":       {TeamPrefix(theirs) + "objects/abc", false},
		"no prefix":        {"objects/abc", false},
		"traversal":        {TeamPrefix(mine) + "../" + TeamPrefix(theirs) + "abc", false},
		"prefix as suffix": {"evil/" + TeamPrefix(mine) + "abc", false},
		"empty":            {"", false},

		// The prefix ends in a slash on purpose. Without it, one team's id
		// being another's leading substring would let the second read the
		// first — uuids make that unreachable today, and the slash is what
		// keeps it unreachable if keys ever stop being uuids.
		"the prefix alone, naming no object": {TeamPrefix(mine), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := InTeam(tc.key, mine); got != tc.want {
				t.Errorf("InTeam(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
