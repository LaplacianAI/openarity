package gateway

import (
	"context"

	"github.com/google/uuid"
)

type Channel struct {
	ID     uuid.UUID
	TeamID uuid.UUID
	// ReplyURL is where a provider that posts replies to a URL sends them.
	// Empty is a channel that does not reply.
	ReplyURL string
}

type Delivery struct {
	Inbound
	UserID uuid.UUID
	Files  []Stored
	// Missed says, a sentence each, which files were sent and not kept, and
	// why, so the run can tell the person rather than the file vanishing.
	Missed []string
}

type Sink interface {
	Deliver(ctx context.Context, ch Channel, msgs []Delivery) error
}
