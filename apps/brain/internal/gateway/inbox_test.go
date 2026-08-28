package gateway

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// inboxStore is the two tables as the Inbox sees them, including the two
// behaviours it depends on and cannot check: the unique index on
// (channel_id, external_id) absorbing a replay, and EnsureSession returning
// the session that already exists for a ref.
type inboxStore struct {
	stored   map[string]db.InsertMessageParams
	ids      map[string]uuid.UUID
	sessions map[string]db.Session

	attachments   []db.CreateAttachmentParams
	attachmentErr error

	err        error
	sessionErr error

	calls        []db.InsertMessageParams
	sessionCalls []db.EnsureSessionParams
}

func newInboxStore() *inboxStore {
	return &inboxStore{
		stored:   map[string]db.InsertMessageParams{},
		ids:      map[string]uuid.UUID{},
		sessions: map[string]db.Session{},
	}
}

// InTx models the one thing Deliver relies on: nothing the function wrote
// survives if it returns an error. A fake that just called fn would make every
// partial-write test pass while the real store rolled back and the fake did
// not.
func (f *inboxStore) InTx(_ context.Context, fn func(Queries) error) error {
	stored := maps.Clone(f.stored)
	ids := maps.Clone(f.ids)
	sessions := maps.Clone(f.sessions)
	attachments := slices.Clone(f.attachments)

	if err := fn(f); err != nil {
		f.stored, f.ids, f.sessions, f.attachments = stored, ids, sessions, attachments
		return err
	}
	return nil
}

func (f *inboxStore) EnsureSession(_ context.Context, arg db.EnsureSessionParams) (db.Session, error) {
	f.sessionCalls = append(f.sessionCalls, arg)
	if f.sessionErr != nil {
		return db.Session{}, f.sessionErr
	}

	key := arg.ChannelID.String() + "/" + arg.ProviderRef
	if existing, seen := f.sessions[key]; seen {
		// ON CONFLICT DO UPDATE: the same row, touched.
		existing.LastMessageAt = time.Now()
		f.sessions[key] = existing
		return existing, nil
	}

	session := db.Session{
		ID: uuid.New(), TeamID: arg.TeamID, ChannelID: &arg.ChannelID,
		ProviderRef: &arg.ProviderRef, Kind: arg.Kind, Status: "open",
	}
	f.sessions[key] = session
	return session, nil
}

func (f *inboxStore) InsertMessage(_ context.Context, arg db.InsertMessageParams) (uuid.UUID, error) {
	f.calls = append(f.calls, arg)
	if f.err != nil {
		return uuid.Nil, f.err
	}

	// DO NOTHING returns no row, and pgx reports that as ErrNoRows rather than
	// as a zero id. A fake that returned uuid.Nil instead would let a caller
	// that never checks the error look like it works.
	key := arg.ChannelID.String() + "/" + arg.ExternalID
	if _, seen := f.stored[key]; seen {
		return uuid.Nil, pgx.ErrNoRows
	}

	id := uuid.New()
	f.stored[key] = arg
	f.ids[key] = id
	return id, nil
}

func (f *inboxStore) CreateAttachment(
	_ context.Context, arg db.CreateAttachmentParams,
) (db.Attachment, error) {
	// Record the write, not the attempt. A fake that appends first makes a
	// failed insert look like a stored row, and every assertion about what
	// was written silently counts it.
	if f.attachmentErr != nil {
		return db.Attachment{}, f.attachmentErr
	}
	f.attachments = append(f.attachments, arg)
	return db.Attachment{
		ID: uuid.New(), MessageID: arg.MessageID, SessionID: arg.SessionID,
		ObjectKey: arg.ObjectKey, KeyVersion: arg.KeyVersion,
		MediaType: arg.MediaType, SizeBytes: arg.SizeBytes, Filename: arg.Filename,
	}, nil
}

func delivery(externalID, text string) Delivery {
	return Delivery{
		UserID: uuid.New(),
		Inbound: Inbound{
			ExternalID: externalID,
			Author:     Author{Ref: "u-1", DisplayName: "Asha"},
			Session:    Session{Ref: "c-1", Kind: SessionDirect},
			Text:       text,
			SentAt:     time.Unix(1755412345, 0).UTC(),
		},
	}
}

func TestDeliverWritesTheMessage(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}
	msg := delivery("m-1", "what's our deploy status?")

	if err := NewInbox(store).Deliver(t.Context(), ch, []Delivery{msg}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(store.calls) != 1 {
		t.Fatalf("made %d inserts, want 1", len(store.calls))
	}

	got := store.calls[0]
	switch {
	case got.ChannelID != ch.ID:
		t.Errorf("ChannelID = %s, want %s", got.ChannelID, ch.ID)
	case got.UserID != msg.UserID:
		t.Errorf("UserID = %s, want the resolved sender %s", got.UserID, msg.UserID)
	case got.ExternalID != "m-1":
		t.Errorf("ExternalID = %q, want m-1", got.ExternalID)
	case got.Text != "what's our deploy status?":
		t.Errorf("Text = %q", got.Text)
	}

	// The message points at a session, and that session is the one the
	// adapter's ref named. Storing the channel instead would merge two threads
	// running in one room into a single conversation.
	if len(store.sessionCalls) != 1 {
		t.Fatalf("resolved %d sessions, want 1", len(store.sessionCalls))
	}
	if ref := store.sessionCalls[0].ProviderRef; ref != "c-1" {
		t.Errorf("session ref = %q, want c-1", ref)
	}
	if got.SessionID != store.sessions[ch.ID.String()+"/c-1"].ID {
		t.Error("the message was not stored against the session that was resolved")
	}
}

// Every provider retries whenever a response is slow, so the same message
// arriving twice is the normal case rather than an edge one.
func TestDeliveringTheSameMessageTwiceLeavesOneRow(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}
	msg := delivery("m-1", "hi")

	for range 2 {
		if err := NewInbox(store).Deliver(t.Context(), ch, []Delivery{msg}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}

	if len(store.stored) != 1 {
		t.Errorf("%d rows after two deliveries, want 1", len(store.stored))
	}
}

// A replay is success. Reporting it as an error would make the handler answer
// non-200, which is exactly what makes a provider retry — and every retry
// would then be another error, until the endpoint was disabled.
func TestAReplayIsNotAnError(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}
	msg := delivery("m-1", "hi")

	inbox := NewInbox(store)
	if err := inbox.Deliver(t.Context(), ch, []Delivery{msg}); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := inbox.Deliver(t.Context(), ch, []Delivery{msg}); err != nil {
		t.Errorf("a replay was reported as a failure: %v", err)
	}
}

// Slack's ts is unique within a channel, not globally, so two channels seeing
// the same provider-side id is legitimate and must be two rows.
func TestTheSameExternalIDInTwoChannelsIsTwoRows(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	first, second := uuid.New(), uuid.New()
	msg := delivery("m-1", "hi")

	inbox := NewInbox(store)
	for _, channelID := range []uuid.UUID{first, second} {
		if err := inbox.Deliver(t.Context(),
			Channel{ID: channelID, TeamID: uuid.New()}, []Delivery{msg}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}

	if len(store.stored) != 2 {
		t.Errorf("%d rows across two channels, want 2", len(store.stored))
	}
}

func TestDeliverWritesEveryMessageInABatch(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	msgs := []Delivery{delivery("m-1", "first"), delivery("m-2", "second"), delivery("m-3", "third")}
	if err := NewInbox(store).Deliver(t.Context(), ch, msgs); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(store.stored) != 3 {
		t.Errorf("%d rows for a batch of 3, want 3", len(store.stored))
	}
}

// A batch with nothing in it is what a reaction or a join notice produces.
// It must not be an error, and it must not write anything.
func TestDeliveringNothingIsNotAnError(t *testing.T) {
	t.Parallel()

	store := newInboxStore()

	if err := NewInbox(store).Deliver(t.Context(), Channel{ID: uuid.New(), TeamID: uuid.New()}, nil); err != nil {
		t.Errorf("Deliver with no messages: %v", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("an empty batch made %d inserts", len(store.calls))
	}
}

// A provider that gives no timestamp stores null rather than the zero time.
// Year 1 in a timestamp column is a value somebody eventually plots.
func TestAMessageWithNoTimestampStoresNull(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	msg := delivery("m-1", "hi")
	msg.SentAt = time.Time{}

	if err := NewInbox(store).Deliver(t.Context(), Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{msg}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if store.calls[0].SentAt != nil {
		t.Errorf("SentAt = %v, want null", *store.calls[0].SentAt)
	}
}

func TestAMessageWithATimestampKeepsIt(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	want := time.Unix(1755412345, 0).UTC()
	msg := delivery("m-1", "hi")
	msg.SentAt = want

	if err := NewInbox(store).Deliver(t.Context(), Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{msg}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if store.calls[0].SentAt == nil {
		t.Fatal("SentAt was dropped")
	}
	if !store.calls[0].SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", *store.calls[0].SentAt, want)
	}
}

// The pointer must not alias the loop variable, or every message in a batch
// ends up with the last one's timestamp.
func TestEachMessageKeepsItsOwnTimestamp(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	first := delivery("m-1", "first")
	first.SentAt = time.Unix(1000, 0).UTC()
	second := delivery("m-2", "second")
	second.SentAt = time.Unix(2000, 0).UTC()

	if err := NewInbox(store).Deliver(t.Context(), Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{first, second}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if store.calls[0].SentAt.Equal(*store.calls[1].SentAt) {
		t.Errorf("both messages stored %v — the pointer aliases the loop variable", *store.calls[0].SentAt)
	}
	if !store.calls[0].SentAt.Equal(first.SentAt) {
		t.Errorf("first stored %v, want %v", *store.calls[0].SentAt, first.SentAt)
	}
}

// A failed write must reach the handler, which answers 500 so the provider
// retries. Swallowing it would lose the message with nothing to say so.
func TestAFailedWriteIsReported(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	store.err = errors.New("connection reset")

	err := NewInbox(store).Deliver(t.Context(), Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{delivery("m-1", "hi")})
	if err == nil {
		t.Fatal("a failed write was swallowed")
	}
	// The id is what somebody reading the log needs to find the message the
	// provider will send again.
	if !errors.Is(err, store.err) {
		t.Errorf("the underlying error was not wrapped: %v", err)
	}
}

// Inbox is the Sink the orchestrator replaces, so it has to satisfy the
// interface rather than merely have a method of the same shape.
func TestInboxIsASink(t *testing.T) {
	t.Parallel()

	var _ Sink = NewInbox(newInboxStore())
}

// --- sessions ---

// The adapter decided what a conversation is; the inbox only records it. Two
// messages naming the same ref are one session, which is what stops every
// message becoming a conversation of its own.
func TestTwoMessagesInOneConversationShareASession(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	first, second := delivery("m-1", "hi"), delivery("m-2", "still here")
	if err := NewInbox(store).Deliver(t.Context(), ch, []Delivery{first, second}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(store.sessions) != 1 {
		t.Fatalf("%d sessions for one conversation, want 1", len(store.sessions))
	}
	if store.calls[0].SessionID != store.calls[1].SessionID {
		t.Error("two messages in one conversation went to different sessions")
	}
}

// And two refs are two sessions, even in the same channel — which is the whole
// reason a session is not the channel.
func TestTwoConversationsInOneChannelAreTwoSessions(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	first := delivery("m-1", "about the deploy")
	second := delivery("m-2", "unrelated")
	second.Session.Ref = "c-2"

	if err := NewInbox(store).Deliver(t.Context(), ch, []Delivery{first, second}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(store.sessions) != 2 {
		t.Errorf("%d sessions for two conversations, want 2", len(store.sessions))
	}
	if store.calls[0].SessionID == store.calls[1].SessionID {
		t.Error("two conversations were merged into one session")
	}
}

// The kind is the adapter's answer to how many people can speak, and it is
// what a visibility rule will eventually read. Dropping it here would make
// every session look like a group one.
func TestTheSessionCarriesItsKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []SessionKind{SessionDirect, SessionGroup, SessionThread} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			store := newInboxStore()
			msg := delivery("m-1", "hi")
			msg.Session.Kind = kind

			if err := NewInbox(store).Deliver(t.Context(),
				Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{msg}); err != nil {
				t.Fatalf("Deliver: %v", err)
			}

			if got := store.sessionCalls[0].Kind; got != string(kind) {
				t.Errorf("kind = %q, want %q", got, kind)
			}
		})
	}
}

// The session is written to the channel's team, not to whatever team happens
// to be handy. Getting this wrong puts a conversation in a team that cannot
// see the channel it arrived on.
func TestTheSessionBelongsToTheChannelsTeam(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	if err := NewInbox(store).Deliver(t.Context(), ch, []Delivery{delivery("m-1", "hi")}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	got := store.sessionCalls[0]
	if got.TeamID != ch.TeamID {
		t.Errorf("TeamID = %s, want %s", got.TeamID, ch.TeamID)
	}
	if got.ChannelID != ch.ID {
		t.Errorf("ChannelID = %s, want %s", got.ChannelID, ch.ID)
	}
}

// A session that cannot be resolved must not become a message with no
// conversation. The handler answers 500 and the provider sends it again.
func TestAFailedSessionStopsTheMessage(t *testing.T) {
	t.Parallel()

	store := newInboxStore()
	store.sessionErr = errors.New("connection reset")

	err := NewInbox(store).Deliver(t.Context(),
		Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{delivery("m-1", "hi")})
	if err == nil {
		t.Fatal("a failed session was swallowed")
	}
	if len(store.calls) != 0 {
		t.Error("the message was stored without a session")
	}
}

// --- who a session belongs to ---

// A direct session is private to one person, and the column the read queries
// filter on is set here. Nowhere else can set it: the API never creates a
// session, so this is the only writer.
func TestADirectSessionRecordsItsParticipant(t *testing.T) {
	store := newInboxStore()
	inbox := NewInbox(store)

	msg := Delivery{
		Inbound: Inbound{
			ExternalID: "m-1",
			Author:     Author{Ref: "U-ASHA"},
			Session:    Session{Ref: "D-ASHA", Kind: SessionDirect},
			Text:       "quick one, privately",
		},
		UserID: uuid.New(),
	}

	if err := inbox.Deliver(t.Context(), Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{msg}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(store.sessionCalls) != 1 {
		t.Fatalf("%d EnsureSession calls, want 1", len(store.sessionCalls))
	}

	got := store.sessionCalls[0].UserID
	if got == nil {
		t.Fatal("a direct session was stored with no participant, so nobody can read it")
	}
	if *got != msg.UserID {
		t.Errorf("UserID = %s, want the approved sender %s", *got, msg.UserID)
	}
}

// A group session names nobody on purpose: several people speak in it, so
// recording one of them would be a fact that is not true — and the database
// refuses it, which would turn every group message into a failed delivery.
func TestAGroupSessionRecordsNoParticipant(t *testing.T) {
	for _, kind := range []SessionKind{SessionGroup, SessionThread} {
		t.Run(string(kind), func(t *testing.T) {
			store := newInboxStore()
			inbox := NewInbox(store)

			msg := Delivery{
				Inbound: Inbound{
					ExternalID: "m-1",
					Author:     Author{Ref: "U-ASHA"},
					Session:    Session{Ref: "C-SUPPORT", Kind: kind},
					Text:       "the deploy finished",
				},
				UserID: uuid.New(),
			}

			if err := inbox.Deliver(t.Context(), Channel{ID: uuid.New(), TeamID: uuid.New()}, []Delivery{msg}); err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if got := store.sessionCalls[0].UserID; got != nil {
				t.Errorf("a %s session named %s as its participant", kind, *got)
			}
		})
	}
}

func stored(key, filename string) Stored {
	return Stored{
		ObjectKey: key,
		MediaType: "image/png",
		SizeBytes: 33,
		Filename:  filename,
	}
}

func TestAnAttachmentIsWrittenAgainstItsMessageAndSession(t *testing.T) {
	t.Parallel()

	f := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	d := delivery("m-1", "here is the label")
	d.Files = []Stored{stored("teams/x/objects/one", "label.png")}

	if err := NewInbox(f).Deliver(t.Context(), ch, []Delivery{d}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(f.attachments) != 1 {
		t.Fatalf("wrote %d attachment rows, want 1", len(f.attachments))
	}
	got := f.attachments[0]

	// The pair is a foreign key onto messages (id, session_id), so a row that
	// names the wrong one of either is refused by the database. Asserting both
	// here is what makes that refusal impossible to reach.
	wantMessage := f.ids[ch.ID.String()+"/m-1"]
	if got.MessageID != wantMessage {
		t.Errorf("MessageID = %s, want the id the insert returned, %s",
			got.MessageID, wantMessage)
	}
	wantSession := f.sessions[ch.ID.String()+"/c-1"].ID
	if got.SessionID != wantSession {
		t.Errorf("SessionID = %s, want %s", got.SessionID, wantSession)
	}

	if got.ObjectKey != "teams/x/objects/one" || got.Filename != "label.png" {
		t.Errorf("row = %+v", got)
	}
	if got.MediaType != "image/png" || got.SizeBytes != 33 {
		t.Errorf("the measured values did not survive: %+v", got)
	}
	if got.KeyVersion != 1 {
		t.Errorf("KeyVersion = %d, want 1 — a read has to know which key sealed it",
			got.KeyVersion)
	}
}

// The whole reason the replay skip exists. Without it a provider retrying a
// delivery stores the same photo again under a new object key, and the first
// one is orphaned in the bucket with no row naming it.
func TestAReplayWritesNoAttachments(t *testing.T) {
	t.Parallel()

	f := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	d := delivery("m-1", "here is the label")
	d.Files = []Stored{stored("teams/x/objects/one", "label.png")}

	inbox := NewInbox(f)
	if err := inbox.Deliver(t.Context(), ch, []Delivery{d}); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	// The same message again, carrying a second copy of the file as it would
	// if the fetch step ran twice.
	replay := delivery("m-1", "here is the label")
	replay.Files = []Stored{stored("teams/x/objects/two", "label.png")}

	if err := inbox.Deliver(t.Context(), ch, []Delivery{replay}); err != nil {
		t.Fatalf("replay: %v", err)
	}

	if len(f.attachments) != 1 {
		t.Fatalf("%d attachment rows after a replay, want 1: %+v",
			len(f.attachments), f.attachments)
	}
	if f.attachments[0].ObjectKey != "teams/x/objects/one" {
		t.Errorf("the replay's object won: %+v", f.attachments[0])
	}
}

func TestAMessageWithNoFilesWritesNoAttachments(t *testing.T) {
	t.Parallel()

	f := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	if err := NewInbox(f).Deliver(t.Context(), ch,
		[]Delivery{delivery("m-1", "just words")}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(f.attachments) != 0 {
		t.Errorf("wrote %+v", f.attachments)
	}
}

func TestEveryFileOnAMessageBecomesARow(t *testing.T) {
	t.Parallel()

	f := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	d := delivery("m-1", "three files")
	d.Files = []Stored{
		stored("teams/x/objects/one", "a.png"),
		stored("teams/x/objects/two", "b.png"),
		stored("teams/x/objects/three", "c.png"),
	}

	if err := NewInbox(f).Deliver(t.Context(), ch, []Delivery{d}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(f.attachments) != 3 {
		t.Fatalf("wrote %d rows, want 3", len(f.attachments))
	}
	for i, want := range []string{"a.png", "b.png", "c.png"} {
		if f.attachments[i].Filename != want {
			t.Errorf("row %d is %q, want %q", i, f.attachments[i].Filename, want)
		}
	}
}

// The object is already in the bucket by the time the row is written, so a
// failure here has to be a 500 the provider will retry. Answering 200 leaves
// bytes nothing names and a message nobody has.
func TestAFailedAttachmentWriteIsReported(t *testing.T) {
	t.Parallel()

	f := newInboxStore()
	f.attachmentErr = errors.New("constraint violated")
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	d := delivery("m-1", "here is the label")
	d.Files = []Stored{stored("teams/x/objects/one", "label.png")}

	err := NewInbox(f).Deliver(t.Context(), ch, []Delivery{d})
	if err == nil {
		t.Fatal("Deliver reported success after the attachment write failed")
	}
	if !strings.Contains(err.Error(), "teams/x/objects/one") {
		t.Errorf("err = %v, want it to name the object", err)
	}
}

// The message and its attachment rows are one unit. Without a transaction the
// message commits first, and a failure on the attachment leaves it there — so
// the provider's retry finds the external id already present, takes the replay
// path, and never writes the file. The message is permanently textless and the
// object is orphaned in the bucket.
//
// The replay skip is what makes a retry safe for a message. This is the window
// where it makes one unsafe for a file.
func TestARetryAfterAFailedAttachmentStillStoresIt(t *testing.T) {
	t.Parallel()

	f := newInboxStore()
	ch := Channel{ID: uuid.New(), TeamID: uuid.New()}

	d := delivery("m-1", "here is the label")
	d.Files = []Stored{stored("teams/x/objects/one", "label.png")}

	inbox := NewInbox(f)

	f.attachmentErr = errors.New("connection reset")
	if err := inbox.Deliver(t.Context(), ch, []Delivery{d}); err == nil {
		t.Fatal("Deliver reported success after the attachment write failed")
	}

	// The provider retries, as it must after a 500.
	f.attachmentErr = nil
	if err := inbox.Deliver(t.Context(), ch, []Delivery{d}); err != nil {
		t.Fatalf("retry: %v", err)
	}

	if len(f.stored) != 1 {
		t.Errorf("%d messages after the retry, want 1", len(f.stored))
	}
	written := 0
	for _, a := range f.attachments {
		if a.ObjectKey == "teams/x/objects/one" {
			written++
		}
	}
	if written != 1 {
		t.Fatalf("the attachment was written %d times across both attempts; "+
			"the retry has to complete what the first attempt could not", written)
	}
}
