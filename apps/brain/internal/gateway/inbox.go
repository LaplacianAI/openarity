package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type Messages interface {
	InsertMessage(ctx context.Context, arg db.InsertMessageParams) (uuid.UUID, error)
}

type Sessions interface {
	EnsureSession(ctx context.Context, arg db.EnsureSessionParams) (db.Session, error)
}

type AttachmentRows interface {
	CreateAttachment(ctx context.Context, arg db.CreateAttachmentParams) (db.Attachment, error)
}

type InboxStore interface {
	Sessions
	Messages
	AttachmentRows
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

		messageID, err := i.store.InsertMessage(ctx, db.InsertMessageParams{
			ChannelID:  ch.ID,
			SessionID:  session.ID,
			UserID:     m.UserID,
			ExternalID: m.ExternalID,
			Text:       m.Text,
			SentAt:     sentAt,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return fmt.Errorf("store message %q: %w", m.ExternalID, err)
		}

		for _, f := range m.Files {
			if _, err := i.store.CreateAttachment(ctx, db.CreateAttachmentParams{
				MessageID:  messageID,
				SessionID:  session.ID,
				ObjectKey:  f.ObjectKey,
				KeyVersion: 1,
				MediaType:  f.MediaType,
				SizeBytes:  f.SizeBytes,
				Filename:   f.Filename,
			}); err != nil {
				return fmt.Errorf("store attachment %q of %q: %w",
					f.ObjectKey, m.ExternalID, err)
			}
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
