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

// Teams is what a delivery needs of the channel's team: that it is still
// live, held until the message is written, and somewhere to record the
// objects fetched for a team that is not.
type Teams interface {
	LockLiveTeam(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error
}

type Queries interface {
	Sessions
	Messages
	AttachmentRows
	Teams
}

// ErrTeamDeleted is a delivery refused because the channel's team was
// deleted after the channel was read. The handler answers it as it answers a
// deleted channel.
var ErrTeamDeleted = errors.New("the channel's team is deleted")

type InboxStore interface {
	InTx(ctx context.Context, fn func(Queries) error) error
}

type Inbox struct {
	store InboxStore
}

func NewInbox(s InboxStore) *Inbox { return &Inbox{store: s} }

// Deliver writes each message in a transaction of its own, which starts by
// locking the channel's team. A team deleted since the channel was read
// refuses the message and every one after it, and the objects already
// fetched for them are tombstoned in that same transaction: their bytes are
// in the bucket, and no row will ever name them.
func (i *Inbox) Deliver(ctx context.Context, ch Channel, msgs []Delivery) error {
	for n, m := range msgs {
		refused := false
		if err := i.store.InTx(ctx, func(q Queries) error {
			if _, err := q.LockLiveTeam(ctx, ch.TeamID); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("lock team %s: %w", ch.TeamID, err)
				}
				refused = true
				return tombstone(ctx, q, ch.TeamID, msgs[n:])
			}
			return deliverOne(ctx, q, ch, m)
		}); err != nil {
			return err
		}
		if refused {
			return ErrTeamDeleted
		}
	}
	return nil
}

func tombstone(ctx context.Context, q Queries, teamID uuid.UUID, msgs []Delivery) error {
	var keys []string
	for _, m := range msgs {
		for _, f := range m.Files {
			keys = append(keys, f.ObjectKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}

	// No hold: nothing is about to name these keys, so the reaper may take
	// them on its next sweep.
	if err := q.ReserveObjects(ctx, db.ReserveObjectsParams{
		ObjectKeys: keys, TeamID: teamID, HoldSeconds: 0,
	}); err != nil {
		return fmt.Errorf("tombstone %d objects: %w", len(keys), err)
	}
	return nil
}

func deliverOne(ctx context.Context, q Queries, ch Channel, m Delivery) error {
	session, err := q.EnsureSession(ctx, db.EnsureSessionParams{
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

	messageID, err := q.InsertMessage(ctx, db.InsertMessageParams{
		ChannelID:  ch.ID,
		SessionID:  session.ID,
		UserID:     m.UserID,
		ExternalID: m.ExternalID,
		Text:       m.Text,
		SentAt:     sentAt,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("store message %q: %w", m.ExternalID, err)
	}

	for _, f := range m.Files {
		if _, err := q.CreateAttachment(ctx, db.CreateAttachmentParams{
			MessageID:  messageID,
			SessionID:  session.ID,
			ObjectKey:  f.ObjectKey,
			TeamID:     ch.TeamID,
			KeyVersion: 1,
			MediaType:  f.MediaType,
			SizeBytes:  f.SizeBytes,
			Filename:   f.Filename,
		}); err != nil {
			return fmt.Errorf("store attachment %q of %q: %w",
				f.ObjectKey, m.ExternalID, err)
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
