package store

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type oauthWorld struct {
	s      *Store
	team   db.Team
	server db.McpServer
	user   uuid.UUID
}

func newOAuthWorld(t *testing.T) *oauthWorld {
	t.Helper()
	s := queryStore(t)
	team := mustCreate(t, s, "oauth-"+uuid.NewString()[:8])
	u := "https://gmailmcp.example/mcp/v1"
	srv := mustCreateServer(t, s, db.CreateMCPServerParams{TeamID: team.ID, Name: "gmail", Url: &u, Env: []byte(`{}`)})
	id, scopes := "client-1", []string{"gmail.readonly"}
	srv, err := s.SetMCPServerOAuth(t.Context(), db.SetMCPServerOAuthParams{ID: srv.ID, TeamID: team.ID, ClientID: &id, Scopes: scopes})
	if err != nil {
		t.Fatalf("SetMCPServerOAuth: %v", err)
	}
	return &oauthWorld{s: s, team: team, server: srv, user: mustCreateUser(t, s, "ann")}
}

func stateHash(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

func (w *oauthWorld) begin(t *testing.T, state string, lasts time.Duration) {
	t.Helper()
	if err := w.s.InsertOAuthPending(t.Context(), db.InsertOAuthPendingParams{
		StateHash: stateHash(state), TeamID: w.team.ID, ServerID: w.server.ID, StartedBy: w.user,
		Verifier: "v", RedirectUri: "https://dash.example/oauth/mcp/callback", Auth: []byte(`{}`), ExpiresAt: time.Now().Add(lasts),
	}); err != nil {
		t.Fatalf("InsertOAuthPending: %v", err)
	}
}

func (w *oauthWorld) connect(t *testing.T) {
	t.Helper()
	if _, err := w.s.UpsertOAuthConnection(t.Context(), db.UpsertOAuthConnectionParams{
		ServerID: w.server.ID, TeamID: w.team.ID, ConnectedBy: &w.user, Issuer: "https://accounts.example",
		TokenEndpoint: "https://accounts.example/token", Resource: "https://gmailmcp.example/mcp/v1", Auth: []byte(`{}`),
		ScopesGranted: []string{"gmail.readonly"},
	}); err != nil {
		t.Fatalf("UpsertOAuthConnection: %v", err)
	}
}

func TestASignInStartedServerHasNoStaticToken(t *testing.T) {
	w := newOAuthWorld(t)
	if w.server.AuthKind != "oauth" || w.server.AuthSecretRef != nil || *w.server.OauthClientID != "client-1" {
		t.Errorf("server = %+v; want oauth, its client, no static token", w.server)
	}
	ref := "teams/" + w.team.ID.String() + "/mcp/gmail#token"
	if _, err := w.s.pool.Exec(t.Context(), `UPDATE mcp_servers SET auth_secret_ref = $1 WHERE id = $2`, ref, w.server.ID); err == nil {
		t.Error("a server that signs in was given a static token as well")
	}
}

// A server reached with a token carries no OAuth client: the fields are a
// signing-in server's alone.
func TestAStaticServerCarriesNoOAuthClient(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "oauth-static")
	u := "https://mcp.example/mcp"
	srv := mustCreateServer(t, s, db.CreateMCPServerParams{TeamID: team.ID, Name: "plain", Url: &u, Env: []byte(`{}`)})
	if srv.AuthKind != "static" {
		t.Errorf("auth_kind = %q, want static by default", srv.AuthKind)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE mcp_servers SET oauth_client_id = 'c' WHERE id = $1`, srv.ID); err == nil {
		t.Error("a static server was given an OAuth client")
	}
}

// The state arrives from a browser with nothing else to prove who sent it,
// so a sign-in is taken once, and only while it lasts.
func TestASignInIsTakenOnceAndOnlyWhileItLasts(t *testing.T) {
	w := newOAuthWorld(t)
	w.begin(t, "state-1", 10*time.Minute)

	got, err := w.s.TakeOAuthPending(t.Context(), stateHash("state-1"))
	if err != nil || got.ServerID != w.server.ID || got.Verifier != "v" {
		t.Fatalf("TakeOAuthPending = %+v, %v", got, err)
	}
	if _, err := w.s.TakeOAuthPending(t.Context(), stateHash("state-1")); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("taken again: %v; want no rows", err)
	}

	w.begin(t, "state-late", -time.Second)
	if _, err := w.s.TakeOAuthPending(t.Context(), stateHash("state-late")); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("an expired sign-in was taken: %v", err)
	}
	if n, err := w.s.PruneOAuthPending(t.Context()); err != nil || n != 1 {
		t.Errorf("pruned %d, %v; want the expired one", n, err)
	}
	if n := count(t, w.s, `SELECT count(*) FROM mcp_oauth_pending`); n != 0 {
		t.Errorf("%d pending left", n)
	}
}

func TestOnlyAHashOfTheStateIsKept(t *testing.T) {
	w := newOAuthWorld(t)
	w.begin(t, "state-raw", 10*time.Minute)
	if n := count(t, w.s, `SELECT count(*) FROM mcp_oauth_pending WHERE position('state-raw'::bytea in state_hash) > 0`); n != 0 {
		t.Error("the state itself is stored")
	}
	if _, err := w.s.pool.Exec(t.Context(), `INSERT INTO mcp_oauth_pending
		(state_hash, team_id, server_id, started_by, verifier, redirect_uri, auth, expires_at)
		VALUES ('short', $1, $2, $3, 'v', 'r', '{}', now())`, w.team.ID, w.server.ID, w.user); err == nil {
		t.Error("a state that is not a sha256 was stored")
	}
}

func TestSigningInAgainReplacesTheConnection(t *testing.T) {
	w := newOAuthWorld(t)
	w.connect(t)
	if _, err := w.s.pool.Exec(t.Context(), `UPDATE mcp_oauth_connections SET status = 'needs_sign_in', reason = 'revoked' WHERE server_id = $1`, w.server.ID); err != nil {
		t.Fatal(err)
	}

	w.connect(t)

	c, err := w.s.GetOAuthConnection(t.Context(), w.server.ID)
	if err != nil || c.Status != "connected" || c.Reason != nil {
		t.Errorf("connection = %+v, %v; want connected again, no reason", c, err)
	}
}

// Tokens are a secret: a connection gone, whichever way, leaves its path for
// the sweep, built as secrets.Path builds it.
func TestAConnectionGoneLeavesItsTokensToDelete(t *testing.T) {
	want := func(w *oauthWorld) string { return secrets.Path(w.team.ID, secrets.KindMCPOAuth, w.server.ID) }
	cases := map[string]func(t *testing.T, w *oauthWorld){
		"disconnected": func(t *testing.T, w *oauthWorld) {
			if _, err := w.s.pool.Exec(t.Context(), `DELETE FROM mcp_oauth_connections WHERE server_id = $1`, w.server.ID); err != nil {
				t.Fatal(err)
			}
		},
		"its server deleted": func(t *testing.T, w *oauthWorld) {
			if err := w.s.DeleteMCPServer(t.Context(), w.server.ID); err != nil {
				t.Fatal(err)
			}
		},
		"its team deleted": func(t *testing.T, w *oauthWorld) {
			if _, err := w.s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, w.team.ID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, gone := range cases {
		t.Run(name, func(t *testing.T) {
			w := newOAuthWorld(t)
			w.connect(t)
			gone(t, w)
			if n := count(t, w.s, `SELECT count(*) FROM deleted_secrets WHERE path = $1`, want(w)); n != 1 {
				t.Errorf("no tombstone at %s", want(w))
			}
		})
	}
}

const beforeMCPOAuth = 20261117090000

func TestMCPOAuthRollsBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	team := mustCreate(t, s, "oauth-down")
	u := "https://mcp.example/mcp"
	srv := mustCreateServer(t, s, db.CreateMCPServerParams{TeamID: team.ID, Name: "kept", Url: &u, Env: []byte(`{}`)})
	id := "client-1"
	if _, err := s.SetMCPServerOAuth(t.Context(), db.SetMCPServerOAuthParams{ID: srv.ID, TeamID: team.ID, ClientID: &id}); err != nil {
		t.Fatal(err)
	}

	downTo(t, s, beforeMCPOAuth)
	if n := count(t, s, `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('mcp_oauth_connections', 'mcp_oauth_pending')`); n != 0 {
		t.Error("rolled back, the tables are still there")
	}
	if n := count(t, s, `SELECT count(*) FROM mcp_servers WHERE name = 'kept'`); n != 1 {
		t.Error("rolling back lost the server")
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
}
