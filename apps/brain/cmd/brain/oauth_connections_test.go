package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type recordingWriter struct {
	mu   sync.Mutex
	puts []map[string]string
	fail error
}

func (w *recordingWriter) Put(context.Context, string, string, string) error { return nil }

func (w *recordingWriter) PutAll(_ context.Context, _ string, values map[string]string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		return w.fail
	}
	w.puts = append(w.puts, values)
	return nil
}

func (w *recordingWriter) Delete(context.Context, string) error { return nil }

func (w *recordingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.puts)
}

type signinWorld struct {
	s      *store.Store
	w      *recordingWriter
	si     oauthConnections
	server db.McpServer
	conn   db.McpOauthConnection
}

func newSigninWorld(t *testing.T) *signinWorld {
	t.Helper()
	s := registrySchema(t)
	team, err := s.CreateTeam(t.Context(), "oauth-connections")
	if err != nil {
		t.Fatal(err)
	}
	u, id := "https://mcp.example/mcp", "client-1"
	srv, err := s.CreateMCPServer(t.Context(), db.CreateMCPServerParams{TeamID: team.ID, Name: "gmail", Url: &u, Env: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if srv, err = s.SetMCPServerOAuth(t.Context(), db.SetMCPServerOAuthParams{ID: srv.ID, TeamID: team.ID, ClientID: &id}); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Minute)
	c, err := s.UpsertOAuthConnection(t.Context(), db.UpsertOAuthConnectionParams{
		ServerID: srv.ID, TeamID: team.ID, Issuer: "https://accounts.example", TokenEndpoint: "https://accounts.example/token",
		Resource: u, Auth: []byte(`{}`), ScopesGranted: []string{"gmail.readonly"}, AccessExpiresAt: &before,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := &recordingWriter{}
	return &signinWorld{s: s, w: w, si: oauthConnections{store: s, writer: w}, server: srv, conn: c}
}

func (w *signinWorld) row(t *testing.T) db.McpOauthConnection {
	t.Helper()
	c, err := w.s.GetOAuthConnection(t.Context(), w.server.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var renewed = &discovery.Tokens{Access: "at-2", Refresh: "rt-2", Expiry: time.Now().Add(time.Hour).Truncate(time.Second)}

func TestARenewalStoresTheTokensThenTheExpiry(t *testing.T) {
	w := newSigninWorld(t)
	err := w.si.Renew(t.Context(), w.server.ID, func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) {
		return renewed, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.w.count() != 1 || w.w.puts[0][discovery.OAuthAccessKey] != "at-2" || w.w.puts[0][discovery.OAuthRefreshKey] != "rt-2" {
		t.Errorf("stored %v, want the renewed tokens", w.w.puts)
	}
	c := w.row(t)
	if c.AccessExpiresAt == nil || !c.AccessExpiresAt.Equal(renewed.Expiry) {
		t.Errorf("expiry = %v, want %v", c.AccessExpiresAt, renewed.Expiry)
	}
	if len(c.ScopesGranted) != 1 || c.ScopesGranted[0] != "gmail.readonly" {
		t.Errorf("scopes = %v; a renewal naming none keeps what was granted", c.ScopesGranted)
	}
}

// Nothing renewed, or nothing stored, leaves the row as it was.
func TestARenewalThatFailsChangesNothing(t *testing.T) {
	cases := map[string]func(w *signinWorld) func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error){
		"the refresh refused": func(*signinWorld) func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) {
			return func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) {
				return nil, discovery.ErrSignInAgain
			}
		},
		"the tokens not stored": func(w *signinWorld) func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) {
			w.w.fail = errors.New("sealed")
			return func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) { return renewed, nil }
		},
	}
	for name, renew := range cases {
		t.Run(name, func(t *testing.T) {
			w := newSigninWorld(t)
			if err := w.si.Renew(t.Context(), w.server.ID, renew(w)); err == nil {
				t.Fatal("Renew succeeded")
			}
			if c := w.row(t); !c.AccessExpiresAt.Equal(*w.conn.AccessExpiresAt) || !c.UpdatedAt.Equal(w.conn.UpdatedAt) {
				t.Errorf("the row changed: %+v", c)
			}
		})
	}
}

// The row is held for the whole renewal, the token request included: a
// second waits until the first has stored what it got.
func TestRenewalsOfOneSignInAreHeldOneAtATime(t *testing.T) {
	w := newSigninWorld(t)
	inside, gate := make(chan struct{}), make(chan struct{})
	// Let go before the store closes, which waits for the held connection:
	// a failure here would otherwise hang rather than fail.
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	first := make(chan error, 1)
	go func() {
		first <- w.si.Renew(t.Context(), w.server.ID, func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) {
			close(inside)
			<-gate
			return renewed, nil
		})
	}()
	<-inside

	secondIn := make(chan int, 1)
	second := make(chan error, 1)
	go func() {
		second <- w.si.Renew(t.Context(), w.server.ID, func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) {
			secondIn <- w.w.count()
			return nil, nil
		})
	}()
	select {
	case <-secondIn:
		t.Fatal("a second renewal ran while the first held the sign-in")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if n := <-secondIn; n != 1 {
		t.Errorf("the second renewal saw %d stored, want the first's", n)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestAMarkEndsOnlyTheSignInItWasReadWith(t *testing.T) {
	w := newSigninWorld(t)
	if err := w.si.MarkNeedsSignIn(t.Context(), w.server.ID, w.conn.ConnectedAt.Add(-time.Second), "old"); err != nil {
		t.Fatal(err)
	}
	if c := w.row(t); c.Status != "connected" {
		t.Errorf("status = %s after an old sign-in's mark, want connected", c.Status)
	}
	if err := w.si.MarkNeedsSignIn(t.Context(), w.server.ID, w.conn.ConnectedAt, "revoked"); err != nil {
		t.Fatal(err)
	}
	if c := w.row(t); c.Status != "needs_sign_in" || c.Reason == nil || *c.Reason != "revoked" {
		t.Errorf("connection = %+v, want needs_sign_in, revoked", c)
	}
	// The first reason is the one a person reads: a later call's is kept off.
	if err := w.si.MarkNeedsSignIn(t.Context(), w.server.ID, w.conn.ConnectedAt, "later"); err != nil {
		t.Fatal(err)
	}
	if c := w.row(t); c.Reason == nil || *c.Reason != "revoked" {
		t.Errorf("reason = %v, want the first kept", c.Reason)
	}
}

func TestNoSignInIsNotSignedIn(t *testing.T) {
	w := newSigninWorld(t)
	other := uuid.New()
	if _, err := w.si.Connection(t.Context(), other); !errors.Is(err, discovery.ErrNotSignedIn) {
		t.Errorf("Connection = %v, want ErrNotSignedIn", err)
	}
	err := w.si.Renew(t.Context(), other, func(context.Context, db.McpOauthConnection) (*discovery.Tokens, error) {
		t.Error("renew ran with no sign-in")
		return nil, nil
	})
	if !errors.Is(err, discovery.ErrNotSignedIn) {
		t.Errorf("Renew = %v, want ErrNotSignedIn", err)
	}
}

var _ secrets.Writer = (*recordingWriter)(nil)

// The discoverer the roles build renews sign-ins: wired to the store, it sees
// a sign-in that has ended and does not call the server with the token it
// still holds.
func TestTheDiscovererTheRolesBuildKnowsASignInHasEnded(t *testing.T) {
	w := newSigninWorld(t)
	sec := static.New()
	writer, ok := sec.(secrets.Writer)
	if !ok {
		t.Fatal("the static store does not write")
	}
	if err := writer.PutAll(t.Context(), secrets.Path(w.server.TeamID, secrets.KindMCPOAuth, w.server.ID),
		map[string]string{discovery.OAuthAccessKey: "at-1"}); err != nil {
		t.Fatal(err)
	}
	if err := w.si.MarkNeedsSignIn(t.Context(), w.server.ID, w.conn.ConnectedAt, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := newDiscoverer(&config.Config{}, sec, w.s).Client(t.Context(), w.server); !errors.Is(err, discovery.ErrNeedsSignIn) {
		t.Errorf("Client = %v, want ErrNeedsSignIn", err)
	}
}
