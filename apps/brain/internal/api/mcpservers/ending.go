package mcpservers

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ending is a sign-in about to end, read while it can be: once its row is
// deleted, which tokens it held and where to revoke them are gone with it.
type ending struct {
	server db.McpServer
	as     discovery.AuthServer
	client discovery.Client
	token  string
	hint   string
}

// endingOf reads what revoking a server's sign-in needs. Nothing to revoke,
// or nothing readable, is nil: ending a sign-in here never waits on telling
// the authorization server.
func (h *handler) endingOf(ctx context.Context, row db.McpServer) *ending {
	if row.AuthKind != authOAuth || h.ending.Revoker == nil || h.ending.Secrets == nil {
		return nil
	}
	c, err := h.store.GetOAuthConnection(ctx, row.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		h.logger.Warn("mcp oauth: the sign-in to revoke could not be read", "server_id", row.ID, "error", err)
		return nil
	}
	e := &ending{server: row}
	if err := json.Unmarshal(c.Auth, &e.as); err != nil {
		h.logger.Warn("mcp oauth: the sign-in's discovery is unreadable", "server_id", row.ID, "error", err)
		return nil
	}
	if e.client, err = discovery.ClientOf(ctx, h.ending.Secrets, h.store, row); err != nil {
		h.logger.Warn("mcp oauth: the client secret could not be read", "server_id", row.ID, "error", err)
		return nil
	}
	// The refresh token, when there is one: revoking it ends the grant, and
	// with it every access token made from it.
	path := secrets.Path(row.TeamID, secrets.KindMCPOAuth, row.ID)
	e.token, e.hint = h.secret(ctx, path, discovery.OAuthRefreshKey), "refresh_token"
	if e.token == "" {
		e.token, e.hint = h.secret(ctx, path, discovery.OAuthAccessKey), "access_token"
	}
	if e.token == "" {
		return nil
	}
	return e
}

func (h *handler) secret(ctx context.Context, path, key string) string {
	v, err := h.ending.Secrets.Get(ctx, path, key)
	if err != nil {
		return ""
	}
	return v
}

// revoke tells the authorization server, once the sign-in has ended here. A
// failure is logged, not answered: the tokens are deleted either way, and
// the grant expires there in its own time.
func (h *handler) revoke(ctx context.Context, e *ending) {
	if e == nil {
		return
	}
	if err := h.ending.Revoker.Revoke(context.WithoutCancel(ctx), e.as, e.client, e.token, e.hint); err != nil {
		h.logger.Warn("mcp oauth: revoking the sign-in failed", "server_id", e.server.ID, "issuer", e.as.Issuer, "error", err)
		return
	}
	h.logger.Info("mcp oauth: sign-in revoked", "server_id", e.server.ID, "issuer", e.as.Issuer)
}
