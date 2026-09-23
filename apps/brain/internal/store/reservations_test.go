package store

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func claimedKeys(t *testing.T, s *Store) []string {
	t.Helper()

	rows, err := s.ClaimDeletedObjects(t.Context(), db.ClaimDeletedObjectsParams{
		RetryBefore: at(time.Now()),
		BatchSize:   100,
	})
	if err != nil {
		t.Fatalf("ClaimDeletedObjects: %v", err)
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.ObjectKey
	}
	return keys
}

func reserve(t *testing.T, s *Store, teamID uuid.UUID, after time.Time, keys ...string) {
	t.Helper()

	if err := s.ReserveObjects(t.Context(), db.ReserveObjectsParams{
		ObjectKeys: keys, TeamID: teamID, ClaimableAfter: after,
	}); err != nil {
		t.Fatalf("ReserveObjects: %v", err)
	}
}

// The race this column exists for: an upload has tombstoned its keys and not
// yet committed the rows naming them. A sweep now would count no row, delete
// the object, and leave the skill that commits a moment later pointing at
// nothing.
func TestAReservationIsNotClaimedWhileTheUploadMayStillCommit(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	key := "teams/" + team.ID.String() + "/objects/uploading"

	reserve(t, s, team.ID, time.Now().Add(10*time.Minute), key)

	if got := claimedKeys(t, s); slices.Contains(got, key) {
		t.Errorf("a reservation was claimed before it was claimable: %v", got)
	}
}

// After the hold, a reservation no row came to name is an object an upload
// abandoned, and is swept like any other tombstone.
func TestAReservationIsClaimedOnceItsHoldHasPassed(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	key := "teams/" + team.ID.String() + "/objects/abandoned"

	reserve(t, s, team.ID, time.Now().Add(-time.Second), key)

	if got := claimedKeys(t, s); !slices.Contains(got, key) {
		t.Errorf("a reservation past its hold was not claimed: %v", got)
	}
}

// Everything that already wrote tombstones — the triggers — must behave as it
// did before the column existed: claimable at once.
func TestATriggersTombstoneIsClaimableAtOnce(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	skill := insertSkill(t, s, team.ID, "review")
	key := "teams/" + team.ID.String() + "/objects/deleted"
	if err := insertSkillFile(t, s, skill, team.ID, "forms.md", key); err != nil {
		t.Fatalf("insert file: %v", err)
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM skills WHERE id = $1`, skill); err != nil {
		t.Fatalf("delete skill: %v", err)
	}

	if got := claimedKeys(t, s); !slices.Contains(got, key) {
		t.Errorf("a trigger's tombstone was held back: %v", got)
	}
}

// Held back means held back even for a row nobody has tried: the claim is
// "never tried, or tried long enough ago" AND "claimable". Without the
// parentheses the first half alone lets a fresh reservation through.
func TestANeverTriedReservationIsStillHeld(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	key := "teams/" + team.ID.String() + "/objects/fresh"

	reserve(t, s, team.ID, time.Now().Add(time.Hour), key)

	var tried bool
	if err := s.pool.QueryRow(t.Context(),
		`SELECT last_attempt_at IS NOT NULL FROM deleted_objects WHERE object_key = $1`, key).Scan(&tried); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if tried {
		t.Fatal("the fixture is wrong: this case is about a row never tried")
	}
	if got := claimedKeys(t, s); slices.Contains(got, key) {
		t.Errorf("a never-tried reservation was claimed through the hold: %v", got)
	}
}

// The reaper deletes an object only when no row names it. A skill file is such
// a row now, and a count that missed it would sweep the files of every
// committed skill — the whole point of reserving would be undone by the check.
func TestObjectReferencesCountSkillFilesAndAttachments(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	skill := insertSkill(t, s, team.ID, "review")

	msg, session, _ := seedMessage(t, s, "refs")
	mustCreateAttachment(t, s, attachment(t, s, msg, session, "teams/x/objects/attached"))
	if err := insertSkillFile(t, s, skill, team.ID, "forms.md", "teams/x/objects/skill-file"); err != nil {
		t.Fatalf("insert file: %v", err)
	}

	for key, want := range map[string]int64{
		"teams/x/objects/attached":   1,
		"teams/x/objects/skill-file": 1,
		"teams/x/objects/absent":     0,
	} {
		got, err := s.CountObjectReferences(t.Context(), key)
		if err != nil {
			t.Fatalf("CountObjectReferences(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("references to %q = %d, want %d", key, got, want)
		}
	}
}

// A key reserved twice is refused. Keys are fresh per upload, so a second
// reservation means two uploads chose one key — and both would believe they
// own the object. Failing the second is the only answer that stays true.
func TestReservingTheSameKeyTwiceFails(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	key := "teams/" + team.ID.String() + "/objects/twice"

	reserve(t, s, team.ID, time.Now().Add(time.Minute), key)

	err := s.ReserveObjects(t.Context(), db.ReserveObjectsParams{
		ObjectKeys: []string{key}, TeamID: team.ID, ClaimableAfter: time.Now().Add(time.Minute),
	})
	wantPGCode(t, err, uniqueViolation, "a key reserved twice")
}
