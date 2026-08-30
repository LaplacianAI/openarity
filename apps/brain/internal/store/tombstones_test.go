package store

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func at(v time.Time) *time.Time { return &v }

func tombstones(t *testing.T, s *Store) []db.DeletedObject {
	t.Helper()

	rows, err := s.ClaimDeletedObjects(t.Context(), db.ClaimDeletedObjectsParams{
		RetryBefore: at(time.Now()),
		BatchSize:   100,
	})
	if err != nil {
		t.Fatalf("ClaimDeletedObjects: %v", err)
	}
	return rows
}

// The reason this is a trigger and not a query. Deleting a team removes its
// attachments through channels, sessions and messages without any Go code
// seeing a row, so an outbox written by our own delete statements would catch
// nothing — and every deletion that exists today is one of these.
func TestEveryCascadeThatReachesAnAttachmentLeavesATombstone(t *testing.T) {
	s := queryStore(t)

	type target struct {
		sql string
		arg func(messageID, sessionID, teamID uuid.UUID) uuid.UUID
	}

	for name, tc := range map[string]target{
		"the team": {
			`DELETE FROM teams WHERE id = $1`,
			func(_, _, teamID uuid.UUID) uuid.UUID { return teamID },
		},
		"the channel": {
			`DELETE FROM channels WHERE id =
			(SELECT channel_id FROM messages WHERE id = $1)`,
			func(messageID, _, _ uuid.UUID) uuid.UUID { return messageID },
		},
		"the message": {
			`DELETE FROM messages WHERE id = $1`,
			func(messageID, _, _ uuid.UUID) uuid.UUID { return messageID },
		},
		"the session": {
			`DELETE FROM sessions WHERE id = $1`,
			func(_, sessionID, _ uuid.UUID) uuid.UUID { return sessionID },
		},
		"the sender": {
			`DELETE FROM users WHERE id =
			(SELECT user_id FROM messages WHERE id = $1)`,
			func(messageID, _, _ uuid.UUID) uuid.UUID { return messageID },
		},
	} {
		t.Run(name, func(t *testing.T) {
			messageID, sessionID, teamID := seedMessage(t, s, "cascade-"+uuid.NewString()[:8])
			key := "teams/" + teamID.String() + "/objects/" + uuid.NewString()
			mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID, key))

			if _, err := s.pool.Exec(t.Context(), tc.sql,
				tc.arg(messageID, sessionID, teamID)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			var found *db.DeletedObject
			for _, row := range tombstones(t, s) {
				if row.ObjectKey == key {
					found = &row
					break
				}
			}
			if found == nil {
				t.Fatalf("deleting %s left no tombstone for %s", name, key)
			}

			// The team has to survive the cascade that removed it, because the
			// team is what scopes the key the sweeper deletes under. A join
			// cannot recover it: during a team cascade the sessions row is
			// already gone when this trigger fires.
			if found.TeamID != teamID {
				t.Errorf("tombstone team = %s, want %s", found.TeamID, teamID)
			}
			if found.Attempts != 1 {
				t.Errorf("Attempts = %d after one claim, want 1", found.Attempts)
			}

			if _, err := s.pool.Exec(t.Context(), `DELETE FROM deleted_objects`); err != nil {
				t.Fatalf("clearing: %v", err)
			}
		})
	}
}

// One statement, not one per row. Deleting a team with ten thousand
// attachments should be one insert into the outbox rather than ten thousand
// trigger invocations.
func TestABulkDeleteWritesEveryTombstone(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, teamID := seedMessage(t, s, "bulk")
	want := map[string]bool{}
	for range 25 {
		key := "teams/" + teamID.String() + "/objects/" + uuid.NewString()
		mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID, key))
		want[key] = true
	}

	if _, err := s.pool.Exec(t.Context(),
		`DELETE FROM attachments WHERE session_id = $1`, sessionID); err != nil {
		t.Fatalf("bulk delete: %v", err)
	}

	for _, row := range tombstones(t, s) {
		delete(want, row.ObjectKey)
	}
	if len(want) != 0 {
		t.Errorf("%d of 25 objects were deleted with no tombstone", len(want))
	}
}

// A claim leases what it hands out, so a second sweeper sees the next batch
// rather than the same one. Without that two replicas do the same work twice
// and neither makes progress on the backlog.
func TestAClaimLeasesWhatItReturns(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, teamID := seedMessage(t, s, "lease")
	for range 3 {
		mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID,
			"teams/"+teamID.String()+"/objects/"+uuid.NewString()))
	}
	if _, err := s.pool.Exec(t.Context(),
		`DELETE FROM attachments WHERE session_id = $1`, sessionID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	first, err := s.ClaimDeletedObjects(t.Context(), db.ClaimDeletedObjectsParams{
		RetryBefore: at(time.Now()), BatchSize: 100,
	})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first claim took %d, want 3", len(first))
	}

	// A second sweeper arriving immediately sees nothing: the lease is
	// last_attempt_at, and the retry window has not passed.
	second, err := s.ClaimDeletedObjects(t.Context(), db.ClaimDeletedObjectsParams{
		RetryBefore: at(time.Now().Add(-5 * time.Minute)), BatchSize: 100,
	})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("a second sweeper claimed %d leased rows", len(second))
	}

	// Once the window passes, the same rows are available again — which is
	// what makes a sweeper that died holding them harmless.
	third, err := s.ClaimDeletedObjects(t.Context(), db.ClaimDeletedObjectsParams{
		RetryBefore: at(time.Now().Add(time.Minute)), BatchSize: 100,
	})
	if err != nil {
		t.Fatalf("third claim: %v", err)
	}
	if len(third) != 3 {
		t.Errorf("after the window, claimed %d, want 3", len(third))
	}
	for _, row := range third {
		if row.Attempts != 2 {
			t.Errorf("Attempts = %d after two claims, want 2", row.Attempts)
		}
	}
}

// Two rows naming one object produce one tombstone, not two. The sweeper asks
// how many rows still reference it before deleting anything, so a single
// record of "somebody's claim ended" is all this has to carry.
func TestTwoRowsSharingAnObjectMakeOneTombstone(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, teamID := seedMessage(t, s, "shared")
	shared := "teams/" + teamID.String() + "/objects/shared"
	mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID, shared))
	mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID, shared))

	if _, err := s.pool.Exec(t.Context(),
		`DELETE FROM attachments WHERE object_key = $1`, shared); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if rows := tombstones(t, s); len(rows) != 1 {
		t.Errorf("%d tombstones for one object", len(rows))
	}
}

// The tombstone outlives the team, and that is the point: the commonest reason
// one is written is that the team itself is being deleted, so a foreign key
// would cascade away the record of the work still to do.
func TestATombstoneSurvivesTheTeamThatCausedIt(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, teamID := seedMessage(t, s, "outlives")
	key := "teams/" + teamID.String() + "/objects/" + uuid.NewString()
	mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID, key))

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, teamID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM teams WHERE id = $1`, teamID); n != 0 {
		t.Fatalf("the team survived")
	}

	rows := tombstones(t, s)
	if len(rows) != 1 || rows[0].ObjectKey != key {
		t.Fatalf("tombstones = %+v, want one for %s", rows, key)
	}
	if rows[0].TeamID != teamID {
		t.Errorf("team = %s, want %s — the sweeper cannot scope the delete without it",
			rows[0].TeamID, teamID)
	}
}

// The backlog is what says whether erasure is keeping up, and it has to answer
// on an empty table without failing: min() over no rows is null.
func TestTheBacklogOnAnEmptyTableIsEmpty(t *testing.T) {
	s := queryStore(t)

	rows, err := s.DeletedObjectBacklog(t.Context())
	if err != nil {
		t.Fatalf("DeletedObjectBacklog: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestTheBacklogCountsAndDatesWhatIsOutstanding(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, teamID := seedMessage(t, s, "backlog")
	for range 4 {
		mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID,
			"teams/"+teamID.String()+"/objects/"+uuid.NewString()))
	}
	before := time.Now()
	if _, err := s.pool.Exec(t.Context(),
		`DELETE FROM attachments WHERE session_id = $1`, sessionID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows, err := s.DeletedObjectBacklog(t.Context())
	if err != nil {
		t.Fatalf("DeletedObjectBacklog: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	if rows[0].Outstanding != 4 {
		t.Errorf("Outstanding = %d, want 4", rows[0].Outstanding)
	}
	if rows[0].Oldest.Before(before.Add(-time.Minute)) || rows[0].Oldest.After(time.Now()) {
		t.Errorf("Oldest = %s, outside the window this test created", rows[0].Oldest)
	}
}

// Forgetting is what the sweeper does once the object is gone, and it must
// take exactly the one row.
func TestForgettingRemovesOnlyThatTombstone(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, teamID := seedMessage(t, s, "forget")
	keys := make([]string, 0, 3)
	for range 3 {
		key := "teams/" + teamID.String() + "/objects/" + uuid.NewString()
		keys = append(keys, key)
		mustCreateAttachment(t, s, attachment(t, s, messageID, sessionID, key))
	}
	if _, err := s.pool.Exec(t.Context(),
		`DELETE FROM attachments WHERE session_id = $1`, sessionID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := s.ForgetDeletedObject(t.Context(), keys[1]); err != nil {
		t.Fatalf("ForgetDeletedObject: %v", err)
	}

	left := map[string]bool{}
	for _, row := range tombstones(t, s) {
		left[row.ObjectKey] = true
	}
	if left[keys[1]] {
		t.Error("the forgotten tombstone is still there")
	}
	if !left[keys[0]] || !left[keys[2]] {
		t.Errorf("forgetting one removed others: %v", left)
	}

	// Forgetting one that is already gone is not an error: the sweeper repeats
	// it after a crash between the object delete and this.
	if err := s.ForgetDeletedObject(t.Context(), keys[1]); err != nil {
		t.Errorf("forgetting twice: %v", err)
	}
}

// The triggers build secret paths in SQL, because a cascade has no caller to
// pass one in. Those strings and secrets.Path are two spellings of one format,
// and nothing but this test connects them: change either and every secret
// written afterwards is orphaned under a path no sweep will ever claim.
func TestTheTriggersSpellSecretPathsTheWayGoDoes(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "paths")
	ch := mustCreateChannel(t, s, team.ID, "custom", "paths")

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}

	got := map[string]uuid.UUID{}
	for _, row := range secretTombstones(t, s) {
		got[row.Path] = row.TeamID
	}

	for what, path := range map[string]string{
		"the channel's signing secret": secrets.Path(team.ID, secrets.KindChannel, ch.ID),
		"the team's attachment key":    secrets.TeamPath(team.ID, secrets.KindAttachments),
	} {
		teamID, found := got[path]
		if !found {
			t.Errorf("%s was not recorded at %q; the trigger spells it differently: %v",
				what, path, got)
			continue
		}
		if teamID != team.ID {
			t.Errorf("%s recorded team %s, want %s", what, teamID, team.ID)
		}
	}
}

func secretTombstones(t *testing.T, s *Store) []db.DeletedSecret {
	t.Helper()

	rows, err := s.ClaimDeletedSecrets(t.Context(), db.ClaimDeletedSecretsParams{
		RetryBefore: at(time.Now()), BatchSize: 100,
	})
	if err != nil {
		t.Fatalf("ClaimDeletedSecrets: %v", err)
	}
	return rows
}

// Deleting a channel on its own records its secret too. The handler used to do
// this itself, best-effort, logging when it failed — which is a leak with a log
// line in front of it.
func TestDeletingAChannelRecordsItsSecret(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "one-channel")
	ch := mustCreateChannel(t, s, team.ID, "custom", "one-channel")

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM channels WHERE id = $1`, ch.ID); err != nil {
		t.Fatalf("deleting the channel: %v", err)
	}

	rows := secretTombstones(t, s)
	want := secrets.Path(team.ID, secrets.KindChannel, ch.ID)
	if len(rows) != 1 || rows[0].Path != want {
		t.Fatalf("tombstones = %+v, want one for %s", rows, want)
	}

	// The team is still there, and its key is not being destroyed.
	if n := scalar[int](t, s, `SELECT count(*) FROM teams WHERE id = $1`, team.ID); n != 1 {
		t.Error("the team went with its channel")
	}
}

// The secret tombstone outlives the team for the same reason the object one
// does: the commonest cause of one is the team being deleted.
func TestASecretTombstoneSurvivesItsTeam(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "outlives-secret")
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, team.ID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}

	rows := secretTombstones(t, s)
	if len(rows) != 1 {
		t.Fatalf("tombstones = %+v, want the team key", rows)
	}
	if rows[0].TeamID != team.ID {
		t.Errorf("team = %s, want %s", rows[0].TeamID, team.ID)
	}
}

func TestTheSecretBacklogOnAnEmptyTableIsEmpty(t *testing.T) {
	s := queryStore(t)

	rows, err := s.DeletedSecretBacklog(t.Context())
	if err != nil {
		t.Fatalf("DeletedSecretBacklog: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestForgettingASecretRemovesOnlyThatOne(t *testing.T) {
	s := queryStore(t)

	first := mustCreate(t, s, "forget-a")
	second := mustCreate(t, s, "forget-b")
	if _, err := s.pool.Exec(t.Context(),
		`DELETE FROM teams WHERE id = ANY($1)`, []uuid.UUID{first.ID, second.ID}); err != nil {
		t.Fatalf("deleting: %v", err)
	}

	gone := secrets.TeamPath(first.ID, secrets.KindAttachments)
	if err := s.ForgetDeletedSecret(t.Context(), gone); err != nil {
		t.Fatalf("ForgetDeletedSecret: %v", err)
	}

	left := map[string]bool{}
	for _, row := range secretTombstones(t, s) {
		left[row.Path] = true
	}
	if left[gone] {
		t.Error("the forgotten tombstone is still there")
	}
	if !left[secrets.TeamPath(second.ID, secrets.KindAttachments)] {
		t.Errorf("forgetting one removed the other: %v", left)
	}
}
