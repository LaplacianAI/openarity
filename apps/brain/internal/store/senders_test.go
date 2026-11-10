package store

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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

	if _, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
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
	_, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
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

	var holder *uuid.UUID
	for _, userID := range []uuid.UUID{first, second} {
		if _, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
			ChannelID: ch.ID, SenderRef: "u-1", UserID: userID, ExpectedHolder: holder,
		}); err != nil {
			t.Fatalf("ApproveSender: %v", err)
		}
		holder = &userID
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
		if _, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
			ChannelID: ch.ID, SenderRef: "approved", UserID: userID,
		}); err != nil {
			t.Fatalf("ApproveSender: %v", err)
		}
		if _, err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
			ChannelID: ch.ID, SenderRef: "approved", ExpectedHolder: &userID,
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
		if _, err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
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

	if _, err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
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

	if _, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
		ChannelID: ch.ID, SenderRef: "u-1", UserID: userID,
	}); err != nil {
		t.Fatalf("ApproveSender: %v", err)
	}
	if _, err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
		ChannelID: ch.ID, SenderRef: "u-1", ExpectedHolder: &userID,
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

// --- writes that name the holder they checked ---

// approveAs writes a binding and returns how many the statement wrote.
func approveAs(t *testing.T, s *Store, ch, user uuid.UUID, ref string, expected *uuid.UUID) int64 {
	t.Helper()

	n, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
		ChannelID: ch, SenderRef: ref, UserID: user, ExpectedHolder: expected,
	})
	if err != nil {
		t.Fatalf("ApproveSender: %v", err)
	}
	return n.Linked
}

func holderOf(t *testing.T, s *Store, ch uuid.UUID, ref string) uuid.UUID {
	t.Helper()
	return scalar[uuid.UUID](t, s,
		`SELECT user_id FROM channel_senders WHERE channel_id = $1 AND sender_ref = $2`, ch, ref)
}

// The handler checks who holds a ref, then writes. A write naming a holder
// that is not the one there now changes nothing — neither the binding nor the
// queue — and says so, so the caller can be told rather than succeed.
func TestAWriteNamingAnotherHolderChangesNothing(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	admin := insertUser(t, s, "dev", "admin")
	other := insertUser(t, s, "dev", "other")
	approver := insertUser(t, s, "dev", "approver")

	approveAs(t, s, ch.ID, admin, "u-1", nil)
	record(t, s, ch.ID, "u-1", "Asha")

	t.Run("approving, having read it unbound", func(t *testing.T) {
		if n := approveAs(t, s, ch.ID, approver, "u-1", nil); n != 0 {
			t.Errorf("wrote %d bindings over a holder nobody checked", n)
		}
	})
	t.Run("approving, having read another holder", func(t *testing.T) {
		if n := approveAs(t, s, ch.ID, approver, "u-1", &other); n != 0 {
			t.Errorf("wrote %d bindings over a holder nobody checked", n)
		}
	})
	t.Run("removing, having read another holder", func(t *testing.T) {
		removed, err := s.RemoveSender(t.Context(), db.RemoveSenderParams{
			ChannelID: ch.ID, SenderRef: "u-1", ExpectedHolder: &other,
		})
		if err != nil {
			t.Fatalf("RemoveSender: %v", err)
		}
		if removed {
			t.Error("a removal naming the wrong holder reported success")
		}
	})

	if got := holderOf(t, s, ch.ID, "u-1"); got != admin {
		t.Errorf("the binding moved to %s; the admin's should stand", got)
	}
	if rows := pending(t, s, ch.ID); len(rows) != 1 {
		t.Errorf("%d pending rows; a refused write must leave the queue alone", len(rows))
	}
}

// A ref read as unbound and bound since was never checked, so a removal
// leaves it, and still clears what was pending: that is all it was asked.
func TestRemovingARefReadAsUnboundLeavesALaterBinding(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	admin := insertUser(t, s, "dev", "admin")

	approveAs(t, s, ch.ID, admin, "u-1", nil)

	removed, err := s.RemoveSender(t.Context(), db.RemoveSenderParams{ChannelID: ch.ID, SenderRef: "u-1"})
	if err != nil {
		t.Fatalf("RemoveSender: %v", err)
	}
	if !removed {
		t.Error("a removal of a ref read as unbound refused")
	}
	if got := holderOf(t, s, ch.ID, "u-1"); got != admin {
		t.Errorf("the binding is %s; one nobody checked must stand", got)
	}
}

// The race the holder check exists for: the binding moves while the write is
// waiting on its row. Another transaction holds the row, moved, uncommitted;
// the write blocks on it, and once that commits it must see the new holder and
// refuse — not overwrite or delete the binding of somebody nobody checked.
//
// Deterministic rather than raced: the test holds the lock itself and asserts
// the write is still blocked before letting go.
func TestABindingMovedWhileTheWriteWaitsIsRefused(t *testing.T) {
	// One store for both: its schema is named after the test, and a
	// subtest's name has a slash in it.
	s := queryStore(t)

	for name, write := range map[string]func(s *Store, ch, approver, checked uuid.UUID) (bool, error){
		"approving": func(s *Store, ch, approver, checked uuid.UUID) (bool, error) {
			n, err := s.ApproveSender(t.Context(), db.ApproveSenderParams{
				ChannelID: ch, SenderRef: "u-1", UserID: approver, ExpectedHolder: &checked,
			})
			return n.Linked > 0, err
		},
		"removing": func(s *Store, ch, _, checked uuid.UUID) (bool, error) {
			return s.RemoveSender(t.Context(), db.RemoveSenderParams{
				ChannelID: ch, SenderRef: "u-1", ExpectedHolder: &checked,
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			team := mustCreate(t, s, "platform-"+name)
			ch := mustCreateChannel(t, s, team.ID, "custom", "support")
			member := insertUser(t, s, "dev", "member-"+name)
			admin := insertUser(t, s, "dev", "admin-"+name)
			approver := insertUser(t, s, "dev", "approver-"+name)

			// The handler checked the member; the binding is the member's.
			approveAs(t, s, ch.ID, member, "u-1", nil)

			// An admin moves it to themselves, and has not committed yet.
			tx, err := s.Pool().Begin(t.Context())
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })
			if _, err := tx.Exec(t.Context(),
				`UPDATE channel_senders SET user_id = $1 WHERE channel_id = $2 AND sender_ref = 'u-1'`,
				admin, ch.ID); err != nil {
				t.Fatalf("moving the binding: %v", err)
			}

			type result struct {
				wrote bool
				err   error
			}
			done := make(chan result, 1)
			go func() {
				wrote, err := write(s, ch.ID, approver, member)
				done <- result{wrote, err}
			}()

			select {
			case r := <-done:
				t.Fatalf("the write finished while the row was held: %+v", r)
			case <-time.After(500 * time.Millisecond):
				// Correct: it is waiting on the admin's transaction.
			}

			if err := tx.Commit(t.Context()); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			r := <-done
			if r.err != nil {
				t.Fatalf("write: %v", r.err)
			}
			if r.wrote {
				t.Error("the write went through against a holder that moved while it waited")
			}
			if got := holderOf(t, s, ch.ID, "u-1"); got != admin {
				t.Errorf("the binding is %s; the admin's, made while the write waited, should stand", got)
			}
		})
	}
}

// --- the team's queue ---

// seenAt sets when a sender was first seen to an exact instant, so two can
// tie and the order between them is the cursor's to decide.
func seenAt(t *testing.T, s *Store, channelID uuid.UUID, ref string, at time.Time) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(),
		`UPDATE pending_senders SET first_seen = $3 WHERE channel_id = $1 AND sender_ref = $2`,
		channelID, ref, at); err != nil {
		t.Fatalf("seenAt %s: %v", ref, err)
	}
}

func teamQueue(t *testing.T, s *Store, p db.ListTeamPendingSendersParams) []db.ListTeamPendingSendersRow {
	t.Helper()

	if p.PageSize == 0 {
		p.PageSize = 1000
	}
	rows, err := s.ListTeamPendingSenders(t.Context(), p)
	if err != nil {
		t.Fatalf("ListTeamPendingSenders: %v", err)
	}
	return rows
}

func refsOf(rows []db.ListTeamPendingSendersRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.PendingSender.SenderRef
	}
	return out
}

// Whoever has waited longest comes first, whichever channel they wrote in
// on, with the channel named; another team's strangers are not this team's.
func TestTheTeamQueueIsOldestFirstAcrossChannels(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	support := mustCreateChannel(t, s, team.ID, "custom", "support")
	sales := mustCreateChannel(t, s, team.ID, "slack", "sales")
	theirs := mustCreateChannel(t, s, mustCreate(t, s, "elsewhere").ID, "custom", "support")

	base := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for _, w := range []struct {
		channel uuid.UUID
		ref     string
		minutes int
	}{
		{support.ID, "newest", 30},
		{sales.ID, "oldest", 10},
		{support.ID, "middle", 20},
		{theirs.ID, "theirs", 0},
	} {
		record(t, s, w.channel, w.ref, w.ref)
		seenAt(t, s, w.channel, w.ref, base.Add(time.Duration(w.minutes)*time.Minute))
	}

	rows := teamQueue(t, s, db.ListTeamPendingSendersParams{TeamID: team.ID})
	if got, want := strings.Join(refsOf(rows), ","), "oldest,middle,newest"; got != want {
		t.Fatalf("the queue reads %s, want %s", got, want)
	}
	if r := rows[0]; r.ChannelName != "sales" || r.ChannelProvider != "slack" || r.PendingSender.ChannelID != sales.ID {
		t.Errorf("the oldest is named as %q on %q (%s), want sales on slack (%s)",
			r.ChannelName, r.ChannelProvider, r.PendingSender.ChannelID, sales.ID)
	}
}

// Senders first seen in the same instant are ordered by channel, then by
// ref, and paging one at a time through the tie returns each exactly once:
// a cursor of the time and the ref alone would skip or repeat across it,
// because a ref is unique only within its channel.
func TestPagingThroughATieReturnsEverySenderOnce(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	a := mustCreateChannel(t, s, team.ID, "custom", "a")
	b := mustCreateChannel(t, s, team.ID, "custom", "b")
	if bytes.Compare(a.ID[:], b.ID[:]) > 0 {
		a, b = b, a
	}

	same := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for _, w := range []struct {
		channel uuid.UUID
		ref     string
	}{{b.ID, "U1"}, {a.ID, "U2"}, {a.ID, "U1"}, {b.ID, "U0"}} {
		record(t, s, w.channel, w.ref, w.ref)
		seenAt(t, s, w.channel, w.ref, same)
	}
	// One sender a moment later, so the tie is not the whole queue.
	record(t, s, a.ID, "later", "later")
	seenAt(t, s, a.ID, "later", same.Add(time.Second))

	want := "a/U1,a/U2,b/U0,b/U1,a/later"
	name := map[uuid.UUID]string{a.ID: "a", b.ID: "b"}
	label := func(r db.ListTeamPendingSendersRow) string {
		return name[r.PendingSender.ChannelID] + "/" + r.PendingSender.SenderRef
	}

	var whole []string
	for _, r := range teamQueue(t, s, db.ListTeamPendingSendersParams{TeamID: team.ID}) {
		whole = append(whole, label(r))
	}
	if got := strings.Join(whole, ","); got != want {
		t.Fatalf("the queue reads %s, want %s", got, want)
	}

	var paged []string
	p := db.ListTeamPendingSendersParams{TeamID: team.ID, PageSize: 1}
	for range 10 {
		rows := teamQueue(t, s, p)
		if len(rows) == 0 {
			break
		}
		last := rows[0].PendingSender
		paged = append(paged, label(rows[0]))
		p.UseCursor = true
		p.AfterFirstSeen, p.AfterChannelID, p.AfterRef = last.FirstSeen, last.ChannelID, last.SenderRef
	}
	if got := strings.Join(paged, ","); got != want {
		t.Errorf("paged one at a time, the queue reads %s, want %s", got, want)
	}
}

// An approved sender has left every channel's queue, so the team's too.
func TestAnApprovedSenderLeavesTheTeamQueue(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")
	ch := mustCreateChannel(t, s, team.ID, "custom", "support")
	user := insertUser(t, s, "dev", "asha")
	addMember(t, s, team.ID, user, "member")

	record(t, s, ch.ID, "U1", "Asha")
	record(t, s, ch.ID, "U2", "Bala")
	approveAs(t, s, ch.ID, user, "U1", nil)

	if got := strings.Join(refsOf(teamQueue(t, s, db.ListTeamPendingSendersParams{TeamID: team.ID})), ","); got != "U2" {
		t.Errorf("the queue reads %s after U1 was approved, want U2", got)
	}
}
