package gateway

import (
	"context"

	"github.com/google/uuid"
)

type Channel struct {
	ID     uuid.UUID
	TeamID uuid.UUID
}

type Delivery struct {
	Inbound
	UserID uuid.UUID
	Files  []Stored
}

type Sink interface {
	Deliver(ctx context.Context, ch Channel, msgs []Delivery) error
}
