package discovery

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// renewWorld is an MCP server that signs in and its authorization server, on
// one TLS server: the token endpoint rotates refresh tokens and refuses one
// spent before, as a server that rotates them does; the MCP endpoint takes
// only the latest access token.
type renewWorld struct {
	*httptest.Server

	mu        sync.Mutex
	access    string // the one the MCP endpoint takes
	refresh   string // the one the token endpoint takes
	issued    int
	refreshes int
	asked     int    // every request to the token endpoint, refused or not
	revoked   bool   // the token endpoint answers invalid_grant
	rotate    bool   // a new refresh token with each access token
	refuseAll bool   // the MCP endpoint answers 401 to everything
	needScope string // the MCP endpoint answers 403 insufficient_scope
	seen      []string

	row         db.McpServer
	secrets     *lockedSecrets
	connections *fakeConnections
}

func newRenewWorld(t *testing.T) *renewWorld {
	t.Helper()
	w := &renewWorld{access: "at-0", refresh: "rt-0", rotate: true}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /as/token", func(rw http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.mu.Lock()
		defer w.mu.Unlock()
		w.asked++
		rw.Header().Set("Content-Type", "application/json")
		if w.revoked || r.PostForm.Get("refresh_token") != w.refresh {
			rw.WriteHeader(http.StatusBadRequest)
			_, _ = rw.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.refreshes++
		w.issued++
		w.access = fmt.Sprintf("at-%d", w.issued)
		body := map[string]any{"access_token": w.access, "expires_in": 3600, "token_type": "Bearer"}
		if w.rotate {
			w.refresh = fmt.Sprintf("rt-%d", w.issued)
			body["refresh_token"] = w.refresh
		}
		_ = json.NewEncoder(rw).Encode(body)
	})
	mux.HandleFunc("POST /mcp", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.seen = append(w.seen, got)
		switch {
		case w.needScope != "":
			rw.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+w.needScope+`"`)
			rw.WriteHeader(http.StatusForbidden)
		case w.refuseAll || got != w.access:
			rw.WriteHeader(http.StatusUnauthorized)
		default:
			rw.WriteHeader(http.StatusOK)
		}
	})
	w.Server = httptest.NewTLSServer(mux)
	t.Cleanup(w.Close)

	team, server := uuid.New(), uuid.New()
	u := w.URL + "/mcp"
	id := "client-1"
	w.row = db.McpServer{ID: server, TeamID: team, AuthKind: "oauth", Url: &u, OauthClientID: &id}
	w.secrets = &lockedSecrets{m: map[string]string{}}
	w.store("at-0", "rt-0", time.Now().Add(time.Hour))
	as, _ := json.Marshal(AuthServer{Issuer: w.URL + "/as", TokenURL: w.URL + "/as/token", Resource: u})
	w.connections = &fakeConnections{
		secrets: w.secrets, path: w.path(),
		conn: db.McpOauthConnection{ServerID: server, TeamID: team, Status: "connected", ConnectedAt: time.Now().Truncate(time.Microsecond), Auth: as},
	}
	return w
}

func (w *renewWorld) path() string { return secrets.Path(w.row.TeamID, secrets.KindMCPOAuth, w.row.ID) }

func (w *renewWorld) store(access, refresh string, expiry time.Time) {
	w.secrets.put(w.path(), map[string]string{
		OAuthAccessKey: access, OAuthRefreshKey: refresh, OAuthExpiryKey: expiry.UTC().Format(time.RFC3339),
	})
}

func (w *renewWorld) discoverer() *Discoverer {
	pool := x509.NewCertPool()
	pool.AddCert(w.Certificate())
	return New(w.secrets, Policy{Allow: allowAll, RootCAs: pool}, 5*time.Second).SignsInWith(w.connections)
}

// call is one request to the MCP endpoint with a client built as a run
// builds one.
func (w *renewWorld) call(ctx context.Context, d *Discoverer) error {
	c, err := d.Client(ctx, w.row)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *w.row.Url, strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	return nil
}

func (w *renewWorld) counts() (refreshes int, seen []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.refreshes, append([]string(nil), w.seen...)
}

type lockedSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *lockedSecrets) Get(_ context.Context, path, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[path+"#"+key]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (s *lockedSecrets) put(path string, values map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range values {
		s.m[path+"#"+k] = v
	}
}

// fakeConnections holds its mutex as the adapter holds the row: for the whole
// of renew.
type fakeConnections struct {
	row     sync.Mutex
	mu      sync.Mutex
	conn    db.McpOauthConnection
	none    bool
	secrets *lockedSecrets
	path    string
	marks   []string
}

func (f *fakeConnections) Connection(context.Context, uuid.UUID) (db.McpOauthConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.none {
		return db.McpOauthConnection{}, ErrNotSignedIn
	}
	return f.conn, nil
}

func (f *fakeConnections) Renew(ctx context.Context, _ uuid.UUID, renew func(context.Context, db.McpOauthConnection) (*Tokens, error)) error {
	f.row.Lock()
	defer f.row.Unlock()
	c, _ := f.Connection(ctx, uuid.Nil)
	t, err := renew(ctx, c)
	if err != nil || t == nil {
		return err
	}
	f.secrets.put(f.path, map[string]string{
		OAuthAccessKey: t.Access, OAuthRefreshKey: t.Refresh, OAuthExpiryKey: t.Expiry.UTC().Format(time.RFC3339),
	})
	return nil
}

func (f *fakeConnections) MarkNeedsSignIn(_ context.Context, _ uuid.UUID, connectedAt time.Time, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !connectedAt.Equal(f.conn.ConnectedAt) || f.conn.Status != "connected" {
		return nil
	}
	f.conn.Status, f.conn.Reason = "needs_sign_in", &reason
	f.marks = append(f.marks, reason)
	return nil
}

func (f *fakeConnections) status() (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn.Status, append([]string(nil), f.marks...)
}

func TestATokenStillGoodIsUsedAsItIs(t *testing.T) {
	w := newRenewWorld(t)
	if err := w.call(t.Context(), w.discoverer()); err != nil {
		t.Fatal(err)
	}
	if n, seen := w.counts(); n != 0 || len(seen) != 1 || seen[0] != "at-0" {
		t.Errorf("refreshes = %d, seen %v; want the stored token, no refresh", n, seen)
	}
}

// Renewed before the call, not after a 401: the server never sees the token
// that was about to expire.
func TestATokenAboutToExpireIsRenewedBeforeTheCall(t *testing.T) {
	w := newRenewWorld(t)
	w.store("at-0", "rt-0", time.Now().Add(30*time.Second))

	if err := w.call(t.Context(), w.discoverer()); err != nil {
		t.Fatal(err)
	}
	n, seen := w.counts()
	if n != 1 || len(seen) != 1 || seen[0] != "at-1" {
		t.Errorf("refreshes = %d, seen %v; want one refresh, then only the new token", n, seen)
	}
	if v, _ := w.secrets.Get(t.Context(), w.path(), OAuthRefreshKey); v != "rt-1" {
		t.Errorf("stored refresh token = %q, want the rotated one", v)
	}
}

// A server that rotates refresh tokens refuses one spent twice, so workers
// renewing at once must spend it once: the rest use what the first stored.
func TestWorkersRenewingAtOnceSpendTheRefreshTokenOnce(t *testing.T) {
	w := newRenewWorld(t)
	w.store("at-0", "rt-0", time.Now().Add(-time.Minute))

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- w.call(t.Context(), w.discoverer()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a worker failed: %v", err)
		}
	}
	if n, _ := w.counts(); n != 1 {
		t.Errorf("refreshes = %d, want 1", n)
	}
	if status, marks := w.connections.status(); status != "connected" {
		t.Errorf("status = %s (%v), want still connected", status, marks)
	}
}

// A server that does not rotate sends no refresh token back, and the one it
// has is kept rather than erased.
func TestARefreshTokenNotRotatedIsKept(t *testing.T) {
	w := newRenewWorld(t)
	w.rotate = false
	w.store("at-0", "rt-0", time.Now().Add(-time.Minute))

	if err := w.call(t.Context(), w.discoverer()); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.secrets.Get(t.Context(), w.path(), OAuthRefreshKey); v != "rt-0" {
		t.Errorf("stored refresh token = %q, want rt-0 kept", v)
	}
}

func TestARefusedRefreshMeansSigningInAgain(t *testing.T) {
	w := newRenewWorld(t)
	w.revoked = true
	w.store("at-0", "rt-0", time.Now().Add(-time.Minute))

	err := w.call(t.Context(), w.discoverer())
	if !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("err = %v, want ErrNeedsSignIn", err)
	}
	if status, marks := w.connections.status(); status != "needs_sign_in" || len(marks) != 1 {
		t.Errorf("status = %s, marks %v; want needs_sign_in, once", status, marks)
	}
	if _, seen := w.counts(); len(seen) != 0 {
		t.Errorf("the server was called with %v, after the sign-in ended", seen)
	}
}

func TestAnExpiredSignInWithNoRefreshTokenMeansSigningInAgain(t *testing.T) {
	w := newRenewWorld(t)
	w.store("at-0", "", time.Now().Add(-time.Minute))

	if err := w.call(t.Context(), w.discoverer()); !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("err = %v, want ErrNeedsSignIn", err)
	}
	if status, _ := w.connections.status(); status != "needs_sign_in" {
		t.Errorf("status = %s, want needs_sign_in", status)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.asked != 0 {
		t.Errorf("the token endpoint was asked %d times with no refresh token to give it", w.asked)
	}
}

// A token can be revoked before it expires: a 401 is tried once more, with a
// renewed one, and the sign-in stands.
func TestATokenRefusedEarlyIsRenewedAndTheCallMadeAgain(t *testing.T) {
	w := newRenewWorld(t)
	w.access = "at-revoked" // the server no longer takes at-0

	if err := w.call(t.Context(), w.discoverer()); err != nil {
		t.Fatalf("err = %v, want the call made again and answered", err)
	}
	n, seen := w.counts()
	if n != 1 || len(seen) != 2 || seen[0] != "at-0" || seen[1] != "at-1" {
		t.Errorf("refreshes = %d, seen %v; want at-0 refused, then at-1", n, seen)
	}
	if status, _ := w.connections.status(); status != "connected" {
		t.Errorf("status = %s, want still connected", status)
	}
}

func TestARenewedTokenRefusedEndsTheSignIn(t *testing.T) {
	w := newRenewWorld(t)
	w.refuseAll = true

	err := w.call(t.Context(), w.discoverer())
	if !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("err = %v, want ErrNeedsSignIn", err)
	}
	if n, seen := w.counts(); n != 1 || len(seen) != 2 {
		t.Errorf("refreshes = %d, seen %v; want one renewal, two tries", n, seen)
	}
	if status, marks := w.connections.status(); status != "needs_sign_in" || len(marks) != 1 || !strings.Contains(marks[0], "renewed") {
		t.Errorf("status = %s, marks %v", status, marks)
	}
}

func TestAServerWantingMoreScopeEndsTheSignInNamingThem(t *testing.T) {
	w := newRenewWorld(t)
	w.needScope = "files:write"

	err := w.call(t.Context(), w.discoverer())
	if !errors.Is(err, ErrNeedsSignIn) || !strings.Contains(err.Error(), "files:write") {
		t.Fatalf("err = %v, want ErrNeedsSignIn naming the scope", err)
	}
	if n, _ := w.counts(); n != 0 {
		t.Errorf("refreshes = %d; more scope is not a refresh's to give", n)
	}
	if _, marks := w.connections.status(); len(marks) != 1 || !strings.Contains(marks[0], "files:write") {
		t.Errorf("marks = %v, want the scope recorded", marks)
	}
}

// One already marked is not called at all: the run says so at once rather
// than spend a dead token on every call.
func TestASignInThatNeedsDoingAgainIsNotCalled(t *testing.T) {
	w := newRenewWorld(t)
	w.connections.conn.Status = "needs_sign_in"

	if err := w.call(t.Context(), w.discoverer()); !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("err = %v, want ErrNeedsSignIn", err)
	}
	if n, seen := w.counts(); n != 0 || len(seen) != 0 {
		t.Errorf("refreshes = %d, seen %v; want nothing sent", n, seen)
	}
}

func TestAServerNeverSignedInIsNotCalled(t *testing.T) {
	w := newRenewWorld(t)
	w.connections.none = true

	if err := w.call(t.Context(), w.discoverer()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
	if _, seen := w.counts(); len(seen) != 0 {
		t.Errorf("the server was called with %v", seen)
	}
}

// A sign-in made again while an old token was in flight is not ended by the
// old token's refusal: the mark names the sign-in it was read with.
func TestAnOldSignInsRefusalLeavesANewOneAlone(t *testing.T) {
	w := newRenewWorld(t)
	w.needScope = "files:write"
	d := w.discoverer()
	c, err := d.Client(t.Context(), w.row)
	if err != nil {
		t.Fatal(err)
	}

	// Signed in again after the client read its sign-in.
	w.connections.mu.Lock()
	w.connections.conn.ConnectedAt = w.connections.conn.ConnectedAt.Add(time.Second)
	w.connections.mu.Unlock()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, *w.row.Url, strings.NewReader(`{}`))
	if resp, err := c.Do(req); err == nil {
		_ = resp.Body.Close()
	}
	if status, marks := w.connections.status(); status != "connected" || len(marks) != 0 {
		t.Errorf("status = %s, marks %v; the new sign-in was ended by the old one", status, marks)
	}
}

func TestAStaticServerIsCalledWithItsTokenAsBefore(t *testing.T) {
	w := newRenewWorld(t)
	ref := "teams/" + w.row.TeamID.String() + "/mcp/static#token"
	w.secrets.put("teams/"+w.row.TeamID.String()+"/mcp/static", map[string]string{"token": "at-0"})
	w.row = db.McpServer{ID: w.row.ID, TeamID: w.row.TeamID, AuthKind: "static", Url: w.row.Url, AuthSecretRef: &ref}
	w.connections.none = true // a static server never asks for a sign-in

	if err := w.call(t.Context(), w.discoverer()); err != nil {
		t.Fatal(err)
	}
	if n, seen := w.counts(); n != 0 || len(seen) != 1 || seen[0] != "at-0" {
		t.Errorf("refreshes = %d, seen %v; want its static token", n, seen)
	}
}
