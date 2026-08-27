package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// seedMessage gives a test a message to hang attachments from, and the team
// that message belongs to. Every attachment needs both: one to reference, and
// one to check the read path's join answers correctly.
func seedMessage(t *testing.T, s *Store, name string) (messageID, teamID uuid.UUID) {
	t.Helper()

	ch, session, userID := seedInbox(t, s, name)
	insertMessage(t, s, message(ch, session, userID, "ext-"+name, "hello"))

	rows := listMessages(t, s, session.ID, 10)
	if len(rows) != 1 {
		t.Fatalf("seeded %d messages, want 1", len(rows))
	}
	return rows[0].ID, session.TeamID
}

func attachment(messageID uuid.UUID, key string) db.CreateAttachmentParams {
	return db.CreateAttachmentParams{
		MessageID:  messageID,
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

	messageID, _ := seedMessage(t, s, "round-trip")
	key := "teams/" + uuid.NewString() + "/objects/abc"

	row := mustCreateAttachment(t, s, attachment(messageID, key))

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

	messageID, teamID := seedMessage(t, s, "with-team")
	created := mustCreateAttachment(t, s, attachment(messageID, "teams/x/objects/abc"))

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

	created := mustCreateAttachment(t, s, attachment(messageID, "teams/x/objects/abc"))

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

	messageID, _ := seedMessage(t, s, "list")
	for _, key := range []string{"teams/x/objects/a", "teams/x/objects/b"} {
		mustCreateAttachment(t, s, attachment(messageID, key))
	}

	// A second message's attachment must not appear in the first's list.
	otherID, _ := seedMessage(t, s, "list-other")
	mustCreateAttachment(t, s, attachment(otherID, "teams/x/objects/c"))

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

	first, _ := seedMessage(t, s, "count-a")
	second, _ := seedMessage(t, s, "count-b")
	shared := "teams/x/objects/shared"

	mustCreateAttachment(t, s, attachment(first, shared))
	mustCreateAttachment(t, s, attachment(second, shared))
	mustCreateAttachment(t, s, attachment(first, "teams/x/objects/lonely"))

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

	messageID, _ := seedMessage(t, s, "cascade")
	created := mustCreateAttachment(t, s, attachment(messageID, "teams/x/objects/abc"))

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

	messageID, _ := seedMessage(t, s, "constraints")

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
		"a message that does not exist": {
			func(a *db.CreateAttachmentParams) { a.MessageID = uuid.New() },
			"attachments_message_id_fkey",
		},
	} {
		t.Run(name, func(t *testing.T) {
			arg := attachment(messageID, "teams/x/objects/"+uuid.NewString())
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

	messageID, _ := seedMessage(t, s, "no-filename")

	arg := attachment(messageID, "teams/x/objects/unnamed")
	arg.Filename = ""

	if _, err := s.CreateAttachment(t.Context(), arg); err != nil {
		t.Errorf("CreateAttachment with no filename: %v", err)
	}
}
