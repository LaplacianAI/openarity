package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const testCap = 3

func record(t *testing.T, s *Store, channelID uuid.UUID, ref, name string) int64 {
	t.Helper()

	n, err := s.RecordPendingSender(t.Context(), db.RecordPendingSenderParams{
		ChannelID: channelID, SenderRef: ref, SenderName: name, Cap: testCap,
	})
	if err != nil {
		t.Fatalf("RecordPendingSender(%q): %v", ref, err)
	}
	return n
}

func pending(t *testing.T, s *Store, channelID uuid.UUID) []db.PendingSender {
	t.Helper()

	rows, err := s.ListPendingSenders(t.Context(), db.ListPendingSendersParams{
		ChannelID: channelID, PageSize: 1000,
	})
	if err != nil {
		t.Fatalf("ListPendingSenders: %v", err)
	}
	return rows
}

// --- the link ---

func TestALinkedSenderIsFoundByChannelAndRef(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	if err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
		ChannelID: ch.ID, SenderRef: "agent-17", UserID: userID,
	}); err != nil {
		t.Fatalf("LinkChannelSender: %v", err)
	}

	got, err := s.FindChannelSender(t.Context(), db.FindChannelSenderParams{
		ChannelID: ch.ID, SenderRef: "agent-17",
	})
	if err != nil {
		t.Fatalf("FindChannelSender: %v", err)
	}
	if got != userID {
		t.Errorf("found %s, want %s", got, userID)
	}
}

// The primary key is (channel_id, sender_ref) and not sender_ref, which is
// what stops an approval in a channel you trust from authorising the same
// string in a channel somebody else controls.
func TestTheSameRefInTwoChannelsIsTwoSenders(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	mine := mustCreateChannel(t, s, team.ID, "custom", "ours")
	theirs := mustCreateChannel(t, s, team.ID, "custom", "partner")

	asha := insertUser(t, s, "dev", "asha")
	ravi := insertUser(t, s, "dev", "ravi")

	for ch, user := range map[uuid.UUID]uuid.UUID{mine.ID: asha, theirs.ID: ravi} {
		if err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
			ChannelID: ch, SenderRef: "agent-17", UserID: user,
		}); err != nil {
			t.Fatalf("LinkChannelSender: %v", err)
		}
	}

	got, err := s.FindChannelSender(t.Context(), db.FindChannelSenderParams{
		ChannelID: mine.ID, SenderRef: "agent-17",
	})
	if err != nil {
		t.Fatalf("FindChannelSender: %v", err)
	}
	if got != asha {
		t.Errorf("agent-17 in our channel resolved to %s, want asha", got)
	}
}

// Re-approving a ref points it at somebody else rather than failing. An
// account handed to a new person is the ordinary reason.
func TestLinkingARefTwiceMovesIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	asha := insertUser(t, s, "dev", "asha")
	ravi := insertUser(t, s, "dev", "ravi")

	for _, user := range []uuid.UUID{asha, ravi} {
		if err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
			ChannelID: ch.ID, SenderRef: "agent-17", UserID: user,
		}); err != nil {
			t.Fatalf("LinkChannelSender: %v", err)
		}
	}

	got, err := s.FindChannelSender(t.Context(), db.FindChannelSenderParams{
		ChannelID: ch.ID, SenderRef: "agent-17",
	})
	if err != nil {
		t.Fatalf("FindChannelSender: %v", err)
	}
	if got != ravi {
		t.Errorf("the ref resolved to %s, want the second link", got)
	}
}

// Disconnecting a channel must not leave approvals behind: the channel id is
// gone, so nothing could ever revoke them, and reconnecting under a new id
// would not inherit them either.
func TestDeletingAChannelDeletesItsSenders(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	if err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
		ChannelID: ch.ID, SenderRef: "agent-17", UserID: userID,
	}); err != nil {
		t.Fatalf("LinkChannelSender: %v", err)
	}
	record(t, s, ch.ID, "stranger", "Nobody")

	if err := s.DeleteChannel(t.Context(), ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM channel_senders`); n != 0 {
		t.Errorf("%d approvals survived their channel", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM pending_senders`); n != 0 {
		t.Errorf("%d pending senders survived their channel", n)
	}
}

// Somebody removed from the deployment must stop being able to speak through
// every channel at once, without an admin having to remember which.
func TestDeletingAUserDeletesTheirApprovals(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	if err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
		ChannelID: ch.ID, SenderRef: "agent-17", UserID: userID,
	}); err != nil {
		t.Fatalf("LinkChannelSender: %v", err)
	}

	if _, err := s.pool.Exec(t.Context(), "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM channel_senders`); n != 0 {
		t.Errorf("%d approvals survived the person they belonged to", n)
	}
}

func TestASenderRefCannotBeEmpty(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
		ChannelID: ch.ID, SenderRef: "", UserID: userID,
	})
	wantPGCode(t, err, checkViolation, "an approval for an empty ref")
}

// --- the pending queue ---

func TestANewSenderIsRecorded(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	if n := record(t, s, ch.ID, "u-1", "Asha"); n != 1 {
		t.Errorf("recorded %d rows, want 1", n)
	}

	rows := pending(t, s, ch.ID)
	if len(rows) != 1 {
		t.Fatalf("%d pending senders, want 1", len(rows))
	}
	if rows[0].SenderRef != "u-1" || rows[0].SenderName != "Asha" || rows[0].SeenCount != 1 {
		t.Errorf("row = %+v", rows[0])
	}
}

// Every message from somebody still waiting bumps the count, so an admin can
// tell one curious stranger from something hammering the endpoint.
func TestSeeingAPendingSenderAgainCountsIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	for range 3 {
		if n := record(t, s, ch.ID, "u-1", "Asha"); n != 1 {
			t.Fatalf("a repeat sighting wrote %d rows, want 1", n)
		}
	}

	rows := pending(t, s, ch.ID)
	if len(rows) != 1 {
		t.Fatalf("%d rows, want the same sender once", len(rows))
	}
	if rows[0].SeenCount != 3 {
		t.Errorf("seen_count = %d, want 3", rows[0].SeenCount)
	}
	if !rows[0].LastSeen.After(rows[0].FirstSeen) && !rows[0].LastSeen.Equal(rows[0].FirstSeen) {
		t.Errorf("last_seen %v is before first_seen %v", rows[0].LastSeen, rows[0].FirstSeen)
	}
}

// Somebody who renames themselves has to show under the name they are using
// now, or an admin approves a row matching nothing they can see in the
// provider's own UI.
func TestARenameIsReflectedInTheQueue(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	record(t, s, ch.ID, "u-1", "Asha")
	record(t, s, ch.ID, "u-1", "Asha Menon")

	rows := pending(t, s, ch.ID)
	if rows[0].SenderName != "Asha Menon" {
		t.Errorf("sender_name = %q, want the newer name", rows[0].SenderName)
	}
}

// The table is written by an unauthenticated request from the open internet.
// Without a bound, anyone holding the signing secret can grow it forever.
func TestANewSenderPastTheCapIsDropped(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	for i := range testCap {
		if n := record(t, s, ch.ID, "u-"+string(rune('a'+i)), "Someone"); n != 1 {
			t.Fatalf("sender %d was dropped below the cap", i)
		}
	}

	if n := record(t, s, ch.ID, "one-too-many", "Stranger"); n != 0 {
		t.Errorf("a sender past the cap wrote %d rows, want 0", n)
	}
	if rows := pending(t, s, ch.ID); len(rows) != testCap {
		t.Errorf("%d rows, want the cap of %d", len(rows), testCap)
	}
}

// The cap bounds *new* strangers, not sightings of the ones already queued.
// Freezing last_seen at the cap would blind an admin on exactly the channel
// they most need to look at.
func TestAFullQueueStillRefreshesTheSendersInIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	for i := range testCap {
		record(t, s, ch.ID, "u-"+string(rune('a'+i)), "Someone")
	}
	record(t, s, ch.ID, "one-too-many", "Stranger") // dropped

	if n := record(t, s, ch.ID, "u-a", "Someone"); n != 1 {
		t.Fatalf("an existing sender was dropped at the cap: %d rows", n)
	}

	for _, row := range pending(t, s, ch.ID) {
		if row.SenderRef == "u-a" && row.SeenCount != 2 {
			t.Errorf("u-a seen_count = %d, want 2", row.SeenCount)
		}
	}
}

// The cap is per channel, so a busy one cannot starve the others.
func TestTheCapIsPerChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	busy := mustCreateChannel(t, s, team.ID, "custom", "busy")
	quiet := mustCreateChannel(t, s, team.ID, "custom", "quiet")

	for i := range testCap {
		record(t, s, busy.ID, "u-"+string(rune('a'+i)), "Someone")
	}
	if n := record(t, s, busy.ID, "extra", "Stranger"); n != 0 {
		t.Fatal("the busy channel accepted a sender past its cap")
	}

	if n := record(t, s, quiet.ID, "u-a", "Someone"); n != 1 {
		t.Errorf("the quiet channel was blocked by the busy one: %d rows", n)
	}
}

// The Go side clips to 64 runes. This is the backstop for anything reaching
// the table another way, so one row cannot flood an admin's terminal.
func TestAnOversizedNameIsRefusedByTheDatabase(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	_, err := s.RecordPendingSender(t.Context(), db.RecordPendingSenderParams{
		ChannelID: ch.ID, SenderRef: "u-1", SenderName: strings.Repeat("a", 65), Cap: testCap,
	})
	wantPGCode(t, err, checkViolation, "a sender name over 64 characters")
}

// The Go constant and the column have to agree, and neither can prove it
// alone: a test in internal/gateway that builds its input from SenderRefMax
// passes whatever the constant says. This is the pair — exactly at the limit
// is accepted, one past it is refused, and the value comes from the same
// constant the gateway refuses with.
func TestTheRefBoundMatchesWhatTheGatewayEnforces(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	atLimit := strings.Repeat("u", gateway.SenderRefMax)
	if _, err := s.RecordPendingSender(t.Context(), db.RecordPendingSenderParams{
		ChannelID: ch.ID, SenderRef: atLimit, SenderName: "Asha", Cap: testCap,
	}); err != nil {
		t.Errorf("a ref of exactly SenderRefMax was refused by the column: %v", err)
	}

	_, err := s.RecordPendingSender(t.Context(), db.RecordPendingSenderParams{
		ChannelID: ch.ID, SenderRef: strings.Repeat("u", gateway.SenderRefMax+1),
		SenderName: "Asha", Cap: testCap,
	})
	wantPGCode(t, err, checkViolation, "a ref one character over SenderRefMax")
}

// char_length counts characters and len counts bytes. A ref of multi-byte
// characters at the limit must fit, or the gateway accepts what the column
// then rejects — and the rejection is a 500 on a webhook that retries.
func TestTheRefBoundCountsCharactersInBothPlaces(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	if _, err := s.RecordPendingSender(t.Context(), db.RecordPendingSenderParams{
		ChannelID: ch.ID, SenderRef: strings.Repeat("é", gateway.SenderRefMax),
		SenderName: "Asha", Cap: testCap,
	}); err != nil {
		t.Errorf("a ref of %d multi-byte characters was refused: %v", gateway.SenderRefMax, err)
	}
}

// The approved side is bounded too. It is written by an admin rather than by
// a webhook, but the value came from one.
func TestAnOversizedRefIsRefusedOnTheApprovedSide(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	err := s.LinkChannelSender(t.Context(), db.LinkChannelSenderParams{
		ChannelID: ch.ID, SenderRef: strings.Repeat("u", gateway.SenderRefMax+1), UserID: userID,
	})
	wantPGCode(t, err, checkViolation, "an approval for a ref over SenderRefMax")
}

func TestApprovingASenderTakesThemOutOfTheQueue(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	record(t, s, ch.ID, "u-1", "Asha")

	if err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
		ChannelID: ch.ID, SenderRef: "u-1", UserID: userID,
	}); err != nil {
		t.Fatalf("ApproveSender: %v", err)
	}

	if rows := pending(t, s, ch.ID); len(rows) != 0 {
		t.Errorf("%d senders still queued after approval", len(rows))
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM channel_senders WHERE channel_id = $1`, ch.ID); n != 1 {
		t.Errorf("%d approvals, want 1", n)
	}
}

// The link and the dequeue are one statement, so they cannot come apart.
// Nothing retries an approval — an admin clicks once — and a link left with
// its pending row in place shows the next admin work that is already done.
//
// The insert half is made to fail here, which is the only way to see that the
// delete half did not happen anyway. A data-modifying CTE runs to completion
// whether or not the primary query reads it, so "it will not have run" is not
// something to assume.
func TestAFailedApprovalLeavesTheQueueAlone(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	record(t, s, ch.ID, "u-1", "Asha")

	// A user id nothing references: the foreign key refuses the insert.
	err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
		ChannelID: ch.ID, SenderRef: "u-1", UserID: uuid.New(),
	})
	wantPGCode(t, err, foreignKeyViolation, "an approval naming a user that does not exist")

	if rows := pending(t, s, ch.ID); len(rows) != 1 {
		t.Errorf("%d senders queued, want the one the failed approval must not have removed", len(rows))
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM channel_senders WHERE channel_id = $1`, ch.ID); n != 0 {
		t.Errorf("%d approvals after a refused insert", n)
	}
}

// Approving somebody already approved moves them to the new user rather than
// failing, so correcting a mistaken approval is one command.
func TestApprovingTwiceMovesTheSender(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	first := insertUser(t, s, "dev", "asha")
	second := insertUser(t, s, "dev", "bala")

	for _, userID := range []uuid.UUID{first, second} {
		if err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
			ChannelID: ch.ID, SenderRef: "u-1", UserID: userID,
		}); err != nil {
			t.Fatalf("ApproveSender: %v", err)
		}
	}

	got := scalar[uuid.UUID](t, s,
		`SELECT user_id FROM channel_senders WHERE channel_id = $1 AND sender_ref = 'u-1'`, ch.ID)
	if got != second {
		t.Errorf("sender points at %s, want the second approval %s", got, second)
	}
}

// One command for two situations: unapproving somebody, and clearing a
// pending row that is spam. The caller cannot always tell which they have.
func TestRemovingClearsBothSides(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	t.Run("an approved sender", func(t *testing.T) {
		if err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
			ChannelID: ch.ID, SenderRef: "approved", UserID: userID,
		}); err != nil {
			t.Fatalf("ApproveSender: %v", err)
		}
		if err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
			ChannelID: ch.ID, SenderRef: "approved",
		}); err != nil {
			t.Fatalf("RemoveSender: %v", err)
		}
		if n := scalar[int](t, s,
			`SELECT count(*) FROM channel_senders WHERE channel_id = $1 AND sender_ref = 'approved'`,
			ch.ID); n != 0 {
			t.Errorf("%d links survived removal", n)
		}
	})

	t.Run("a pending row", func(t *testing.T) {
		record(t, s, ch.ID, "spam", "Free Money")
		if err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
			ChannelID: ch.ID, SenderRef: "spam",
		}); err != nil {
			t.Fatalf("RemoveSender: %v", err)
		}
		if n := scalar[int](t, s,
			`SELECT count(*) FROM pending_senders WHERE channel_id = $1 AND sender_ref = 'spam'`,
			ch.ID); n != 0 {
			t.Errorf("%d pending rows survived removal", n)
		}
	})
}

// Removing somebody who was never there is success. The caller asked for a
// state and that state holds.
func TestRemovingSomebodyWhoIsNotThereIsNotAnError(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")

	if err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
		ChannelID: ch.ID, SenderRef: "never-seen",
	}); err != nil {
		t.Errorf("RemoveSender for an unknown ref: %v", err)
	}
}

// Removal is not a block: the next message queues them again. That is what
// makes a mistaken removal recoverable, and it is also why removal alone does
// not stop somebody persistent.
func TestARemovedSenderCanQueueAgain(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	userID := insertUser(t, s, "dev", "asha")

	if err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
		ChannelID: ch.ID, SenderRef: "u-1", UserID: userID,
	}); err != nil {
		t.Fatalf("ApproveSender: %v", err)
	}
	if err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
		ChannelID: ch.ID, SenderRef: "u-1",
	}); err != nil {
		t.Fatalf("RemoveSender: %v", err)
	}

	record(t, s, ch.ID, "u-1", "Asha")
	if rows := pending(t, s, ch.ID); len(rows) != 1 {
		t.Errorf("%d senders queued after a removed sender spoke again, want 1", len(rows))
	}
}

// A pending sender for a channel that does not exist would be unreachable —
// nothing could approve or clear it.
func TestAPendingSenderNeedsAChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)

	_, err := s.RecordPendingSender(t.Context(), db.RecordPendingSenderParams{
		ChannelID: uuid.New(), SenderRef: "u-1", SenderName: "Asha", Cap: testCap,
	})
	wantPGCode(t, err, foreignKeyViolation, "a pending sender in a channel that does not exist")
}
