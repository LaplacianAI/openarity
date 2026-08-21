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

type Inbox struct {
	messages Messages
}

func NewInbox(m Messages) *Inbox { return &Inbox{messages: m} }

func (i *Inbox) Deliver(ctx context.Context, channelID uuid.UUID, msgs []Delivery) error {
	for _, m := range msgs {
		var sentAt *time.Time
		if !m.SentAt.IsZero() {
			at := m.SentAt
			sentAt = &at
		}

		if _, err := i.messages.InsertMessage(ctx, db.InsertMessageParams{
			ChannelID:       channelID,
			UserID:          m.UserID,
			ExternalID:      m.ExternalID,
			ConversationRef: m.Conversation.Ref,
			Text:            m.Text,
			SentAt:          sentAt,
		}); err != nil {
			return fmt.Errorf("store message %q: %w", m.ExternalID, err)
		}
	}
	return nil
}
