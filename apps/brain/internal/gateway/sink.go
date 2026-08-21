package gateway

import (
	"context"

	"github.com/google/uuid"
)

type Delivery struct {
	Inbound
	UserID uuid.UUID
}

type Sink interface {
	Deliver(ctx context.Context, channelID uuid.UUID, msgs []Delivery) error
}
