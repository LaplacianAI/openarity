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
	BuilderCanRun(ctx context.Context, teamID uuid.UUID) (bool, error)
	MarkMessageAsks(ctx context.Context, id uuid.UUID) error
	MarkMessageAnswer(ctx context.Context, id uuid.UUID) error
	ConsumeMessages(ctx context.Context, arg db.ConsumeMessagesParams) (int64, error)
	Superseding
	Approvals
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
	// Resume hands over a run that waited, as its next attempt.
	Resume(ctx context.Context, runID uuid.UUID, attempt int32) error
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
	// notices sends a notice queued about a message now; nil leaves it to
	// the reply sweep, a minute at most.
	notices func(ctx context.Context, messageID uuid.UUID) error
}

func NewInbox(s InboxStore, runs Enqueuer, logger *slog.Logger) *Inbox {
	return &Inbox{store: s, runs: runs, logger: logger}
}

// KickingNotices has each notice the inbox queues sent at once, rather than
// at the next reply sweep.
func (i *Inbox) KickingNotices(kick func(ctx context.Context, messageID uuid.UUID) error) *Inbox {
	i.notices = kick
	return i
}

// Delivered is what became of one message: the conversation it is in, and
// the run it started or was sent to, or why it got no answer. Message is
// uuid.Nil when nothing was stored: a message already stored under the same
// ExternalID, dropped as the retry it is, or an answer given with a control
// that names no request it may act on, refused (Unanswered says so). Only a
// channel's message carries a control, so the dashboard never sees the second.
type Delivered struct {
	Session    uuid.UUID
	Message    uuid.UUID
	Run        uuid.UUID
	Steered    uuid.UUID
	Unanswered string
}

// DeliverOne writes one message as Deliver does, and says what became of it,
// for a caller that answers the sender itself: the dashboard.
func (i *Inbox) DeliverOne(ctx context.Context, ch Channel, m Delivery) (Delivered, error) {
	var out outcome
	var d Delivered
	refused := false
	if err := i.store.InTx(ctx, func(q Queries) error {
		if _, err := q.LockLiveTeam(ctx, ch.TeamID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("lock team %s: %w", ch.TeamID, err)
			}
			refused = true
			return tombstone(ctx, q, ch.TeamID, []Delivery{m})
		}
		var err error
		d, out, err = deliverOne(ctx, q, ch, m, "dashboard")
		return err
	}); err != nil {
		return Delivered{}, err
	}
	if refused {
		return Delivered{}, ErrTeamDeleted
	}
	i.follow(ctx, ch, m, out)
	return d, nil
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
			_, out, err = deliverOne(ctx, q, ch, m, "channel")
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
// goes to the workers, a run it queued again goes back to them, and a message
// that asked and got no answer says why.
// Handing a run over before its row commits would let a worker claim a run it
// cannot find.
func (i *Inbox) follow(ctx context.Context, ch Channel, m Delivery, out outcome) {
	if out.notice != uuid.Nil && i.notices != nil {
		if err := i.notices(ctx, out.notice); err != nil {
			i.logger.WarnContext(ctx, "notice not kicked; the sweep will send it", "message", out.notice, "err", err)
		}
	}
	switch {
	case out.run != uuid.Nil:
		if err := i.runs.Enqueue(ctx, out.run); err != nil {
			i.logger.WarnContext(ctx, "run not enqueued; the sweep will start it",
				"run", out.run, "err", err)
		}
	case out.resumed != nil:
		if err := i.runs.Resume(ctx, out.resumed.ID, out.resumed.Attempt); err != nil {
			i.logger.WarnContext(ctx, "run not resumed; the sweep will start it",
				"run", out.resumed.ID, "err", err)
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

func deliverOne(ctx context.Context, q Queries, ch Channel, m Delivery, via string) (Delivered, outcome, error) {
	session, err := q.EnsureSession(ctx, db.EnsureSessionParams{
		TeamID:      ch.TeamID,
		ChannelID:   ch.ID,
		ProviderRef: m.Session.Ref,
		Kind:        string(m.Session.Kind),
		UserID:      participant(m),
		Builder:     m.Session.Builder,
	})
	if err != nil {
		return Delivered{}, outcome{}, fmt.Errorf("resolve session %q: %w", m.Session.Ref, err)
	}
	d := Delivered{Session: session.ID}

	// An answer given with a control is matched before it is stored: one
	// naming nothing it may act on (a poll vote changed after it decided) is
	// refused here, so it never reaches the conversation, the next run's
	// input or the session's reads. One that acts is kept as the record of
	// the action, marked so those reads leave it out too.
	var t tap
	if m.Answer != nil {
		if t, err = matchTap(ctx, q, ch.TeamID, session, m); err != nil {
			return Delivered{}, outcome{}, err
		}
		if !t.acts() {
			out := outcome{unanswered: "an answer to a request that is no longer waiting"}
			d.Unanswered = out.unanswered
			return d, out, nil
		}
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
		SenderName: CleanName(m.Author.DisplayName),
		Answer:     m.Answer != nil,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return d, outcome{}, nil
		}
		return Delivered{}, outcome{}, fmt.Errorf("store message %q: %w", m.ExternalID, err)
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
			return Delivered{}, outcome{}, fmt.Errorf("store attachment %q of %q: %w",
				f.ObjectKey, m.ExternalID, err)
		}
	}

	d.Message = messageID
	out, err := answer(ctx, q, ch, session, m, t, messageID, via)
	if err != nil {
		return Delivered{}, outcome{}, err
	}
	d.Run, d.Steered, d.Unanswered = out.run, out.steered, out.unanswered
	return d, out, nil
}

func participant(m Delivery) *uuid.UUID {
	if m.Session.Kind != SessionDirect {
		return nil
	}
	return &m.UserID
}
