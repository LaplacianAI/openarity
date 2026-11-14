package sessions

import (
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

type session struct {
	ID          uuid.UUID  `json:"id"`
	ChannelID   *uuid.UUID `json:"channel_id"`
	ProviderRef string     `json:"provider_ref"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	// UserID is who a direct session is with; absent for anything else.
	UserID *uuid.UUID `json:"user_id,omitempty"`
	// Builder is a conversation the team's builder answers, not its hub.
	Builder bool `json:"builder"`

	StartedAt     time.Time `json:"started_at"`
	LastMessageAt time.Time `json:"last_message_at"`

	// FirstAsked is the input of the session's oldest root run, cut as a
	// preview is: what names a direct session, which has no reference.
	FirstAsked *string `json:"first_asked"`
}

type listedSession struct {
	session

	ChannelName     *string `json:"channel_name"`
	Participant     *string `json:"participant"`
	LastMessage     *string `json:"last_message"`
	LatestRunStatus *string `json:"latest_run_status"`
}

type message struct {
	ID         uuid.UUID `json:"id"`
	ExternalID string    `json:"external_id"`
	UserID     uuid.UUID `json:"user_id"`
	Text       string    `json:"text"`

	SentAt     *time.Time `json:"sent_at"`
	ReceivedAt time.Time  `json:"received_at"`
}

type attachment struct {
	ID        uuid.UUID `json:"id"`
	MessageID uuid.UUID `json:"message_id"`
	Filename  string    `json:"filename"`
	MediaType string    `json:"media_type"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type sessionCursor struct {
	LastMessageAt time.Time `json:"l"`
	ID            uuid.UUID `json:"i"`
}

type messageCursor struct {
	ReceivedAt time.Time `json:"r"`
	ID         uuid.UUID `json:"i"`
}

type attachmentCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

// sayRequest is what a person types on the dashboard, with the key a retry of
// the same send carries.
type sayRequest struct {
	Text string `json:"text"`
	Key  string `json:"key"`
	// Builder starts the conversation as the builder's. Only a start may say
	// it: who answers a conversation is decided when it begins.
	Builder bool `json:"builder"`
	// files arrive in a multipart body only, stored as they are read.
	files []gateway.Stored
}

// said is what became of it: the conversation, the message, and the run that
// answers it, which is the one already working when Steered.
type said struct {
	SessionID  uuid.UUID  `json:"session_id"`
	MessageID  uuid.UUID  `json:"message_id"`
	RunID      *uuid.UUID `json:"run_id,omitempty"`
	Steered    bool       `json:"steered,omitempty"`
	Unanswered string     `json:"unanswered,omitempty"`
}
