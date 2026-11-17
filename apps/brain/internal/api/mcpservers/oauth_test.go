package mcpservers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// oauthFakes is the sign-ins the fake store keeps, ended as the real query
// ends them.
type oauthFakes struct {
	mu        sync.Mutex
	conns     map[uuid.UUID]db.McpOauthConnection
	authSet   []db.SetMCPServerAuthParams
	ended     []uuid.UUID
	authErr   error
	listErr   error
	deleteErr error
}

func (s *fakeStore) GetOAuthConnection(_ context.Context, id uuid.UUID) (db.McpOauthConnection, error) {
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	c, ok := s.oauth.conns[id]
	if !ok {
		return c, pgx.ErrNoRows
	}
	return c, nil
}

func (s *fakeStore) ListOAuthConnectionsOf(_ context.Context, ids []uuid.UUID) ([]db.McpOauthConnection, error) {
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	if s.oauth.listErr != nil {
		return nil, s.oauth.listErr
	}
	var out []db.McpOauthConnection
	for _, id := range ids {
		if c, ok := s.oauth.conns[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *fakeStore) DeleteOAuthConnection(_ context.Context, id uuid.UUID) (int64, error) {
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	if s.oauth.deleteErr != nil {
		return 0, s.oauth.deleteErr
	}
	if _, ok := s.oauth.conns[id]; !ok {
		return 0, nil
	}
	delete(s.oauth.conns, id)
	s.oauth.ended = append(s.oauth.ended, id)
	return 1, nil
}

func (t *fakeTx) CreateMCPServer(ctx context.Context, arg db.CreateMCPServerParams) (db.McpServer, error) {
	return t.store.CreateMCPServer(ctx, arg)
}

func (t *fakeTx) UpdateMCPServer(ctx context.Context, arg db.UpdateMCPServerParams) (db.McpServer, error) {
	return t.store.UpdateMCPServer(ctx, arg)
}

// SetMCPServerAuth ends a sign-in made for another address, client or kind,
// as the query does.
func (t *fakeTx) SetMCPServerAuth(_ context.Context, arg db.SetMCPServerAuthParams) (db.McpServer, error) {
	s := t.store
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	s.oauth.authSet = append(s.oauth.authSet, arg)
	if s.oauth.authErr != nil {
		return db.McpServer{}, s.oauth.authErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.servers[arg.ID]
	if !ok || row.TeamID != arg.TeamID {
		return db.McpServer{}, pgx.ErrNoRows
	}
	if _, ok := s.oauth.conns[row.ID]; ok &&
		(arg.AuthKind != authOAuth || !same(row.Url, arg.Url) || !same(row.OauthClientID, arg.ClientID)) {
		delete(s.oauth.conns, row.ID)
		s.oauth.ended = append(s.oauth.ended, row.ID)
	}
	row.AuthKind, row.AuthSecretRef = arg.AuthKind, arg.AuthSecretRef
	row.OauthClientID, row.OauthClientSecretRef, row.OauthScopes = arg.ClientID, arg.ClientSecretRef, arg.Scopes
	s.servers[row.ID] = row
	return row, nil
}

func same(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

type fakeRevoker struct {
	mu    sync.Mutex
	asked []string // the token each revocation named, and its hint
	err   error
}

func (r *fakeRevoker) Revoke(_ context.Context, as discovery.AuthServer, _ discovery.Client, token, hint string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, as.RevokeURL+" "+token+" "+hint)
	return r.err
}

func (r *fakeRevoker) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.asked)
}

func strp(s string) *string { return &s }

// signedIn is a Gmail-like server with a sign-in made, and its tokens kept.
func signedIn(t *testing.T, teamID uuid.UUID) (*fakeStore, db.McpServer) {
	t.Helper()
	row := aURLServer(teamID, "gmail")
	row.AuthSecretRef, row.AuthKind, row.OauthClientID = nil, authOAuth, strp("client-1")
	s := newFakeStore(row)
	as, _ := json.Marshal(discovery.AuthServer{Issuer: "https://accounts.example", TokenURL: "https://accounts.example/token", RevokeURL: "https://accounts.example/revoke"})
	by := uuid.New()
	s.oauth.conns = map[uuid.UUID]db.McpOauthConnection{row.ID: {
		ServerID: row.ID, TeamID: teamID, Status: "needs_sign_in", Reason: strp("revoked"), ConnectedBy: &by,
		ConnectedAt: time.Now(), ScopesGranted: []string{"gmail.readonly"}, Auth: as,
	}}
	_ = s.vault.PutAll(t.Context(), secrets.Path(teamID, secrets.KindMCPOAuth, row.ID),
		map[string]string{discovery.OAuthAccessKey: "at-1", discovery.OAuthRefreshKey: "rt-1"})
	return s, row
}

func oauthRequest(row db.McpServer) map[string]any {
	return map[string]any{
		"name": row.Name, "url": *row.Url, "auth_kind": "oauth", "oauth_client_id": *row.OauthClientID,
		"oauth_scopes": []string{"gmail.readonly"},
	}
}

func serverURL(teamID, id uuid.UUID) string {
	return "/teams/" + teamID.String() + "/mcp-servers/" + id.String()
}

func TestAServerThatSignsInIsCreatedWithItsClient(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s := newFakeStore()
	secret := "teams/" + teamID.String() + "/mcp/google#secret"

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, "/teams/"+teamID.String()+"/mcp-servers",
		map[string]any{
			"name": "gmail", "url": "https://gmailmcp.example/mcp/v1", "auth_kind": "oauth",
			"oauth_client_id": "client-1", "oauth_client_secret_ref": secret, "oauth_scopes": []string{"gmail.readonly"},
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got server
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.AuthKind != authOAuth || got.OAuthClientID == nil || *got.OAuthClientID != "client-1" ||
		got.OAuthClientSecret == nil || *got.OAuthClientSecret != secret || !slices.Equal(got.OAuthScopes, []string{"gmail.readonly"}) ||
		got.Connection != nil {
		t.Errorf("server = %+v; want oauth, its client, no connection yet", got)
	}
	if s.committed != 1 {
		t.Errorf("committed %d times, want the server and its auth in one transaction", s.committed)
	}
}

func TestAServerIsStaticUnlessItSaysOtherwise(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s := newFakeStore()

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, "/teams/"+teamID.String()+"/mcp-servers",
		map[string]any{"name": "plain", "url": "https://mcp.example/mcp"})
	var got server
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusCreated || got.AuthKind != authStatic || len(s.oauth.authSet) != 0 {
		t.Errorf("status %d, kind %q, auth set %v; a static server is created as it always was", rec.Code, got.AuthKind, s.oauth.authSet)
	}
}

// Each of these is refused before anything is written.
func TestAServerThatSignsInIsRefusedWhatItCannotBe(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	other := "teams/" + uuid.NewString() + "/mcp/google#secret"
	cases := map[string]map[string]any{
		"an unknown kind":          {"name": "x", "url": "https://m.example/mcp", "auth_kind": "magic"},
		"a command server":         {"name": "x", "command": []string{"npx", "x"}, "auth_kind": "oauth", "oauth_client_id": "c"},
		"no client":                {"name": "x", "url": "https://m.example/mcp", "auth_kind": "oauth"},
		"a blank client":           {"name": "x", "url": "https://m.example/mcp", "auth_kind": "oauth", "oauth_client_id": "  "},
		"a static token as well":   {"name": "x", "url": "https://m.example/mcp", "auth_kind": "oauth", "oauth_client_id": "c", "auth_secret_ref": refIn(teamID, "x")},
		"another team's secret":    {"name": "x", "url": "https://m.example/mcp", "auth_kind": "oauth", "oauth_client_id": "c", "oauth_client_secret_ref": other},
		"a scope with a space":     {"name": "x", "url": "https://m.example/mcp", "auth_kind": "oauth", "oauth_client_id": "c", "oauth_scopes": []string{"a b"}},
		"an empty scope":           {"name": "x", "url": "https://m.example/mcp", "auth_kind": "oauth", "oauth_client_id": "c", "oauth_scopes": []string{""}},
		"too many scopes":          {"name": "x", "url": "https://m.example/mcp", "auth_kind": "oauth", "oauth_client_id": "c", "oauth_scopes": slices.Repeat([]string{"s"}, maxScopes+1)},
		"a static one with client": {"name": "x", "url": "https://m.example/mcp", "oauth_client_id": "c"},
		"a static one with scopes": {"name": "x", "url": "https://m.example/mcp", "oauth_scopes": []string{"s"}},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newFakeStore()
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, "/teams/"+teamID.String()+"/mcp-servers", body)
			if rec.Code != http.StatusBadRequest || s.wrote() {
				t.Errorf("status = %d, wrote %v; want 400 and nothing written", rec.Code, s.wrote())
			}
		})
	}
}

// A sign-in is for one address and one client: an edit to either ends it,
// and an edit to anything else keeps it.
func TestAnEditEndsASignInMadeForSomethingElse(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		edit func(map[string]any)
		ends bool
	}{
		"another url":    {func(b map[string]any) { b["url"] = "https://elsewhere.example/mcp" }, true},
		"another client": {func(b map[string]any) { b["oauth_client_id"] = "client-2" }, true},
		"a static token": {func(b map[string]any) {
			b["auth_kind"], b["oauth_client_id"], b["oauth_scopes"] = "static", nil, nil
		}, true},
		"other scopes": {func(b map[string]any) { b["oauth_scopes"] = []string{"gmail.modify"} }, false},
		"a new name":   {func(b map[string]any) { b["name"] = "mail" }, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			teamID := uuid.New()
			s, row := signedIn(t, teamID)
			body := oauthRequest(row)
			c.edit(body)
			for k, v := range body {
				if v == nil {
					delete(body, k)
				}
			}

			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPut, serverURL(teamID, row.ID), body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			_, kept := s.oauth.conns[row.ID]
			if kept == c.ends {
				t.Errorf("sign-in kept = %v; want ended %v", kept, c.ends)
			}
		})
	}
}

// The auth is set before the rest, so a server becoming static is static
// before it is handed a token: the other order breaks the constraint.
func TestAnEditSetsHowTheServerIsReachedFirst(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s, row := signedIn(t, teamID)
	s.oauth.authErr = errAny

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPut, serverURL(teamID, row.ID), oauthRequest(row))
	if rec.Code != http.StatusInternalServerError || len(s.updated) != 0 {
		t.Errorf("status = %d, updated %d; want the edit stopped at its auth", rec.Code, len(s.updated))
	}
}

func TestAServerCarriesItsSignInButNeverItsTokens(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s, row := signedIn(t, teamID)
	static := aURLServer(teamID, "plain")
	s.servers[static.ID] = static

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodGet, "/teams/"+teamID.String()+"/mcp-servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, leak := range []string{"at-1", "rt-1", "accounts.example"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the list carries %q", leak)
		}
	}
	var page struct{ Items []server }
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	for _, got := range page.Items {
		switch got.ID {
		case row.ID:
			if got.Connection == nil || got.Connection.Status != "needs_sign_in" || got.Connection.Reason == nil {
				t.Errorf("gmail's connection = %+v, want needs_sign_in with its reason", got.Connection)
			}
		case static.ID:
			if got.Connection != nil {
				t.Errorf("the static server = %+v", got)
			}
		}
	}

	one := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodGet, serverURL(teamID, row.ID), nil)
	var got server
	_ = json.Unmarshal(one.Body.Bytes(), &got)
	if got.Connection == nil {
		t.Error("one server read alone carries no connection")
	}
}

func TestAPageThatCannotReadItsSignInsSaysSo(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s, _ := signedIn(t, teamID)
	s.oauth.listErr = errAny

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodGet, "/teams/"+teamID.String()+"/mcp-servers", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 rather than servers shown as never signed in", rec.Code)
	}
}

// Disconnecting ends the sign-in here and then tells the authorization
// server, with the refresh token, since revoking it ends the whole grant.
func TestDisconnectingEndsTheSignInAndRevokesIt(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s, row := signedIn(t, teamID)

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodDelete, serverURL(teamID, row.ID)+"/oauth", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if _, ok := s.oauth.conns[row.ID]; ok {
		t.Error("the sign-in is still there")
	}
	if !slices.Equal(s.revoker.asked, []string{"https://accounts.example/revoke rt-1 refresh_token"}) {
		t.Errorf("revoked %v, want the refresh token", s.revoker.asked)
	}
}

func TestARevocationThatFailsStillDisconnects(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s, row := signedIn(t, teamID)
	s.revoker.err = errors.New("the authorization server is down")

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodDelete, serverURL(teamID, row.ID)+"/oauth", nil)
	if _, ok := s.oauth.conns[row.ID]; rec.Code != http.StatusNoContent || ok {
		t.Errorf("status = %d, kept %v; want disconnected regardless", rec.Code, ok)
	}
}

func TestDisconnectingWithNoSignInIsDisconnectedAlready(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	row := aURLServer(teamID, "plain")
	s := newFakeStore(row)

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodDelete, serverURL(teamID, row.ID)+"/oauth", nil)
	if rec.Code != http.StatusNoContent || s.revoker.count() != 0 {
		t.Errorf("status = %d, revoked %d; want 204 and nothing to tell", rec.Code, s.revoker.count())
	}
}

func TestADisconnectRefusedChangesNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]func(teamID uuid.UUID, s *fakeStore) (*fakeAuthz, uuid.UUID, int){
		"without tool:write": func(teamID uuid.UUID, _ *fakeStore) (*fakeAuthz, uuid.UUID, int) {
			return &fakeAuthz{allowed: false}, teamID, http.StatusForbidden
		},
		"another team's server": func(_ uuid.UUID, _ *fakeStore) (*fakeAuthz, uuid.UUID, int) {
			return &fakeAuthz{allowed: true}, uuid.New(), http.StatusNotFound
		},
		"the delete failing": func(teamID uuid.UUID, s *fakeStore) (*fakeAuthz, uuid.UUID, int) {
			s.oauth.deleteErr = errAny
			return &fakeAuthz{allowed: true}, teamID, http.StatusInternalServerError
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			teamID := uuid.New()
			s, row := signedIn(t, teamID)
			a, asTeam, want := setup(teamID, s)

			rec := call(t, s, a, memberOf(asTeam), http.MethodDelete, serverURL(asTeam, row.ID)+"/oauth", nil)
			if _, ok := s.oauth.conns[row.ID]; rec.Code != want || !ok || s.revoker.count() != 0 {
				t.Errorf("status = %d, kept %v, revoked %d; want %d, kept, none", rec.Code, ok, s.revoker.count(), want)
			}
		})
	}
}

// A server deleted revokes its sign-in, once it is deleted: one refused for
// being granted to an agent keeps the sign-in working.
func TestDeletingAServerRevokesItsSignInOnceDeleted(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s, row := signedIn(t, teamID)
	s.deleteErr = pgCode(codeRestrictViolation)

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodDelete, serverURL(teamID, row.ID), nil)
	if rec.Code != http.StatusConflict || s.revoker.count() != 0 {
		t.Fatalf("status = %d, revoked %d; a refused delete revokes nothing", rec.Code, s.revoker.count())
	}

	s.deleteErr = nil
	rec = call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodDelete, serverURL(teamID, row.ID), nil)
	if rec.Code != http.StatusNoContent || s.revoker.count() != 1 {
		t.Errorf("status = %d, revoked %d; want deleted and revoked", rec.Code, s.revoker.count())
	}
}

// With no refresh token the access token is revoked instead; with neither
// there is nothing to say.
func TestTheTokenRevokedIsTheBestOneKept(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s, row := signedIn(t, teamID)
	_ = s.vault.PutAll(t.Context(), secrets.Path(teamID, secrets.KindMCPOAuth, row.ID), map[string]string{discovery.OAuthAccessKey: "at-1"})

	call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodDelete, serverURL(teamID, row.ID)+"/oauth", nil)
	if !slices.Equal(s.revoker.asked, []string{"https://accounts.example/revoke at-1 access_token"}) {
		t.Errorf("revoked %v, want the access token", s.revoker.asked)
	}
}
