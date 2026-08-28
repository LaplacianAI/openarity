package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// seedMessage gives a test a message to hang attachments from, and the team
// that message belongs to. Every attachment needs both: one to reference, and
// one to check the read path's join answers correctly.
func seedMessage(t *testing.T, s *Store, name string) (messageID, sessionID, teamID uuid.UUID) {
	t.Helper()

	ch, session, userID := seedInbox(t, s, name)
	insertMessage(t, s, message(ch, session, userID, "ext-"+name, "hello"))

	rows := listMessages(t, s, session.ID, 10)
	if len(rows) != 1 {
		t.Fatalf("seeded %d messages, want 1", len(rows))
	}
	return rows[0].ID, session.ID, session.TeamID
}

func attachment(messageID, sessionID uuid.UUID, key string) db.CreateAttachmentParams {
	return db.CreateAttachmentParams{
		MessageID:  messageID,
		SessionID:  sessionID,
		ObjectKey:  key,
		KeyVersion: 1,
		MediaType:  "image/png",
		SizeBytes:  1024,
		Filename:   "holiday.png",
	}
}

func mustCreateAttachment(t *testing.T, s *Store, arg db.CreateAttachmentParams) db.Attachment {
	t.Helper()

	row, err := s.CreateAttachment(t.Context(), arg)
	if err != nil {
		t.Fatalf("CreateAttachment(%q): %v", arg.ObjectKey, err)
	}
	return row
}

// constraintName reports which CHECK or key refused a write, so a test can
// say it was refused *for the reason it expected* rather than for any reason.
func constraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func TestAttachmentRoundTrips(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "round-trip")
	key := "teams/" + uuid.NewString() + "/objects/abc"

	row := mustCreateAttachment(t, s, attachment(messageID, sessionID, key))

	if row.ObjectKey != key {
		t.Errorf("ObjectKey = %q, want %q", row.ObjectKey, key)
	}
	if row.KeyVersion != 1 {
		t.Errorf("KeyVersion = %d, want 1", row.KeyVersion)
	}
	if row.SizeBytes != 1024 {
		t.Errorf("SizeBytes = %d, want 1024", row.SizeBytes)
	}
	if row.ID == uuid.Nil {
		t.Error("ID is the zero uuid")
	}
	if row.CreatedAt.IsZero() {
		t.Error("CreatedAt was not defaulted")
	}
}

// The read path's whole authorisation story in one query: the team comes back
// with the row, so there is never a second round trip and never a moment when
// the object is known and the team is not.
func TestGetAttachmentWithTeamReturnsTheOwningTeam(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, teamID := seedMessage(t, s, "with-team")
	created := mustCreateAttachment(t, s, attachment(messageID, sessionID, "teams/x/objects/abc"))

	got, err := s.GetAttachmentWithTeam(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetAttachmentWithTeam: %v", err)
	}
	if got.TeamID != teamID {
		t.Errorf("TeamID = %s, want %s", got.TeamID, teamID)
	}
	if got.Attachment.ID != created.ID {
		t.Errorf("Attachment.ID = %s, want %s", got.Attachment.ID, created.ID)
	}
	if got.Attachment.MediaType != "image/png" {
		t.Errorf("MediaType = %q", got.Attachment.MediaType)
	}
}

// The bug this query was written to avoid, made reachable.
//
// The plan joined through channels. sessions.channel_id is nullable — the
// dashboard and the API start sessions with no channel behind them — so that
// join returns no row for those, and an attachment that exists reads as one
// that does not.
//
// Today both joins happen to work, because messages.channel_id is still NOT
// NULL and every message therefore has a channel. That column is a stand-in
// for the orchestrator by its own migration's admission, so this test drops
// the constraint *in its own schema* and asserts the query survives the
// change rather than waiting to find out.
func TestAnAttachmentOnAChannellessSessionStillResolvesItsTeam(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform-channelless")
	userID := insertUser(t, s, "dev", "asha-channelless")

	// This test owns its schema, so relaxing the column here reaches nothing
	// else. It is the schema messages will have once a session can start
	// without a channel.
	if _, err := s.pool.Exec(t.Context(),
		`ALTER TABLE messages ALTER COLUMN channel_id DROP NOT NULL`); err != nil {
		t.Fatalf("relaxing messages.channel_id: %v", err)
	}

	var sessionID uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`,
		team.ID,
	).Scan(&sessionID); err != nil {
		t.Fatalf("inserting a session with no channel: %v", err)
	}

	var messageID uuid.UUID
	if err := s.pool.QueryRow(t.Context(),
		`INSERT INTO messages (channel_id, session_id, user_id, external_id, text)
		 VALUES (NULL, $1, $2, 'ext-channelless', 'hello') RETURNING id`,
		sessionID, userID,
	).Scan(&messageID); err != nil {
		t.Fatalf("inserting a message with no channel: %v", err)
	}

	created := mustCreateAttachment(t, s, attachment(messageID, sessionID, "teams/x/objects/abc"))

	got, err := s.GetAttachmentWithTeam(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetAttachmentWithTeam: %v", err)
	}
	if got.TeamID != team.ID {
		t.Errorf("TeamID = %s, want %s", got.TeamID, team.ID)
	}

	// And the join the plan proposed would have found nothing here, which is
	// the whole reason this query goes through sessions.
	var viaChannels int
	if err := s.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM attachments a
		 JOIN messages m ON m.id = a.message_id
		 JOIN channels c ON c.id = m.channel_id
		 WHERE a.id = $1`, created.ID).Scan(&viaChannels); err != nil {
		t.Fatalf("counting through channels: %v", err)
	}
	if viaChannels != 0 {
		t.Errorf("the channels join found %d rows, so this test is no longer "+
			"exercising the case it was written for", viaChannels)
	}
}

// A session's team is the authoritative one, and the database keeps it equal
// to the channel's. Pinned because the query depends on that being true: if
// the two could disagree, joining through sessions would authorise against a
// different team than joining through channels.
func TestASessionsTeamCannotDisagreeWithItsChannels(t *testing.T) {
	s := queryStore(t)

	ch, session, _ := seedInbox(t, s, "same-team")
	if session.TeamID != ch.TeamID {
		t.Fatalf("session team %s, channel team %s", session.TeamID, ch.TeamID)
	}

	other := mustCreate(t, s, "platform-other-team")

	// The composite foreign key is what refuses this. Without it a session
	// could name a team its channel is not in.
	_, err := s.pool.Exec(t.Context(),
		`UPDATE sessions SET team_id = $1 WHERE id = $2`, other.ID, session.ID)
	if err == nil {
		t.Fatal("a session was moved to a team its channel is not in")
	}
	if name := constraintName(err); name != "sessions_channel_in_team" {
		t.Errorf("refused by %q, want sessions_channel_in_team: %v", name, err)
	}
}

func TestListAttachmentsByMessage(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "list")
	for _, key := range []string{"teams/x/objects/a", "teams/x/objects/b"} {
		mustCreateAttachment(t, s, attachment(messageID, sessionID, key))
	}

	// A second message's attachment must not appear in the first's list.
	otherID, otherSession, _ := seedMessage(t, s, "list-other")
	mustCreateAttachment(t, s, attachment(otherID, otherSession, "teams/x/objects/c"))

	rows, err := s.ListAttachmentsByMessage(t.Context(), messageID)
	if err != nil {
		t.Fatalf("ListAttachmentsByMessage: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d attachments, want 2", len(rows))
	}
	for _, row := range rows {
		if row.MessageID != messageID {
			t.Errorf("attachment %s belongs to message %s", row.ID, row.MessageID)
		}
	}
}

// Deletion asks this before removing an object. Two rows pointing at one key
// is not hypothetical bookkeeping — it is what content-addressed names produce
// when the same file is forwarded twice.
func TestCountAttachmentsByObjectKey(t *testing.T) {
	s := queryStore(t)

	first, firstSession, _ := seedMessage(t, s, "count-a")
	second, secondSession, _ := seedMessage(t, s, "count-b")
	shared := "teams/x/objects/shared"

	mustCreateAttachment(t, s, attachment(first, firstSession, shared))
	mustCreateAttachment(t, s, attachment(second, secondSession, shared))
	mustCreateAttachment(t, s, attachment(first, firstSession, "teams/x/objects/lonely"))

	for key, want := range map[string]int64{
		shared:                   2,
		"teams/x/objects/lonely": 1,
		"teams/x/objects/absent": 0,
	} {
		got, err := s.CountAttachmentsByObjectKey(t.Context(), key)
		if err != nil {
			t.Fatalf("CountAttachmentsByObjectKey(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("count(%q) = %d, want %d", key, got, want)
		}
	}
}

// Deleting a message takes its attachment rows with it. Deleting the objects
// is Task 7 and does not happen here — a cascade does not reach a bucket.
func TestDeletingAMessageRemovesItsAttachmentRows(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "cascade")
	created := mustCreateAttachment(t, s, attachment(messageID, sessionID, "teams/x/objects/abc"))

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM messages WHERE id = $1`, messageID); err != nil {
		t.Fatalf("deleting the message: %v", err)
	}

	if _, err := s.GetAttachmentWithTeam(t.Context(), created.ID); err == nil {
		t.Error("the attachment row survived its message")
	}
}

// Everything on this table that arrives from a webhook is bounded by the
// database, not only by whoever remembered to check. Each case names the
// constraint it expects, so a write refused by a different one fails rather
// than reading as success.
func TestTheDatabaseRefusesWhatTheColumnsCannotMean(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "constraints")

	for name, tc := range map[string]struct {
		mutate func(*db.CreateAttachmentParams)
		want   string
	}{
		"an empty object key": {
			func(a *db.CreateAttachmentParams) { a.ObjectKey = "" },
			"attachments_object_key_present",
		},
		"an empty media type": {
			func(a *db.CreateAttachmentParams) { a.MediaType = "" },
			"attachments_media_type_present",
		},
		"a negative size": {
			func(a *db.CreateAttachmentParams) { a.SizeBytes = -1 },
			"attachments_size_not_negative",
		},
		"a key version below one": {
			func(a *db.CreateAttachmentParams) { a.KeyVersion = 0 },
			"attachments_key_version_positive",
		},
		"a filename from a hostile provider": {
			func(a *db.CreateAttachmentParams) { a.Filename = strings.Repeat("a", 513) },
			"attachments_filename_bounded",
		},
		"a media type from the same": {
			func(a *db.CreateAttachmentParams) { a.MediaType = strings.Repeat("a", 256) },
			"attachments_media_type_bounded",
		},
		// The composite key replaced the single-column reference, so this is
		// the constraint that refuses an unknown message now.
		"a message that does not exist": {
			func(a *db.CreateAttachmentParams) { a.MessageID = uuid.New() },
			"attachments_message_in_session",
		},
		"a session the message is not in": {
			func(a *db.CreateAttachmentParams) { a.SessionID = uuid.New() },
			"attachments_message_in_session",
		},
	} {
		t.Run(name, func(t *testing.T) {
			arg := attachment(messageID, sessionID, "teams/x/objects/"+uuid.NewString())
			tc.mutate(&arg)

			_, err := s.CreateAttachment(t.Context(), arg)
			if err == nil {
				t.Fatalf("the database accepted %s", name)
			}
			if got := constraintName(err); got != tc.want {
				t.Errorf("refused by %q, want %q: %v", got, tc.want, err)
			}
		})
	}
}

// An empty filename is allowed. Some providers send none, and refusing the
// row would lose the attachment over a label — the bytes are what matter and
// the media type is what makes them safe to serve.
func TestAnEmptyFilenameIsAllowed(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "no-filename")

	arg := attachment(messageID, sessionID, "teams/x/objects/unnamed")
	arg.Filename = ""

	if _, err := s.CreateAttachment(t.Context(), arg); err != nil {
		t.Errorf("CreateAttachment with no filename: %v", err)
	}
}

// session_id is a second copy of a fact the message already carries, and it
// earns its place by being an access path no join can express: asking for a
// session's attachments through messages reads every message in the session
// and every attachment in the database. Measured on 200 sessions, 209k
// messages and 21k attachments, one session's attachments took 13.6 ms warm
// through the join and 0.36 ms through this column.
//
// The copy cannot drift. This is the test that says so rather than the
// comment.
func TestAnAttachmentCannotClaimAnotherSessionThanItsMessage(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "no-drift")
	_, elsewhere, _ := seedMessage(t, s, "elsewhere")

	created := mustCreateAttachment(t, s, attachment(messageID, sessionID, "teams/x/objects/abc"))

	_, err := s.pool.Exec(t.Context(),
		`UPDATE attachments SET session_id = $1 WHERE id = $2`, elsewhere, created.ID)
	if err == nil {
		t.Fatal("an attachment was moved to a session its message is not in")
	}
	if name := constraintName(err); name != "attachments_message_in_session" {
		t.Errorf("refused by %q, want attachments_message_in_session: %v", name, err)
	}

	// And it is still the session it was written with.
	got := listAttachments(t, s, sessionID, 10)
	if len(got) != 1 || got[0].ID != created.ID {
		t.Errorf("the session holds %d attachments, want the one written", len(got))
	}
}

// The query the column exists for: everything a session has accumulated,
// across every message in it.
func TestListAttachmentsBySession(t *testing.T) {
	s := queryStore(t)

	ch, session, userID := seedInbox(t, s, "by-session")

	// Two messages in one session, each with a file, plus a third message
	// with none — the agent asking "the file I sent earlier" spans them.
	for _, ext := range []string{"ext-1", "ext-2", "ext-3"} {
		insertMessage(t, s, message(ch, session, userID, ext, "hello"))
	}
	rows := listMessages(t, s, session.ID, 10)
	if len(rows) != 3 {
		t.Fatalf("seeded %d messages, want 3", len(rows))
	}
	for _, m := range rows[:2] {
		mustCreateAttachment(t, s, attachment(m.ID, session.ID, "teams/x/objects/"+m.ExternalID))
	}

	// A second session's attachment must not appear.
	otherID, otherSession, _ := seedMessage(t, s, "by-session-other")
	mustCreateAttachment(t, s, attachment(otherID, otherSession, "teams/x/objects/other"))

	got := listAttachments(t, s, session.ID, 10)
	if len(got) != 2 {
		t.Fatalf("got %d attachments, want 2", len(got))
	}
	for _, a := range got {
		if a.SessionID != session.ID {
			t.Errorf("attachment %s belongs to session %s", a.ID, a.SessionID)
		}
	}
}

func listAttachments(t *testing.T, s *Store, sessionID uuid.UUID, size int32) []db.Attachment {
	t.Helper()

	rows, err := s.ListAttachmentsBySession(t.Context(), db.ListAttachmentsBySessionParams{
		SessionID: sessionID,
		PageSize:  size,
	})
	if err != nil {
		t.Fatalf("ListAttachmentsBySession: %v", err)
	}
	return rows
}

// Paging on created_at alone would drop or repeat a row whenever two files
// share a timestamp, which every multi-file delivery produces: they are
// written in one request, and created_at defaults to now(), which is the
// transaction's start time and identical for all of them.
func TestAttachmentPagingIsStableWhenTimestampsTie(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "paging")
	for i := range 5 {
		mustCreateAttachment(t, s,
			attachment(messageID, sessionID, fmt.Sprintf("teams/x/objects/p-%d", i)))
	}

	// Each insert above was its own transaction, so now() gave each row a
	// different microsecond. Force the tie the query has to survive: a single
	// delivery writing several files inside one transaction gets one
	// created_at for all of them, because now() is the transaction's start.
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE attachments SET created_at = now() WHERE session_id = $1`, sessionID); err != nil {
		t.Fatalf("flatten timestamps: %v", err)
	}
	same := scalar[int](t, s,
		`SELECT count(DISTINCT created_at) FROM attachments WHERE session_id = $1`, sessionID)
	if same != 1 {
		t.Fatalf("%d distinct timestamps; the tie this test needs did not happen", same)
	}

	var seen []uuid.UUID
	params := db.ListAttachmentsBySessionParams{SessionID: sessionID, PageSize: 2}
	for range 5 {
		page, err := s.ListAttachmentsBySession(t.Context(), params)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, a := range page {
			seen = append(seen, a.ID)
		}
		last := page[len(page)-1]
		params.UseCursor = true
		params.AfterCreatedAt = last.CreatedAt
		params.AfterID = last.ID
	}

	if len(seen) != 5 {
		t.Fatalf("paged %d rows, want 5 — a tie was dropped or repeated: %v", len(seen), seen)
	}
	unique := map[uuid.UUID]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Errorf("%s came back on two pages", id)
		}
		unique[id] = true
	}
}

// The bytes route asks for one attachment inside a session it has already
// proven the caller may read. An id from another session is a missing row.
func TestGetAttachmentInSessionIsScopedToIt(t *testing.T) {
	s := queryStore(t)

	messageID, sessionID, _ := seedMessage(t, s, "scoped")
	created := mustCreateAttachment(t, s,
		attachment(messageID, sessionID, "teams/x/objects/scoped"))

	got, err := s.GetAttachmentInSession(t.Context(), db.GetAttachmentInSessionParams{
		ID: created.ID, SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("GetAttachmentInSession: %v", err)
	}
	if got.ID != created.ID || got.ObjectKey != created.ObjectKey {
		t.Errorf("row = %+v, want %+v", got, created)
	}

	_, otherSession, _ := seedMessage(t, s, "scoped-other")
	_, err = s.GetAttachmentInSession(t.Context(), db.GetAttachmentInSessionParams{
		ID: created.ID, SessionID: otherSession,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("reading it through another session returned %v, want ErrNoRows", err)
	}
}
