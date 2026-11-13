package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// Runs is what answering a message needs: whether its sender may, the run
// already answering the conversation, and starting one.
type Runs interface {
	MemberMay(ctx context.Context, arg db.MemberMayParams) (bool, error)
	ActiveSessionRun(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error)
	AddSteer(ctx context.Context, arg db.AddSteerParams) error
	HubCanRun(ctx context.Context, teamID uuid.UUID) (bool, error)
	CreateChannelRun(ctx context.Context, arg db.CreateChannelRunParams) (db.Run, error)
}

type Queries interface {
	Sessions
	Messages
	AttachmentRows
	Teams
	Runs
}

// Enqueuer hands a run to the workers. A run it fails to hand over is still
// queued in Postgres, and the sweep starts it within a minute.
type Enqueuer interface {
	Enqueue(ctx context.Context, runID uuid.UUID) error
}

// ErrTeamDeleted is a delivery refused because the channel's team was
// deleted after the channel was read. The handler answers it as it answers a
// deleted channel.
var ErrTeamDeleted = errors.New("the channel's team is deleted")

type InboxStore interface {
	InTx(ctx context.Context, fn func(Queries) error) error
}

type Inbox struct {
	store  InboxStore
	runs   Enqueuer
	logger *slog.Logger
}

func NewInbox(s InboxStore, runs Enqueuer, logger *slog.Logger) *Inbox {
	return &Inbox{store: s, runs: runs, logger: logger}
}

// Deliver writes each message in a transaction of its own, which starts by
// locking the channel's team. A team deleted since the channel was read
// refuses the message and every one after it, and the objects already
// fetched for them are tombstoned in that same transaction: their bytes are
// in the bucket, and no row will ever name them.
func (i *Inbox) Deliver(ctx context.Context, ch Channel, msgs []Delivery) error {
	for n, m := range msgs {
		refused := false
		var out outcome
		if err := i.store.InTx(ctx, func(q Queries) error {
			if _, err := q.LockLiveTeam(ctx, ch.TeamID); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("lock team %s: %w", ch.TeamID, err)
				}
				refused = true
				return tombstone(ctx, q, ch.TeamID, msgs[n:])
			}
			var err error
			out, err = deliverOne(ctx, q, ch, m)
			return err
		}); err != nil {
			return err
		}
		if refused {
			return ErrTeamDeleted
		}
		i.follow(ctx, ch, m, out)
	}
	return nil
}

// follow acts on what a stored message started, once it is committed: a run
// goes to the workers, and a message that asked and got no answer says why.
// Handing a run over before its row commits would let a worker claim a run it
// cannot find.
func (i *Inbox) follow(ctx context.Context, ch Channel, m Delivery, out outcome) {
	switch {
	case out.run != uuid.Nil:
		if err := i.runs.Enqueue(ctx, out.run); err != nil {
			i.logger.WarnContext(ctx, "run not enqueued; the sweep will start it",
				"run", out.run, "err", err)
		}
	case out.unanswered != "":
		i.logger.InfoContext(ctx, "message not answered",
			"channel", ch.ID, "message", m.ExternalID, "why", out.unanswered)
	}
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

func deliverOne(ctx context.Context, q Queries, ch Channel, m Delivery) (outcome, error) {
	session, err := q.EnsureSession(ctx, db.EnsureSessionParams{
		TeamID:      ch.TeamID,
		ChannelID:   ch.ID,
		ProviderRef: m.Session.Ref,
		Kind:        string(m.Session.Kind),
		UserID:      participant(m),
	})
	if err != nil {
		return outcome{}, fmt.Errorf("resolve session %q: %w", m.Session.Ref, err)
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
			return outcome{}, nil
		}
		return outcome{}, fmt.Errorf("store message %q: %w", m.ExternalID, err)
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
			return outcome{}, fmt.Errorf("store attachment %q of %q: %w",
				f.ObjectKey, m.ExternalID, err)
		}
	}

	return answer(ctx, q, ch, session, m, messageID)
}

func participant(m Delivery) *uuid.UUID {
	if m.Session.Kind != SessionDirect {
		return nil
	}
	return &m.UserID
}
