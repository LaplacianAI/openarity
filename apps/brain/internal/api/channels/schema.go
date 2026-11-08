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
	HookURL  string    `json:"hook_url,omitempty"`
	ReplyURL string    `json:"reply_url,omitempty"`
}

// listedChannel is a row of the channels list: the channel, and how recently
// it was used, counted by the session rule.
type listedChannel struct {
	channel
	LastMessageAt *time.Time `json:"last_message_at"`
	Sessions24h   int64      `json:"sessions_24h"`
}

type createRequest struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`

	SigningSecret *string `json:"signing_secret,omitempty"`
	ReplyURL      *string `json:"reply_url,omitempty"`
}

type updateRequest struct {
	ReplyURL nullableString `json:"reply_url"`
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

type teamPendingSender struct {
	pendingSender
	ChannelID       uuid.UUID `json:"channel_id"`
	ChannelName     string    `json:"channel_name"`
	ChannelProvider string    `json:"channel_provider"`
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

type teamPendingCursor struct {
	FirstSeen time.Time `json:"f"`
	Channel   uuid.UUID `json:"c"`
	Ref       string    `json:"r"`
}

type senderCursor struct {
	CreatedAt time.Time `json:"c"`
	Ref       string    `json:"r"`
}
