package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// insertMessage is for the tests that need the message to exist. A replay
// returns no row, which pgx reports as ErrNoRows — so this fataling on any
// error also fatals on a duplicate, which is what a caller expecting a write
// wants.
func insertMessage(t *testing.T, s *Store, arg db.InsertMessageParams) uuid.UUID {
	t.Helper()

	id, err := s.InsertMessage(t.Context(), arg)
	if err != nil {
		t.Fatalf("InsertMessage(%q): %v", arg.ExternalID, err)
	}
	if id == uuid.Nil {
		t.Fatalf("InsertMessage(%q) returned the nil uuid", arg.ExternalID)
	}
	return id
}

// replayMessage is the other half: it asserts the write was absorbed, and
// that nothing came back to attach anything to.
func replayMessage(t *testing.T, s *Store, arg db.InsertMessageParams) {
	t.Helper()

	id, err := s.InsertMessage(t.Context(), arg)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a replay of %q returned id %s, err %v; want ErrNoRows",
			arg.ExternalID, id, err)
	}
	if id != uuid.Nil {
		t.Errorf("a replay returned id %s alongside the error", id)
	}
}

// seedInbox gives a test a channel, an open session and an approved sender,
// which is the only state in which a message can exist at all.
func seedInbox(t *testing.T, s *Store, name string) (db.Channel, db.Session, uuid.UUID) {
	t.Helper()

	team := mustCreate(t, s, "platform-"+name)
	ch := mustCreateChannel(t, s, team.ID, "custom", name)
	userID := insertUser(t, s, "dev", "asha-"+name)
	session := mustEnsureSession(t, s, team.ID, ch.ID, "c-"+name, "direct")
	return ch, session, userID
}

func mustEnsureSession(
	t *testing.T, s *Store, teamID, channelID uuid.UUID, ref, kind string,
) db.Session {
	t.Helper()

	session, err := s.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: teamID, ChannelID: channelID, ProviderRef: ref, Kind: kind,
	})
	if err != nil {
		t.Fatalf("EnsureSession(%q): %v", ref, err)
	}
	return session
}

func message(ch db.Channel, session db.Session, userID uuid.UUID, externalID, text string) db.InsertMessageParams {
	return db.InsertMessageParams{
		ChannelID:  ch.ID,
		SessionID:  session.ID,
		UserID:     userID,
		ExternalID: externalID,
		Text:       text,
	}
}

func listMessages(t *testing.T, s *Store, sessionID uuid.UUID, size int32) []db.Message {
	t.Helper()

	rows, err := s.ListMessagesBySession(t.Context(), db.ListMessagesBySessionParams{
		SessionID: sessionID, PageSize: size,
	})
	if err != nil {
		t.Fatalf("ListMessagesBySession: %v", err)
	}
	return rows
}

func TestAMessageRoundTrips(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")

	id := insertMessage(t, s, message(ch, session, userID, "m-1", "hello"))

	rows := listMessages(t, s, session.ID, 10)
	if len(rows) != 1 {
		t.Fatalf("%d messages, want 1", len(rows))
	}
	if rows[0].Text != "hello" || rows[0].ExternalID != "m-1" || rows[0].UserID != userID {
		t.Errorf("row = %+v", rows[0])
	}
	if rows[0].SessionID != session.ID {
		t.Errorf("SessionID = %s, want %s", rows[0].SessionID, session.ID)
	}

	// The id the insert reported is the id the row has. An attachment is
	// written against it in the same request, so a wrong one is a foreign key
	// violation at best and somebody else's message at worst.
	if rows[0].ID != id {
		t.Errorf("InsertMessage returned %s, the row is %s", id, rows[0].ID)
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
	ch, session, userID := seedInbox(t, s, "support")

	insertMessage(t, s, message(ch, session, userID, "m-1", "hello"))
	replayMessage(t, s, message(ch, session, userID, "m-1", "hello again"))

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

// The uniqueness is per channel and not per session, which matters when a
// session has been closed and reopened: a retry arriving afterwards must not
// become a second copy under the new session.
func TestAReplayIntoASecondSessionIsStillOneRow(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, first, userID := seedInbox(t, s, "support")

	insertMessage(t, s, message(ch, first, userID, "m-1", "hello"))

	// Close it and open another for the same conversation, as an idle sweep
	// eventually will.
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE sessions SET status = 'closed' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("close session: %v", err)
	}
	second := mustEnsureSession(t, s, ch.TeamID, ch.ID, *first.ProviderRef, "direct")
	if second.ID == first.ID {
		t.Fatal("a closed session was reused")
	}

	replayMessage(t, s, message(ch, second, userID, "m-1", "hello"))
	if n := scalar[int](t, s, `SELECT count(*) FROM messages WHERE channel_id = $1`, ch.ID); n != 1 {
		t.Errorf("%d copies of one message, want 1", n)
	}
}

// Slack's ts is unique within a channel, not globally.
func TestTheSameExternalIDInTwoChannelsIsTwoRows(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	firstCh, firstSession, firstUser := seedInbox(t, s, "one")
	secondCh, secondSession, secondUser := seedInbox(t, s, "two")

	insertMessage(t, s, message(firstCh, firstSession, firstUser, "m-1", "hello"))
	insertMessage(t, s, message(secondCh, secondSession, secondUser, "m-1", "hello"))

	if n := scalar[int](t, s, `SELECT count(*) FROM messages`); n != 2 {
		t.Errorf("%d rows across two channels, want 2", n)
	}
}

func TestDeletingAChannelDeletesItsMessages(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")
	insertMessage(t, s, message(ch, session, userID, "m-1", "hello"))

	if err := s.DeleteChannel(t.Context(), ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d messages survived their channel", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM sessions`); n != 0 {
		t.Errorf("%d sessions survived their channel", n)
	}
}

// A session going takes its messages with it, or they point at a conversation
// nothing can name.
func TestDeletingASessionDeletesItsMessages(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")
	insertMessage(t, s, message(ch, session, userID, "m-1", "hello"))

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM sessions WHERE id = $1`, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d messages survived their session", n)
	}
}

// Deleting somebody has to take what they said with them, or their messages
// point at a user id nothing can resolve.
func TestDeletingAUserDeletesTheirMessages(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")
	insertMessage(t, s, message(ch, session, userID, "m-1", "hello"))

	if _, err := s.pool.Exec(t.Context(), "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d messages survived their sender", n)
	}
}

// A message with no external id could never be deduplicated, so every retry
// would store it again.
func TestAMessageNeedsAnExternalID(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")

	_, err := s.InsertMessage(t.Context(), message(ch, session, userID, "", "hello"))
	wantPGCode(t, err, checkViolation, "a message with no external id")
}

// A message with no session belongs to no conversation, and nothing could
// ever read it back.
func TestAMessageNeedsASessionThatExists(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")

	arg := message(ch, session, userID, "m-1", "hello")
	arg.SessionID = uuid.New()

	_, err := s.InsertMessage(t.Context(), arg)
	wantPGCode(t, err, foreignKeyViolation, "a message in a session that does not exist")
}

// A message from somebody who is not a user cannot exist: the approval flow
// is what produces the id, so a row without one means it was bypassed.
func TestAMessageNeedsAKnownSender(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, _ := seedInbox(t, s, "support")

	_, err := s.InsertMessage(t.Context(), message(ch, session, uuid.New(), "m-1", "hello"))
	wantPGCode(t, err, foreignKeyViolation, "a message from a user that does not exist")
}

func TestMessagesComeBackNewestFirst(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")

	now := time.Now()
	for i, id := range []string{"oldest", "middle", "newest"} {
		insertMessage(t, s, message(ch, session, userID, id, id))
		if _, err := s.pool.Exec(t.Context(),
			"UPDATE messages SET received_at = $1 WHERE external_id = $2",
			now.Add(time.Duration(i-3)*time.Hour), id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	rows := listMessages(t, s, session.ID, 10)
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
	ch, session, userID := seedInbox(t, s, "support")

	// Claimed to be from the future, but it arrived first.
	early := message(ch, session, userID, "arrived-first", "first")
	future := time.Now().Add(24 * time.Hour)
	early.SentAt = &future
	insertMessage(t, s, early)

	late := message(ch, session, userID, "arrived-second", "second")
	past := time.Now().Add(-24 * time.Hour)
	late.SentAt = &past
	insertMessage(t, s, late)

	if _, err := s.pool.Exec(t.Context(),
		"UPDATE messages SET received_at = now() - interval '1 hour' WHERE external_id = 'arrived-first'"); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rows := listMessages(t, s, session.ID, 10)
	if rows[0].ExternalID != "arrived-second" {
		t.Errorf("order = %v, want the one that arrived last first — sent_at reordered the inbox",
			externalIDs(rows))
	}
}

// Two conversations in one channel are two inboxes. Reading one must not show
// the other, which is the whole reason a message points at a session.
func TestListingShowsOnlyThatSession(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, mine, userID := seedInbox(t, s, "support")
	theirs := mustEnsureSession(t, s, ch.TeamID, ch.ID, "c-other", "thread")

	insertMessage(t, s, message(ch, mine, userID, "m-1", "ours"))
	insertMessage(t, s, message(ch, theirs, userID, "m-2", "theirs"))

	rows := listMessages(t, s, mine.ID, 10)
	if len(rows) != 1 || rows[0].Text != "ours" {
		t.Errorf("got %v, want only this session's message", externalIDs(rows))
	}
}

func TestMessagesPageFromACursor(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	ch, session, userID := seedInbox(t, s, "support")

	now := time.Now()
	for i, id := range []string{"oldest", "middle", "newest"} {
		insertMessage(t, s, message(ch, session, userID, id, id))
		if _, err := s.pool.Exec(t.Context(),
			"UPDATE messages SET received_at = $1 WHERE external_id = $2",
			now.Add(time.Duration(i-3)*time.Hour), id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	first := listMessages(t, s, session.ID, 2)
	if got := externalIDs(first); len(got) != 2 || got[0] != "newest" {
		t.Fatalf("first page = %v", got)
	}

	last := first[len(first)-1]
	second, err := s.ListMessagesBySession(t.Context(), db.ListMessagesBySessionParams{
		SessionID:       session.ID,
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
