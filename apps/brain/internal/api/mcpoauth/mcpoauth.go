// Package mcpoauth is signing in to an MCP server: a sign-in begun by a
// person on the dashboard, and the callback the authorization server sends
// their browser back to.
package mcpoauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	// CallbackPath is where an authorization server sends the browser back,
	// under PUBLIC_URL. It is the redirect URI an OAuth client registers.
	CallbackPath = "/oauth/mcp/callback"
	// pendingFor is how long a sign-in may take: a person reading a consent
	// screen, not a person who wandered off.
	pendingFor = 10 * time.Minute
	// scopesUnasked is how many scopes a server may offer before a sign-in
	// with none chosen is refused: Gmail offers twenty, full mailbox access
	// among them, and asking for all of them asks for everything.
	scopesUnasked = 5
)

type Store interface {
	GetMCPServer(ctx context.Context, id uuid.UUID) (db.McpServer, error)
	InsertOAuthPending(ctx context.Context, arg db.InsertOAuthPendingParams) error
	TakeOAuthPending(ctx context.Context, stateHash []byte) (db.McpOauthPending, error)
	PruneOAuthPending(ctx context.Context) (int64, error)
	UpsertOAuthConnection(ctx context.Context, arg db.UpsertOAuthConnectionParams) (db.McpOauthConnection, error)
}

type Discoverer interface {
	DiscoverAuth(ctx context.Context, serverURL string) (discovery.AuthServer, error)
	Exchange(ctx context.Context, as discovery.AuthServer, c discovery.Client, redirect, code, verifier string) (discovery.Tokens, error)
}

type Secrets interface {
	Get(ctx context.Context, path, key string) (string, error)
	PutAll(ctx context.Context, path string, values map[string]string) error
}

// Refresher discovers a server's tools and records them, as Discover on the
// API does.
type Refresher interface {
	Refresh(ctx context.Context, row db.McpServer) ([]discovery.Tool, error)
}

type flow struct {
	logger    *slog.Logger
	store     Store
	disc      Discoverer
	secrets   Secrets
	refresher Refresher
	publicURL string
}

// New is the two halves of a sign-in: the team's route that begins it, and
// the public callback that ends it. publicURL is the dashboard's origin, and
// with none no sign-in can begin.
func New(logger *slog.Logger, s Store, d Discoverer, sec Secrets, r Refresher, publicURL string) (start, callback *api.Router) {
	f := &flow{logger: logger, store: s, disc: d, secrets: sec, refresher: r, publicURL: strings.TrimSuffix(publicURL, "/")}

	start = api.NewRouter("/teams")
	start.Post("/{id}/mcp-servers/{serverID}/oauth/start", f.start)

	callback = api.NewPublicRouter("/oauth/mcp")
	callback.Get("/callback", f.callback)
	return start, callback
}

type startResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// start begins a sign-in. Every refusal is decided before anything is
// written, and the only thing written is the pending sign-in.
func (f *flow) start(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, f.logger)
	if !ok {
		return
	}
	row, ok := f.server(w, r, teamID)
	if !ok {
		return
	}
	switch {
	case row.AuthKind != "oauth":
		http.Error(w, "this server is reached with a token, not a sign-in", http.StatusConflict)
		return
	case row.Url == nil:
		http.Error(w, "a command server cannot be signed in to", http.StatusConflict)
		return
	case f.publicURL == "":
		http.Error(w, "this brain has no PUBLIC_URL, so a sign-in would have nowhere to come back to", http.StatusConflict)
		return
	case row.OauthClientID == nil:
		http.Error(w, "the server has no OAuth client: set its client id, and its secret if it has one", http.StatusConflict)
		return
	}

	as, err := f.disc.DiscoverAuth(r.Context(), *row.Url)
	if err != nil {
		f.logger.Warn("mcp oauth: discovery failed", "subject", u.Subject, "server_id", row.ID, "error", err)
		http.Error(w, "the server's sign-in could not be found: "+err.Error(), http.StatusBadGateway)
		return
	}
	if len(row.OauthScopes) == 0 && len(as.Scopes) > scopesUnasked {
		http.Error(w, fmt.Sprintf("the server offers %d scopes; set the ones this team needs on the server, rather than asking for all of them",
			len(as.Scopes)), http.StatusConflict)
		return
	}
	client, ok := f.client(w, r, row)
	if !ok {
		return
	}

	// Sign-ins abandoned on a consent screen, cleared as new ones begin:
	// expired, none can be taken, so this only keeps the table small.
	if _, err := f.store.PruneOAuthPending(r.Context()); err != nil {
		f.logger.Warn("mcp oauth: expired sign-ins not cleared", "error", err)
	}

	redirect := f.publicURL + CallbackPath
	begun, err := discovery.AuthorizeURL(as, client, redirect, row.OauthScopes)
	if err != nil {
		api.Fail(w, f.logger, u, "failed to begin the sign-in", err)
		return
	}
	raw, err := json.Marshal(as)
	if err != nil {
		api.Fail(w, f.logger, u, "failed to begin the sign-in", err)
		return
	}
	if err := f.store.InsertOAuthPending(r.Context(), db.InsertOAuthPendingParams{
		StateHash: hashOf(begun.State), TeamID: teamID, ServerID: row.ID, StartedBy: u.ID,
		Verifier: begun.Verifier, RedirectUri: redirect, Auth: raw, ExpiresAt: time.Now().Add(pendingFor),
	}); err != nil {
		api.Fail(w, f.logger, u, "failed to begin the sign-in", err)
		return
	}

	f.logger.Info("mcp oauth: sign-in begun", "subject", u.Subject, "server_id", row.ID, "issuer", as.Issuer)
	api.WriteJSON(w, f.logger, http.StatusOK, startResponse{AuthorizeURL: begun.URL})
}

func (f *flow) server(w http.ResponseWriter, r *http.Request, teamID uuid.UUID) (db.McpServer, bool) {
	id, err := uuid.Parse(r.PathValue("serverID"))
	if err != nil {
		http.Error(w, "server id must be a uuid", http.StatusBadRequest)
		return db.McpServer{}, false
	}
	row, err := f.store.GetMCPServer(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.TeamID != teamID) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.McpServer{}, false
	}
	if err != nil {
		api.Fail(w, f.logger, api.Caller(r), "failed to read the server", err)
		return db.McpServer{}, false
	}
	return row, true
}

func (f *flow) client(w http.ResponseWriter, r *http.Request, row db.McpServer) (discovery.Client, bool) {
	c, err := clientOf(r.Context(), f.secrets, row)
	if err != nil {
		f.logger.Warn("mcp oauth: the client secret could not be read", "server_id", row.ID, "error", err)
		http.Error(w, "the server's client secret could not be read; set it again", http.StatusConflict)
		return discovery.Client{}, false
	}
	return c, true
}

func clientOf(ctx context.Context, sec Secrets, row db.McpServer) (discovery.Client, error) {
	c := discovery.Client{}
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

func hashOf(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

// The outcomes a callback ends in, as the dashboard reads them off its URL.
const (
	outcomeConnected = "connected"
	outcomeNoTools   = "connected-no-tools"
	outcomeExpired   = "expired"
	outcomeDenied    = "denied"
	outcomeRefused   = "refused"
	outcomeFailed    = "failed"
)

// callback ends a sign-in. The request comes from a browser with no
// credential of its own, so the state is the only proof, taken once; the
// browser is only ever sent back to the dashboard, whatever the request
// says.
func (f *flow) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" || f.publicURL == "" {
		f.back(w, r, uuid.Nil, outcomeExpired)
		return
	}
	pending, err := f.store.TakeOAuthPending(r.Context(), hashOf(state))
	if errors.Is(err, pgx.ErrNoRows) {
		f.back(w, r, uuid.Nil, outcomeExpired)
		return
	}
	if err != nil {
		f.logger.Error("mcp oauth: reading the sign-in failed", "error", err)
		f.back(w, r, uuid.Nil, outcomeFailed)
		return
	}
	server := pending.ServerID

	if q.Get("error") != "" || q.Get("code") == "" {
		f.back(w, r, server, outcomeDenied)
		return
	}
	var as discovery.AuthServer
	if err := json.Unmarshal(pending.Auth, &as); err != nil {
		f.logger.Error("mcp oauth: the sign-in's discovery is unreadable", "server_id", server, "error", err)
		f.back(w, r, server, outcomeFailed)
		return
	}
	// RFC 9207: an authorization server that says it sends iss must have
	// sent its own, or the code may be another's, mixed in.
	if iss := q.Get("iss"); (as.IssResponse && iss == "") || (iss != "" && !sameIssuer(iss, as.Issuer)) {
		f.logger.Warn("mcp oauth: the callback named another issuer", "server_id", server, "issuer", as.Issuer)
		f.back(w, r, server, outcomeRefused)
		return
	}

	row, err := f.store.GetMCPServer(r.Context(), server)
	if err != nil || row.TeamID != pending.TeamID || row.AuthKind != "oauth" {
		f.back(w, r, server, outcomeRefused)
		return
	}
	client, err := clientOf(r.Context(), f.secrets, row)
	if err != nil {
		f.logger.Warn("mcp oauth: the client secret could not be read", "server_id", server, "error", err)
		f.back(w, r, server, outcomeFailed)
		return
	}

	tokens, err := f.disc.Exchange(r.Context(), as, client, pending.RedirectUri, q.Get("code"), pending.Verifier)
	if errors.Is(err, discovery.ErrSignInAgain) {
		f.back(w, r, server, outcomeRefused)
		return
	}
	if err != nil {
		f.logger.Warn("mcp oauth: the code could not be exchanged", "server_id", server, "error", err)
		f.back(w, r, server, outcomeFailed)
		return
	}

	if err := f.keep(r.Context(), row, pending, as, tokens); err != nil {
		f.logger.Error("mcp oauth: the sign-in could not be kept", "server_id", server, "error", err)
		f.back(w, r, server, outcomeFailed)
		return
	}
	f.logger.Info("mcp oauth: signed in", "server_id", server, "started_by", pending.StartedBy, "issuer", as.Issuer)

	// The tools, with the new token. A failure is the server's, recorded
	// on it as any discovery's is, and the sign-in stands.
	if _, err := f.refresher.Refresh(r.Context(), row); err != nil {
		f.back(w, r, server, outcomeNoTools)
		return
	}
	f.back(w, r, server, outcomeConnected)
}

// keep stores the tokens, then the connection: a connection naming tokens
// that were never written would be a sign-in that fails on its first call.
func (f *flow) keep(ctx context.Context, row db.McpServer, p db.McpOauthPending, as discovery.AuthServer, t discovery.Tokens) error {
	expires := ""
	var at *time.Time
	if !t.Expiry.IsZero() {
		expires = t.Expiry.UTC().Format(time.RFC3339)
		at = &t.Expiry
	}
	if err := f.secrets.PutAll(ctx, secrets.Path(row.TeamID, secrets.KindMCPOAuth, row.ID), map[string]string{
		discovery.OAuthAccessKey: t.Access, discovery.OAuthRefreshKey: t.Refresh, discovery.OAuthExpiryKey: expires,
	}); err != nil {
		return fmt.Errorf("storing the tokens: %w", err)
	}
	var revoke *string
	if as.RevokeURL != "" {
		revoke = &as.RevokeURL
	}
	startedBy := p.StartedBy
	if _, err := f.store.UpsertOAuthConnection(ctx, db.UpsertOAuthConnectionParams{
		ServerID: row.ID, TeamID: row.TeamID, ConnectedBy: &startedBy, Issuer: as.Issuer,
		TokenEndpoint: as.TokenURL, RevocationEndpoint: revoke, Resource: as.Resource, Auth: p.Auth,
		ScopesGranted: t.Scopes, AccessExpiresAt: at,
	}); err != nil {
		return fmt.Errorf("recording the connection: %w", err)
	}
	return nil
}

// back sends the browser to the dashboard's servers screen with the
// outcome. Always to PUBLIC_URL, never to anything the request named.
func (f *flow) back(w http.ResponseWriter, r *http.Request, server uuid.UUID, outcome string) {
	q := url.Values{"oauth": {outcome}}
	if server != uuid.Nil {
		q.Set("server", server.String())
	}
	http.Redirect(w, r, f.publicURL+"/ui/servers?"+q.Encode(), http.StatusSeeOther)
}

func sameIssuer(a, b string) bool { return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") }
