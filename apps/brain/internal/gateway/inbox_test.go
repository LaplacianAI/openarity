package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeMessages is the messages table as the Inbox sees it, including the one
// behaviour the Inbox depends on and cannot check: the unique index on
// (channel_id, external_id) absorbing a replay.
type fakeMessages struct {
	stored map[string]db.InsertMessageParams
	err    error

	calls []db.InsertMessageParams
}

func newFakeMessages() *fakeMessages {
	return &fakeMessages{stored: map[string]db.InsertMessageParams{}}
}

func (f *fakeMessages) InsertMessage(_ context.Context, arg db.InsertMessageParams) (int64, error) {
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
			ExternalID:   externalID,
			Author:       Author{Ref: "u-1", DisplayName: "Asha"},
			Conversation: Conversation{Ref: "c-1", Kind: ConversationDirect},
			Text:         text,
			SentAt:       time.Unix(1755412345, 0).UTC(),
		},
	}
}

func TestDeliverWritesTheMessage(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	channelID := uuid.New()
	msg := delivery("m-1", "what's our deploy status?")

	if err := NewInbox(m).Deliver(t.Context(), channelID, []Delivery{msg}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(m.calls) != 1 {
		t.Fatalf("made %d inserts, want 1", len(m.calls))
	}

	got := m.calls[0]
	switch {
	case got.ChannelID != channelID:
		t.Errorf("ChannelID = %s, want %s", got.ChannelID, channelID)
	case got.UserID != msg.UserID:
		t.Errorf("UserID = %s, want the resolved sender %s", got.UserID, msg.UserID)
	case got.ExternalID != "m-1":
		t.Errorf("ExternalID = %q, want m-1", got.ExternalID)
	case got.Text != "what's our deploy status?":
		t.Errorf("Text = %q", got.Text)
	}

	// The conversation and not the channel: two threads in one channel are two
	// conversations, and storing the channel would merge them.
	if got.ConversationRef != "c-1" {
		t.Errorf("ConversationRef = %q, want the conversation c-1", got.ConversationRef)
	}
}

// Every provider retries whenever a response is slow, so the same message
// arriving twice is the normal case rather than an edge one.
func TestDeliveringTheSameMessageTwiceLeavesOneRow(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	channelID := uuid.New()
	msg := delivery("m-1", "hi")

	for range 2 {
		if err := NewInbox(m).Deliver(t.Context(), channelID, []Delivery{msg}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}

	if len(m.stored) != 1 {
		t.Errorf("%d rows after two deliveries, want 1", len(m.stored))
	}
}

// A replay is success. Reporting it as an error would make the handler answer
// non-200, which is exactly what makes a provider retry — and every retry
// would then be another error, until the endpoint was disabled.
func TestAReplayIsNotAnError(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	channelID := uuid.New()
	msg := delivery("m-1", "hi")

	inbox := NewInbox(m)
	if err := inbox.Deliver(t.Context(), channelID, []Delivery{msg}); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := inbox.Deliver(t.Context(), channelID, []Delivery{msg}); err != nil {
		t.Errorf("a replay was reported as a failure: %v", err)
	}
}

// Slack's ts is unique within a channel, not globally, so two channels seeing
// the same provider-side id is legitimate and must be two rows.
func TestTheSameExternalIDInTwoChannelsIsTwoRows(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	first, second := uuid.New(), uuid.New()
	msg := delivery("m-1", "hi")

	inbox := NewInbox(m)
	for _, channelID := range []uuid.UUID{first, second} {
		if err := inbox.Deliver(t.Context(), channelID, []Delivery{msg}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}

	if len(m.stored) != 2 {
		t.Errorf("%d rows across two channels, want 2", len(m.stored))
	}
}

func TestDeliverWritesEveryMessageInABatch(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	channelID := uuid.New()

	msgs := []Delivery{delivery("m-1", "first"), delivery("m-2", "second"), delivery("m-3", "third")}
	if err := NewInbox(m).Deliver(t.Context(), channelID, msgs); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(m.stored) != 3 {
		t.Errorf("%d rows for a batch of 3, want 3", len(m.stored))
	}
}

// A batch with nothing in it is what a reaction or a join notice produces.
// It must not be an error, and it must not write anything.
func TestDeliveringNothingIsNotAnError(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()

	if err := NewInbox(m).Deliver(t.Context(), uuid.New(), nil); err != nil {
		t.Errorf("Deliver with no messages: %v", err)
	}
	if len(m.calls) != 0 {
		t.Errorf("an empty batch made %d inserts", len(m.calls))
	}
}

// A provider that gives no timestamp stores null rather than the zero time.
// Year 1 in a timestamp column is a value somebody eventually plots.
func TestAMessageWithNoTimestampStoresNull(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	msg := delivery("m-1", "hi")
	msg.SentAt = time.Time{}

	if err := NewInbox(m).Deliver(t.Context(), uuid.New(), []Delivery{msg}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if m.calls[0].SentAt != nil {
		t.Errorf("SentAt = %v, want null", *m.calls[0].SentAt)
	}
}

func TestAMessageWithATimestampKeepsIt(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	want := time.Unix(1755412345, 0).UTC()
	msg := delivery("m-1", "hi")
	msg.SentAt = want

	if err := NewInbox(m).Deliver(t.Context(), uuid.New(), []Delivery{msg}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if m.calls[0].SentAt == nil {
		t.Fatal("SentAt was dropped")
	}
	if !m.calls[0].SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", *m.calls[0].SentAt, want)
	}
}

// The pointer must not alias the loop variable, or every message in a batch
// ends up with the last one's timestamp.
func TestEachMessageKeepsItsOwnTimestamp(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	first := delivery("m-1", "first")
	first.SentAt = time.Unix(1000, 0).UTC()
	second := delivery("m-2", "second")
	second.SentAt = time.Unix(2000, 0).UTC()

	if err := NewInbox(m).Deliver(t.Context(), uuid.New(), []Delivery{first, second}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if m.calls[0].SentAt.Equal(*m.calls[1].SentAt) {
		t.Errorf("both messages stored %v — the pointer aliases the loop variable", *m.calls[0].SentAt)
	}
	if !m.calls[0].SentAt.Equal(first.SentAt) {
		t.Errorf("first stored %v, want %v", *m.calls[0].SentAt, first.SentAt)
	}
}

// A failed write must reach the handler, which answers 500 so the provider
// retries. Swallowing it would lose the message with nothing to say so.
func TestAFailedWriteIsReported(t *testing.T) {
	t.Parallel()

	m := newFakeMessages()
	m.err = errors.New("connection reset")

	err := NewInbox(m).Deliver(t.Context(), uuid.New(), []Delivery{delivery("m-1", "hi")})
	if err == nil {
		t.Fatal("a failed write was swallowed")
	}
	// The id is what somebody reading the log needs to find the message the
	// provider will send again.
	if !errors.Is(err, m.err) {
		t.Errorf("the underlying error was not wrapped: %v", err)
	}
}

// Inbox is the Sink the orchestrator replaces, so it has to satisfy the
// interface rather than merely have a method of the same shape.
func TestInboxIsASink(t *testing.T) {
	t.Parallel()

	var _ Sink = NewInbox(newFakeMessages())
}
