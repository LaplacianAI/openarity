package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// inboxStore is the two tables as the Inbox sees them, including the two
// behaviours it depends on and cannot check: the unique index on
// (channel_id, external_id) absorbing a replay, and EnsureSession returning
// the session that already exists for a ref.
type inboxStore struct {
	stored   map[string]db.InsertMessageParams
	sessions map[string]db.Session

	err        error
	sessionErr error

	calls        []db.InsertMessageParams
	sessionCalls []db.EnsureSessionParams
}

func newInboxStore() *inboxStore {
	return &inboxStore{
		stored:   map[string]db.InsertMessageParams{},
		sessions: map[string]db.Session{},
	}
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

func (f *inboxStore) InsertMessage(_ context.Context, arg db.InsertMessageParams) (int64, error) {
	f.calls = append(f.calls, arg)
	if f.err != nil {
		return 0, f.err
	}

	key := arg.ChannelID.String() + "/" + arg.ExternalID
	if _, seen := f.stored[key]; seen {
		return 0, nil // ON CONFLICT DO NOTHING
	}
	f.stored[key] = arg
	return 1, nil
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
