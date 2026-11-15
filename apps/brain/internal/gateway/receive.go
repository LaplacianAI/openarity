package gateway

import (
	"context"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Files keeps the files a message named, saying each one not kept.
type Files func(ctx context.Context, claimed []Attachment) (stored []Stored, missed []string)

// Receiver takes what a provider received once it has been read: each sender
// resolved (a stranger queued, and told so when the channel says to), the
// files kept, and the messages delivered. A webhook's handler and a socket's
// holder both hand theirs here, so a message is taken the same way however it
// arrived.
type Receiver struct {
	logger *slog.Logger
	store  Senders
	reg    Registry
	sink   Sink
	kick   Kicker
}

func NewReceiver(logger *slog.Logger, s Senders, reg Registry, sink Sink, kick Kicker) *Receiver {
	return &Receiver{logger: logger, store: s, reg: reg, sink: sink, kick: kick}
}

// Receive returns ErrTeamDeleted when the channel's team has gone, and any
// other error when a retry could help: nothing it wrote is lost by asking
// again, since every write here is idempotent.
func (r *Receiver) Receive(ctx context.Context, p Provider, ch db.Channel, msgs []Inbound, files Files) error {
	deliveries, err := r.resolve(ctx, p, ch, msgs)
	if err != nil {
		return err
	}
	if len(deliveries) == 0 {
		return nil
	}
	for i := range deliveries {
		deliveries[i].Files, deliveries[i].Missed = files(ctx, deliveries[i].Attachments)
	}
	return r.sink.Deliver(ctx, Channel{ID: ch.ID, TeamID: ch.TeamID}, deliveries)
}

func (r *Receiver) resolve(ctx context.Context, p Provider, ch db.Channel, msgs []Inbound) ([]Delivery, error) {
	_, notifies := r.reg.Notifier(p.Name())
	deliveries := make([]Delivery, 0, len(msgs))
	for _, in := range msgs {
		if err := in.Validate(); err != nil {
			r.logger.Warn("gateway: adapter produced an invalid message",
				"channel_id", ch.ID, "provider_message", in.ExternalID, "error", err)
			continue
		}

		userID, linked, err := ResolveSender(ctx, r.store, ch.ID, in)
		if err != nil {
			return nil, err
		}
		if !linked {
			if notifies {
				r.tellWaiting(ctx, ch, in)
			}
			continue
		}
		deliveries = append(deliveries, Delivery{Inbound: in, UserID: userID})
	}
	return deliveries, nil
}

// tellWaiting is best effort: a notice that could not be written is logged,
// and the stranger stays queued, which is what they came for.
func (r *Receiver) tellWaiting(ctx context.Context, ch db.Channel, in Inbound) {
	id, ok, err := TellWaiting(ctx, r.store, ch, in)
	switch {
	case err != nil:
		r.logger.Error("gateway: notice for a waiting sender", "channel_id", ch.ID, "error", err)
		return
	case !ok:
		return
	}
	if r.kick == nil {
		return
	}
	if err := r.kick.KickNotice(ctx, id); err != nil {
		r.logger.Warn("gateway: kick a notice; the sweep sends it", "notice", id, "error", err)
	}
}
