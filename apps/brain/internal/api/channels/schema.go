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
