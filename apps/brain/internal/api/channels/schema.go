package channels

import (
	"time"

	"github.com/google/uuid"
)

type channel struct {
	ID       uuid.UUID `json:"id"`
	TeamID   uuid.UUID `json:"team_id"`
	Provider string    `json:"provider"`
	Name     string    `json:"name"`
}

type createRequest struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`

	SigningSecret *string `json:"signing_secret,omitempty"`
}

type created struct {
	channel
	SigningSecret string `json:"signing_secret,omitempty"`
}

type channelCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

type pendingSender struct {
	SenderRef  string    `json:"sender_ref"`
	SenderName string    `json:"sender_name"`
	SeenCount  int32     `json:"seen_count"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}

type channelSender struct {
	SenderRef string    `json:"sender_ref"`
	UserID    uuid.UUID `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

type approveRequest struct {
	SenderRef string    `json:"sender_ref"`
	UserID    uuid.UUID `json:"user_id"`
}

type pendingCursor struct {
	FirstSeen time.Time `json:"f"`
	Ref       string    `json:"r"`
}

type senderCursor struct {
	CreatedAt time.Time `json:"c"`
	Ref       string    `json:"r"`
}
