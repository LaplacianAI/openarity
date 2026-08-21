package store

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func insertMessage(t *testing.T, s *Store, arg db.InsertMessageParams) int64 {
	t.Helper()

	n, err := s.InsertMessage(t.Context(), arg)
	if err != nil {
		t.Fatalf("InsertMessage(%q): %v", arg.ExternalID, err)
	}
	return n
}

// seedInbox gives a test a channel and an approved sender, which is the only
// state in which a message can exist at all.
func seedInbox(t *testing.T, s *Store, channelName string) (db.Channel, uuid.UUID) {
	t.Helper()

	team := mustCreate(t, s, "platform-"+channelName)
	ch := mustCreateChannel(t, s, team.ID, "custom", channelName)
	userID := insertUser(t, s, "dev", "asha-"+channelName)
	return ch, userID
}

func message(ch db.Channel, userID uuid.UUID, externalID, text string) db.InsertMessageParams {
	return db.InsertMessageParams{
		ChannelID:       ch.ID,
		UserID:          userID,
		ExternalID:      externalID,
		ConversationRef: "c-1",
		Text:            text,
	}
}

func TestAMessageRoundTrips(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")

	if n := insertMessage(t, s, message(ch, userID, "m-1", "hello")); n != 1 {
		t.Fatalf("wrote %d rows, want 1", n)
	}

	rows, err := s.ListMessagesByChannel(t.Context(), db.ListMessagesByChannelParams{
		ChannelID: ch.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListMessagesByChannel: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d messages, want 1", len(rows))
	}
	if rows[0].Text != "hello" || rows[0].ExternalID != "m-1" || rows[0].UserID != userID {
		t.Errorf("row = %+v", rows[0])
	}
	if rows[0].ReceivedAt.IsZero() {
		t.Error("received_at was not defaulted")
	}
	if rows[0].SentAt != nil {
		t.Errorf("sent_at = %v, want null when the provider did not say", *rows[0].SentAt)
	}
}

// Every provider retries when a response is slow. The index absorbing the
// duplicate is what lets the handler answer 200 without asking first.
func TestTheSameExternalIDTwiceInOneChannelIsOneRow(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")

	if n := insertMessage(t, s, message(ch, userID, "m-1", "hello")); n != 1 {
		t.Fatalf("first write reported %d rows, want 1", n)
	}
	if n := insertMessage(t, s, message(ch, userID, "m-1", "hello again")); n != 0 {
		t.Errorf("a replay wrote %d rows, want 0", n)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM messages WHERE channel_id = $1`, ch.ID); n != 1 {
		t.Errorf("%d rows after a replay, want 1", n)
	}

	// DO NOTHING and not DO UPDATE: the first version of a message is what
	// was approved and stored, and a replay carrying different text is a
	// provider bug or somebody editing history.
	text := scalar[string](t, s, `SELECT text FROM messages WHERE channel_id = $1`, ch.ID)
	if text != "hello" {
		t.Errorf("text = %q, want the first version", text)
	}
}

// Slack's ts is unique within a channel, not globally.
func TestTheSameExternalIDInTwoChannelsIsTwoRows(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	first, firstUser := seedInbox(t, s, "one")
	second, secondUser := seedInbox(t, s, "two")

	insertMessage(t, s, message(first, firstUser, "m-1", "hello"))
	insertMessage(t, s, message(second, secondUser, "m-1", "hello"))

	if n := scalar[int](t, s, `SELECT count(*) FROM messages`); n != 2 {
		t.Errorf("%d rows across two channels, want 2", n)
	}
}

func TestDeletingAChannelDeletesItsMessages(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")
	insertMessage(t, s, message(ch, userID, "m-1", "hello"))

	if err := s.DeleteChannel(t.Context(), ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d messages survived their channel", n)
	}
}

// Deleting somebody has to take what they said with them, or their messages
// point at a user id nothing can resolve.
func TestDeletingAUserDeletesTheirMessages(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")
	insertMessage(t, s, message(ch, userID, "m-1", "hello"))

	if _, err := s.pool.Exec(t.Context(), "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d messages survived their sender", n)
	}
}

// A message with no external id could never be deduplicated, so every retry
// would store it again.
func TestAMessageNeedsAnExternalIDAndAConversation(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")

	for what, arg := range map[string]db.InsertMessageParams{
		"no external id":      {ChannelID: ch.ID, UserID: userID, ExternalID: "", ConversationRef: "c-1"},
		"no conversation ref": {ChannelID: ch.ID, UserID: userID, ExternalID: "m-1", ConversationRef: ""},
	} {
		_, err := s.InsertMessage(t.Context(), arg)
		wantPGCode(t, err, checkViolation, "a message with "+what)
	}
}

// A message from somebody who is not a user cannot exist: the approval flow
// is what produces the id, so a row without one means it was bypassed.
func TestAMessageNeedsAKnownSender(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, _ := seedInbox(t, s, "support")

	_, err := s.InsertMessage(t.Context(), message(ch, uuid.New(), "m-1", "hello"))
	wantPGCode(t, err, foreignKeyViolation, "a message from a user that does not exist")
}

func TestMessagesComeBackNewestFirst(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")

	now := time.Now()
	for i, id := range []string{"oldest", "middle", "newest"} {
		insertMessage(t, s, message(ch, userID, id, id))
		if _, err := s.pool.Exec(t.Context(),
			"UPDATE messages SET received_at = $1 WHERE external_id = $2",
			now.Add(time.Duration(i-3)*time.Hour), id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	rows, err := s.ListMessagesByChannel(t.Context(), db.ListMessagesByChannelParams{
		ChannelID: ch.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListMessagesByChannel: %v", err)
	}

	want := []string{"newest", "middle", "oldest"}
	for i := range want {
		if rows[i].ExternalID != want[i] {
			t.Fatalf("order = %v, want %v", externalIDs(rows), want)
		}
	}
}

// The order is ours, not the sender's. sent_at is a clock we do not control —
// a provider with a wrong one, or somebody setting it deliberately, would
// otherwise reorder an inbox they do not own.
func TestOrderingIgnoresTheSendersClock(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")

	// Claimed to be from the future, but it arrived first.
	early := message(ch, userID, "arrived-first", "first")
	future := time.Now().Add(24 * time.Hour)
	early.SentAt = &future
	insertMessage(t, s, early)

	late := message(ch, userID, "arrived-second", "second")
	past := time.Now().Add(-24 * time.Hour)
	late.SentAt = &past
	insertMessage(t, s, late)

	if _, err := s.pool.Exec(t.Context(),
		"UPDATE messages SET received_at = now() - interval '1 hour' WHERE external_id = 'arrived-first'"); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rows, err := s.ListMessagesByChannel(t.Context(), db.ListMessagesByChannelParams{
		ChannelID: ch.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListMessagesByChannel: %v", err)
	}
	if rows[0].ExternalID != "arrived-second" {
		t.Errorf("order = %v, want the one that arrived last first — sent_at reordered the inbox",
			externalIDs(rows))
	}
}

func TestListMessagesShowsOnlyThatChannel(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	mine, mineUser := seedInbox(t, s, "mine")
	theirs, theirsUser := seedInbox(t, s, "theirs")

	insertMessage(t, s, message(mine, mineUser, "m-1", "ours"))
	insertMessage(t, s, message(theirs, theirsUser, "m-2", "theirs"))

	rows, err := s.ListMessagesByChannel(t.Context(), db.ListMessagesByChannelParams{
		ChannelID: mine.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListMessagesByChannel: %v", err)
	}
	if len(rows) != 1 || rows[0].Text != "ours" {
		t.Errorf("got %v, want only this channel's message", externalIDs(rows))
	}
}

func TestMessagesPageFromACursor(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, userID := seedInbox(t, s, "support")

	now := time.Now()
	for i, id := range []string{"oldest", "middle", "newest"} {
		insertMessage(t, s, message(ch, userID, id, id))
		if _, err := s.pool.Exec(t.Context(),
			"UPDATE messages SET received_at = $1 WHERE external_id = $2",
			now.Add(time.Duration(i-3)*time.Hour), id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	first, err := s.ListMessagesByChannel(t.Context(), db.ListMessagesByChannelParams{
		ChannelID: ch.ID, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := externalIDs(first); len(got) != 2 || got[0] != "newest" {
		t.Fatalf("first page = %v", got)
	}

	last := first[len(first)-1]
	second, err := s.ListMessagesByChannel(t.Context(), db.ListMessagesByChannelParams{
		ChannelID:       ch.ID,
		PageSize:        2,
		UseCursor:       true,
		AfterReceivedAt: last.ReceivedAt,
		AfterID:         last.ID,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := externalIDs(second); len(got) != 1 || got[0] != "oldest" {
		t.Errorf("second page = %v, want [oldest]", got)
	}
}

func externalIDs(rows []db.Message) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.ExternalID
	}
	return out
}
