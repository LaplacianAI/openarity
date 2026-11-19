package channels

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ReconnectStore lists the channels to point at themselves again.
type ReconnectStore interface {
	ListLiveChannelsOf(ctx context.Context, providers []string) ([]db.Channel, error)
}

// Reconnect points every channel whose provider can be told where to post
// at its channel again, with what the provider asks for today: Telegram's
// setWebhook, which since approvals in the chat also asks for the taps on a
// prompt's keyboard. A channel connected before that would otherwise never
// hear a tap. Pointing a channel again is idempotent, so it runs whenever the
// API starts; a channel that cannot be pointed is logged and left as it was.
// It returns how many were pointed.
func Reconnect(
	ctx context.Context, logger *slog.Logger, s ReconnectStore, sec Secrets, p Providers, names []string,
	hookBase string, setup *http.Client,
) (int, error) {
	var connectors []string
	for _, n := range names {
		if _, ok := p.Connector(n); ok {
			connectors = append(connectors, n)
		}
	}
	// No public address makes a hook the provider refuses (Telegram posts
	// only to https), so it needs no check of its own here.
	if len(connectors) == 0 {
		return 0, nil
	}
	rows, err := s.ListLiveChannelsOf(ctx, connectors)
	if err != nil {
		return 0, err
	}
	h := &handler{logger: logger, setup: setup}
	hookBase = strings.TrimSuffix(hookBase, "/")
	pointed := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return pointed, err
		}
		if reconnectOne(ctx, h, sec, p, hookBase, row) {
			pointed++
		}
	}
	return pointed, nil
}

func reconnectOne(ctx context.Context, h *handler, sec Secrets, p Providers, hookBase string, row db.Channel) bool {
	log := h.logger.With("channel_id", row.ID, "provider", row.Provider)
	prov, ok := p.Get(row.Provider)
	c, can := p.Connector(row.Provider)
	if !ok || !can || len(prov.Routes()) == 0 {
		return false
	}
	keys := prov.Keys()
	if st, ok := p.Setup(row.Provider); ok {
		keys = append(keys, st.Supplied()...)
	}
	path := secrets.Path(row.TeamID, secrets.KindChannel, row.ID)
	creds := map[string]string{}
	for _, k := range keys {
		v, err := sec.Get(ctx, path, k)
		switch {
		case err == nil:
			creds[k] = v
		case !errors.Is(err, secrets.ErrNotFound):
			log.WarnContext(ctx, "channel not pointed again: its credentials could not be read", "error", err)
			return false
		}
	}
	hook := hookBase + "/hooks/" + row.Provider + "/" + row.ID.String() + prov.Routes()[0].Suffix
	out, err := c.ConnectRequest(hook, creds)
	if err != nil {
		log.InfoContext(ctx, "channel not pointed again", "reason", err)
		return false
	}
	status, body, err := h.ask(ctx, out)
	if err == nil {
		err = c.Answered(status, body)
	}
	if err != nil {
		log.WarnContext(ctx, "channel not pointed again", "error", err)
		return false
	}
	return true
}
