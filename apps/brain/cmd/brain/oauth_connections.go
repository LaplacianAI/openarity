package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// oauthConnections is where discovery keeps a server's sign-in: the row in Postgres,
// the tokens in the secret store.
type oauthConnections struct {
	store  *store.Store
	writer secrets.Writer
}

func (s oauthConnections) Connection(ctx context.Context, server uuid.UUID) (db.McpOauthConnection, error) {
	c, err := s.store.GetOAuthConnection(ctx, server)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, discovery.ErrNotSignedIn
	}
	return c, err
}

// Renew holds the row for the whole of renew, the token request included, so
// a refresh token is spent once however many workers find it due. The tokens
// are written before the commit: the secret is what calls are made with, and
// a commit that fails after it leaves good tokens and a stale expiry, which
// costs one renewal more and nothing else.
func (s oauthConnections) Renew(ctx context.Context, server uuid.UUID, renew func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error)) error {
	return s.store.InTx(ctx, func(q *db.Queries) error {
		c, err := q.LockOAuthConnection(ctx, server)
		if errors.Is(err, pgx.ErrNoRows) {
			return discovery.ErrNotSignedIn
		}
		if err != nil {
			return err
		}
		t, err := renew(ctx, c)
		if err != nil || t == nil {
			return err
		}

		expires, at := "", (*time.Time)(nil)
		if !t.Expiry.IsZero() {
			expires, at = t.Expiry.UTC().Format(time.RFC3339), &t.Expiry
		}
		if err := s.writer.PutAll(ctx, secrets.Path(c.TeamID, secrets.KindMCPOAuth, server), map[string]string{
			discovery.OAuthAccessKey: t.Access, discovery.OAuthRefreshKey: t.Refresh, discovery.OAuthExpiryKey: expires,
		}); err != nil {
			return fmt.Errorf("storing the renewed tokens: %w", err)
		}
		var scopes []string
		if len(t.Scopes) > 0 {
			scopes = t.Scopes
		}
		return q.SetOAuthRenewed(ctx, db.SetOAuthRenewedParams{ServerID: server, AccessExpiresAt: at, ScopesGranted: scopes})
	})
}

func (s oauthConnections) MarkNeedsSignIn(ctx context.Context, server uuid.UUID, connectedAt time.Time, reason string) error {
	_, err := s.store.MarkOAuthNeedsSignIn(ctx, db.MarkOAuthNeedsSignInParams{
		ServerID: server, ConnectedAt: connectedAt, Reason: &reason,
	})
	return err
}
