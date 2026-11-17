package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ErrNeedsSignIn is a sign-in that stopped working: revoked, expired past
// refreshing, or refused by the server. Someone connects it again.
var ErrNeedsSignIn = errors.New("the server needs signing in again: reconnect it on the dashboard")

// refreshBefore is how long before it expires an access token is renewed, so
// a call that is slow to start does not arrive with one just gone.
const refreshBefore = 2 * time.Minute

// Connections is where a server's sign-in is kept. Renew holds the sign-in
// while renew runs and stores the tokens renew returns before letting go;
// renew returning none stores nothing.
type Connections interface {
	Connection(ctx context.Context, server uuid.UUID) (db.McpOauthConnection, error)
	Renew(ctx context.Context, server uuid.UUID, renew func(ctx context.Context, c db.McpOauthConnection) (*Tokens, error)) error
	MarkNeedsSignIn(ctx context.Context, server uuid.UUID, connectedAt time.Time, reason string) error
}

// SignsInWith gives the discoverer where sign-ins are kept, so a server that
// signs in is called with a token it renews. Without it, such a server is
// called with the access token it was signed in with, until that expires.
func (d *Discoverer) SignsInWith(s Connections) *Discoverer {
	d.connections = s
	return d
}

// ClientOf is the OAuth client a server signs in as: its id, and its secret
// when it is a confidential one.
func ClientOf(ctx context.Context, sec Secrets, row db.McpServer) (Client, error) {
	c := Client{}
	if row.OauthClientID != nil {
		c.ID = *row.OauthClientID
	}
	if row.OauthClientSecretRef == nil {
		return c, nil
	}
	ref := *row.OauthClientSecretRef
	i := strings.LastIndexByte(ref, '#')
	if i < 0 {
		return c, errors.New("the client secret reference is not path#key")
	}
	v, err := sec.Get(ctx, ref[:i], ref[i+1:])
	if err != nil {
		return c, err
	}
	c.Secret = v
	return c, nil
}

// tokens is one server's access token for one client: read once, kept while
// it lasts, renewed under the sign-in's lock when it does not.
type tokens struct {
	d *Discoverer
	s db.McpServer

	mu          sync.Mutex
	access      string
	expiry      time.Time
	connectedAt time.Time
}

func lasts(expiry time.Time) bool {
	return expiry.IsZero() || time.Until(expiry) > refreshBefore
}

// get is a token to call with. stale is one the server refused, which is
// never handed out again: past it the token is renewed.
func (t *tokens) get(ctx context.Context, stale string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.access != "" && t.access != stale && lasts(t.expiry) {
		return t.access, nil
	}

	c, err := t.d.connections.Connection(ctx, t.s.ID)
	if err != nil {
		return "", err
	}
	if c.Status != "connected" {
		return "", ErrNeedsSignIn
	}
	t.connectedAt = c.ConnectedAt
	if access, expiry, _, err := t.stored(ctx); err != nil {
		return "", err
	} else if access != "" && access != stale && lasts(expiry) {
		t.access, t.expiry = access, expiry
		return access, nil
	}

	err = t.d.connections.Renew(ctx, t.s.ID, func(ctx context.Context, c db.McpOauthConnection) (*Tokens, error) {
		return t.renew(ctx, c, stale)
	})
	if errors.Is(err, ErrSignInAgain) || errors.Is(err, errNoRefresh) {
		t.mark(ctx, err.Error())
		return "", ErrNeedsSignIn
	}
	if err != nil {
		return "", err
	}
	return t.access, nil
}

var errNoRefresh = errors.New("the sign-in has no refresh token, and its access token has expired")

// renew runs holding the sign-in. Another worker may have renewed it while
// this one waited, and then what it stored is used rather than spending the
// refresh token again.
func (t *tokens) renew(ctx context.Context, c db.McpOauthConnection, stale string) (*Tokens, error) {
	if c.Status != "connected" {
		return nil, ErrNeedsSignIn
	}
	t.connectedAt = c.ConnectedAt
	access, expiry, refresh, err := t.stored(ctx)
	if err != nil {
		return nil, err
	}
	if access != "" && access != stale && lasts(expiry) {
		t.access, t.expiry = access, expiry
		return nil, nil
	}
	if refresh == "" {
		return nil, errNoRefresh
	}

	var as AuthServer
	if err := json.Unmarshal(c.Auth, &as); err != nil {
		return nil, fmt.Errorf("reading the sign-in's discovery: %w", err)
	}
	client, err := ClientOf(ctx, t.d.secrets, t.s)
	if err != nil {
		return nil, fmt.Errorf("reading the server's client secret: %w", err)
	}
	renewed, err := t.d.Refresh(ctx, as, client, refresh)
	if err != nil {
		return nil, err
	}
	// A server that does not rotate refresh tokens sends none back: the one
	// it has stays good.
	if renewed.Refresh == "" {
		renewed.Refresh = refresh
	}
	t.access, t.expiry = renewed.Access, renewed.Expiry
	return &renewed, nil
}

func (t *tokens) stored(ctx context.Context) (access string, expiry time.Time, refresh string, err error) {
	path := secrets.Path(t.s.TeamID, secrets.KindMCPOAuth, t.s.ID)
	if access, err = t.d.secrets.Get(ctx, path, OAuthAccessKey); errors.Is(err, secrets.ErrNotFound) {
		return "", time.Time{}, "", ErrNotSignedIn
	} else if err != nil {
		return "", time.Time{}, "", fmt.Errorf("reading the server's sign-in: %w", err)
	}
	refresh, _ = t.d.secrets.Get(ctx, path, OAuthRefreshKey)
	if at, _ := t.d.secrets.Get(ctx, path, OAuthExpiryKey); at != "" {
		if expiry, err = time.Parse(time.RFC3339, at); err != nil {
			// An expiry that cannot be read is one already past: renewing
			// costs a request, a dead token costs the call.
			expiry = time.Unix(0, 0)
		}
	}
	return access, expiry, refresh, nil
}

// mark ends the sign-in this source read. A failure to record it is not the
// call's: the call fails either way, and the next one finds out again.
func (t *tokens) mark(ctx context.Context, reason string) {
	if t.connectedAt.IsZero() {
		return
	}
	_ = t.d.connections.MarkNeedsSignIn(context.WithoutCancel(ctx), t.s.ID, t.connectedAt, reason)
	t.access = ""
}

func (t *tokens) markAs(ctx context.Context, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.mark(ctx, reason)
}

// renewing sends each request with the server's current token. A 401 is
// tried once more with a renewed token, since a token can be revoked before
// it expires; a second is the sign-in ended. A 403 asking for more scope is
// a sign-in that must be made again with them.
type renewing struct {
	t    *tokens
	next http.RoundTripper
}

func (rt renewing) RoundTrip(r *http.Request) (*http.Response, error) {
	token, err := rt.t.get(r.Context(), "")
	if err != nil {
		closeBody(r)
		return nil, err
	}
	again := r
	if r.Body != nil && r.Body != http.NoBody {
		if r.GetBody == nil {
			again = nil
		} else if again, err = withBody(r); err != nil {
			closeBody(r)
			return nil, err
		}
	}

	resp, err := bearer{token: token, next: rt.next}.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden && insufficientScope(resp):
		discard(resp)
		reason := "the server needs more scopes than were granted"
		if s := scopeOf(challengesOf(resp)); s != "" {
			reason = "the server needs the scopes " + s
		}
		rt.t.markAs(r.Context(), reason)
		return nil, fmt.Errorf("%w (%s)", ErrNeedsSignIn, reason)
	case resp.StatusCode != http.StatusUnauthorized || again == nil:
		return resp, nil
	}
	discard(resp)

	if token, err = rt.t.get(r.Context(), token); err != nil {
		closeBody(again)
		return nil, err
	}
	resp, err = bearer{token: token, next: rt.next}.RoundTrip(again)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		discard(resp)
		rt.t.markAs(r.Context(), "the server refused a renewed token")
		return nil, ErrNeedsSignIn
	}
	return resp, nil
}

func challengesOf(resp *http.Response) []oauthex.Challenge {
	c, _ := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	return c
}

func insufficientScope(resp *http.Response) bool {
	return paramOf(challengesOf(resp), "error") == "insufficient_scope"
}

func withBody(r *http.Request) (*http.Request, error) {
	body, err := r.GetBody()
	if err != nil {
		return nil, fmt.Errorf("rereading the request: %w", err)
	}
	again := r.Clone(r.Context())
	again.Body = body
	return again, nil
}

func closeBody(r *http.Request) {
	if r != nil && r.Body != nil {
		_ = r.Body.Close()
	}
}

func discard(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}
