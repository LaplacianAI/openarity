package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type Messages interface {
	InsertMessage(ctx context.Context, arg db.InsertMessageParams) (int64, error)
}

type Sessions interface {
	EnsureSession(ctx context.Context, arg db.EnsureSessionParams) (db.Session, error)
}

type InboxStore interface {
	Sessions
	Messages
}

type Inbox struct {
	store InboxStore
}

func NewInbox(s InboxStore) *Inbox { return &Inbox{store: s} }

func (i *Inbox) Deliver(ctx context.Context, ch Channel, msgs []Delivery) error {
	for _, m := range msgs {
		session, err := i.store.EnsureSession(ctx, db.EnsureSessionParams{
			TeamID:      ch.TeamID,
			ChannelID:   ch.ID,
			ProviderRef: m.Session.Ref,
			Kind:        string(m.Session.Kind),
			UserID:      participant(m),
		})
		if err != nil {
			return fmt.Errorf("resolve session %q: %w", m.Session.Ref, err)
		}

		var sentAt *time.Time
		if !m.SentAt.IsZero() {
			at := m.SentAt
			sentAt = &at
		}

		if _, err := i.store.InsertMessage(ctx, db.InsertMessageParams{
			ChannelID:  ch.ID,
			SessionID:  session.ID,
			UserID:     m.UserID,
			ExternalID: m.ExternalID,
			Text:       m.Text,
			SentAt:     sentAt,
		}); err != nil {
			return fmt.Errorf("store message %q: %w", m.ExternalID, err)
		}
	}
	return nil
}

func participant(m Delivery) *uuid.UUID {
	if m.Session.Kind != SessionDirect {
		return nil
	}
	return &m.UserID
}
