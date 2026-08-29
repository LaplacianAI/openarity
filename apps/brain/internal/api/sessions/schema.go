package sessions

import (
	"time"

	"github.com/google/uuid"
)

type session struct {
	ID          uuid.UUID  `json:"id"`
	ChannelID   *uuid.UUID `json:"channel_id"`
	ProviderRef string     `json:"provider_ref"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`

	StartedAt     time.Time `json:"started_at"`
	LastMessageAt time.Time `json:"last_message_at"`
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
