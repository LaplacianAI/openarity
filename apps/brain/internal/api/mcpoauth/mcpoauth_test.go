package mcpoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const publicURL = "https://dash.example"

// --- fakes ---

type fakeStore struct {
	mu        sync.Mutex
	servers   map[uuid.UUID]db.McpServer
	pending   map[string]db.McpOauthPending
	connected []db.UpsertOAuthConnectionParams
	pruned    int
	insertErr error
}

func (s *fakeStore) GetMCPServer(_ context.Context, id uuid.UUID) (db.McpServer, error) {
	row, ok := s.servers[id]
	if !ok {
		return db.McpServer{}, pgx.ErrNoRows
	}
	return row, nil
}

func (s *fakeStore) InsertOAuthPending(_ context.Context, p db.InsertOAuthPendingParams) error {
	if s.insertErr != nil {
		return s.insertErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[string(p.StateHash)] = db.McpOauthPending{
		StateHash: p.StateHash, TeamID: p.TeamID, ServerID: p.ServerID, StartedBy: p.StartedBy,
		Verifier: p.Verifier, RedirectUri: p.RedirectUri, Auth: p.Auth, ExpiresAt: p.ExpiresAt,
	}
	return nil
}

// TakeOAuthPending deletes as it reads, as the query does.
func (s *fakeStore) TakeOAuthPending(_ context.Context, hash []byte) (db.McpOauthPending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[string(hash)]
	if !ok || !p.ExpiresAt.After(time.Now()) {
		return db.McpOauthPending{}, pgx.ErrNoRows
	}
	delete(s.pending, string(hash))
	return p, nil
}

func (s *fakeStore) PruneOAuthPending(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, p := range s.pending {
		if !p.ExpiresAt.After(time.Now()) {
			delete(s.pending, k)
			n++
		}
	}
	s.pruned++
	return n, nil
}

func (s *fakeStore) UpsertOAuthConnection(_ context.Context, p db.UpsertOAuthConnectionParams) (db.McpOauthConnection, error) {
	s.connected = append(s.connected, p)
	return db.McpOauthConnection{ServerID: p.ServerID}, nil
}

type fakeDisc struct {
	as         discovery.AuthServer
	discErr    error
	tokens     discovery.Tokens
	exchErr    error
	exchanged  []string // code|verifier|redirect
	discovered int
}

func (d *fakeDisc) DiscoverAuth(context.Context, string) (discovery.AuthServer, error) {
	d.discovered++
	return d.as, d.discErr
}

func (d *fakeDisc) Exchange(_ context.Context, _ discovery.AuthServer, c discovery.Client, redirect, code, verifier string) (discovery.Tokens, error) {
	d.exchanged = append(d.exchanged, code+"|"+verifier+"|"+redirect+"|"+c.ID+"|"+c.Secret)
	return d.tokens, d.exchErr
}

type fakeSecrets struct {
	values  map[string]string
	written map[string]map[string]string
	putErr  error
}

func (s *fakeSecrets) Get(_ context.Context, path, key string) (string, error) {
	v, ok := s.values[path+"#"+key]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (s *fakeSecrets) PutAll(_ context.Context, path string, values map[string]string) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.written[path] = values
	return nil
}

type fakeRefresher struct {
	err   error
	asked int
}

func (r *fakeRefresher) Refresh(context.Context, db.McpServer) ([]discovery.Tool, error) {
	r.asked++
	return nil, r.err
}

type allow struct{ yes bool }

func (allow) IsSuperAdmin(*auth.User) bool { return false }
func (a allow) Can(context.Context, *auth.User, authz.Action, authz.Resource) (bool, error) {
	return a.yes, nil
}

func (allow) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("no route here is any_team")
}

type liveTeams struct{}

func (liveTeams) TeamDeleted(context.Context, uuid.UUID) (bool, error) { return false, nil }

// --- the world ---

type world struct {
	team, serverID uuid.UUID
	store          *fakeStore
	disc           *fakeDisc
	secrets        *fakeSecrets
	refresher      *fakeRefresher
	publicURL      string
	allowed        bool
	logged         bytes.Buffer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	team, server := uuid.New(), uuid.New()
	u, id := "https://gmailmcp.example/mcp/v1", "client-1"
	ref := secrets.TeamPath(team, secrets.KindMCP) + "/google#secret"
	w := &world{
		team: team, serverID: server, publicURL: publicURL, allowed: true,
		store: &fakeStore{servers: map[uuid.UUID]db.McpServer{server: {
			ID: server, TeamID: team, Name: "gmail", Url: &u, AuthKind: "oauth",
			OauthClientID: &id, OauthClientSecretRef: &ref, OauthScopes: []string{"gmail.readonly"},
		}}, pending: map[string]db.McpOauthPending{}},
		disc: &fakeDisc{
			as: discovery.AuthServer{
				Issuer: "https://accounts.example", AuthorizeURL: "https://accounts.example/authorize",
				TokenURL: "https://accounts.example/token", RevokeURL: "https://accounts.example/revoke",
				Resource: u, Scopes: []string{"gmail.readonly"}, IssResponse: true,
			},
			tokens: discovery.Tokens{Access: "at-1", Refresh: "rt-1", Expiry: time.Now().Add(time.Hour), Scopes: []string{"gmail.readonly"}},
		},
		secrets:   &fakeSecrets{values: map[string]string{ref: "client-secret-1"}, written: map[string]map[string]string{}},
		refresher: &fakeRefresher{},
	}
	return w
}

func (w *world) mux(t *testing.T) *http.ServeMux {
	t.Helper()
	rs := authz.NewRoutes()
	write := "tool:write"
	if err := rs.Add("POST", "/teams/{id}/mcp-servers/{serverID}/oauth/start", "team", &write); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(&w.logged, nil))
	start, callback := New(logger, w.store, w.disc, w.secrets, w.refresher, w.publicURL)
	mux := http.NewServeMux()
	g := api.NewGuard(rs, allow{w.allowed}, liveTeams{}, logger)
	start.Register(mux, g)
	callback.Register(mux, g)
	return mux
}

func (w *world) start(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/teams/"+w.team.String()+"/mcp-servers/"+w.serverID.String()+"/oauth/start", http.NoBody)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{
		ID: uuid.New(), Subject: "ann", Teams: []auth.Membership{{TeamID: w.team, Role: "member"}},
	}))
	rec := httptest.NewRecorder()
	w.mux(t).ServeHTTP(rec, req)
	return rec
}

// begun starts a sign-in and returns the state the authorization server
// would hand back.
func (w *world) begun(t *testing.T) string {
	t.Helper()
	rec := w.start(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	var got startResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func (w *world) callback(t *testing.T, query url.Values) (outcome string, rec *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, CallbackPath+"?"+query.Encode(), http.NoBody)
	rec = httptest.NewRecorder()
	w.mux(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d, want a redirect: %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Scheme+"://"+loc.Host != publicURL || loc.Path != "/ui/servers" {
		t.Fatalf("redirected to %q; want the dashboard's servers screen, under PUBLIC_URL", rec.Header().Get("Location"))
	}
	return loc.Query().Get("oauth"), rec
}

func signedIn(state string) url.Values {
	return url.Values{"state": {state}, "code": {"code-1"}, "iss": {"https://accounts.example"}}
}

// --- start ---

func TestStartingASignInSendsThePersonToTheAuthorizationServer(t *testing.T) {
	w := newWorld(t)
	rec := w.start(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	var got startResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	u, _ := url.Parse(got.AuthorizeURL)
	q := u.Query()
	if u.Host != "accounts.example" || q.Get("client_id") != "client-1" || q.Get("scope") != "gmail.readonly" ||
		q.Get("redirect_uri") != publicURL+CallbackPath || q.Get("code_challenge") == "" {
		t.Errorf("authorize URL = %s", got.AuthorizeURL)
	}
	if len(w.store.pending) != 1 {
		t.Fatalf("%d pending, want 1", len(w.store.pending))
	}
	for hash, p := range w.store.pending {
		if strings.Contains(rec.Body.String(), p.Verifier) || hash == q.Get("state") {
			t.Error("the verifier reached the browser, or the state is stored as itself")
		}
		if time.Until(p.ExpiresAt) > pendingFor || time.Until(p.ExpiresAt) < pendingFor-time.Minute {
			t.Errorf("the sign-in lasts until %s, want ten minutes", p.ExpiresAt)
		}
	}
}

// Every refusal is decided before the authorization server is asked or
// anything is written.
func TestAStartThatCannotWorkIsRefusedAndWritesNothing(t *testing.T) {
	cases := map[string]struct {
		setup func(w *world)
		want  int
	}{
		"a static server":    {func(w *world) { w.edit(func(s *db.McpServer) { s.AuthKind = "static" }) }, http.StatusConflict},
		"a command server":   {func(w *world) { w.edit(func(s *db.McpServer) { s.Url = nil }) }, http.StatusConflict},
		"no PUBLIC_URL":      {func(w *world) { w.publicURL = "" }, http.StatusConflict},
		"no OAuth client":    {func(w *world) { w.edit(func(s *db.McpServer) { s.OauthClientID = nil }) }, http.StatusConflict},
		"another team's":     {func(w *world) { w.edit(func(s *db.McpServer) { s.TeamID = uuid.New() }) }, http.StatusNotFound},
		"no such server":     {func(w *world) { w.store.servers = map[uuid.UUID]db.McpServer{} }, http.StatusNotFound},
		"without tool:write": {func(w *world) { w.allowed = false }, http.StatusForbidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			tc.setup(w)
			if rec := w.start(t); rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if len(w.store.pending) != 0 || w.disc.discovered != 0 {
				t.Errorf("pending %d, discovered %d; want nothing", len(w.store.pending), w.disc.discovered)
			}
		})
	}
}

func (w *world) edit(fn func(*db.McpServer)) {
	s := w.store.servers[w.serverID]
	fn(&s)
	w.store.servers[w.serverID] = s
}

// Gmail lists twenty scopes, full mailbox access among them: asking for all
// of them because none were chosen asks for everything.
func TestAServerOfferingManyScopesNeedsTheTeamToChoose(t *testing.T) {
	w := newWorld(t)
	w.edit(func(s *db.McpServer) { s.OauthScopes = nil })
	w.disc.as.Scopes = []string{"a", "b", "c", "d", "e", "f"}

	rec := w.start(t)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "6 scopes") {
		t.Errorf("status = %d %s; want a 409 naming the six", rec.Code, rec.Body)
	}
	if len(w.store.pending) != 0 {
		t.Error("a sign-in asking for every scope was begun")
	}
}

func TestADiscoveryThatFailsSaysWhy(t *testing.T) {
	w := newWorld(t)
	w.disc.discErr = discovery.ErrNoPKCE
	rec := w.start(t)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "PKCE") {
		t.Errorf("status = %d %s; want a 502 saying why", rec.Code, rec.Body)
	}
}

// --- callback ---

func TestACallbackKeepsTheTokensAndTheConnectionAndFindsTheTools(t *testing.T) {
	w := newWorld(t)
	state := w.begun(t)

	outcome, _ := w.callback(t, signedIn(state))

	if outcome != outcomeConnected {
		t.Fatalf("outcome = %q, want connected", outcome)
	}
	if len(w.disc.exchanged) != 1 || !strings.HasPrefix(w.disc.exchanged[0], "code-1|") ||
		!strings.HasSuffix(w.disc.exchanged[0], "|"+publicURL+CallbackPath+"|client-1|client-secret-1") {
		t.Errorf("exchanged %v; want the code, the verifier, the same redirect and the client with its secret", w.disc.exchanged)
	}
	tokens := w.secrets.written[secrets.Path(w.team, secrets.KindMCPOAuth, w.serverID)]
	if tokens[discovery.OAuthAccessKey] != "at-1" || tokens[discovery.OAuthRefreshKey] != "rt-1" || tokens[discovery.OAuthExpiryKey] == "" {
		t.Errorf("tokens written = %v", tokens)
	}
	if len(w.store.connected) != 1 || w.store.connected[0].Issuer != "https://accounts.example" ||
		*w.store.connected[0].RevocationEndpoint != "https://accounts.example/revoke" {
		t.Errorf("connections = %+v", w.store.connected)
	}
	if w.refresher.asked != 1 {
		t.Errorf("tools discovered %d times, want once", w.refresher.asked)
	}
	for _, leak := range []string{state, "code-1", "at-1", "rt-1", "client-secret-1"} {
		if strings.Contains(w.logged.String(), leak) {
			t.Errorf("the log carries %q", leak)
		}
	}
}

// Taken once: a state replayed, made up, or come back too late finds nothing
// and changes nothing.
func TestAStateWorksOnce(t *testing.T) {
	w := newWorld(t)
	state := w.begun(t)
	if outcome, _ := w.callback(t, signedIn(state)); outcome != outcomeConnected {
		t.Fatalf("first callback = %q", outcome)
	}

	for name, q := range map[string]url.Values{
		"replayed": signedIn(state),
		"made up":  signedIn("not-a-state"),
		"no state": {"code": {"code-1"}},
	} {
		t.Run(name, func(t *testing.T) {
			if outcome, _ := w.callback(t, q); outcome != outcomeExpired {
				t.Errorf("outcome = %q, want expired", outcome)
			}
		})
	}
	if len(w.disc.exchanged) != 1 || len(w.store.connected) != 1 {
		t.Errorf("exchanged %d, connected %d; want the first callback's alone", len(w.disc.exchanged), len(w.store.connected))
	}
}

func TestALateCallbackFindsNothing(t *testing.T) {
	w := newWorld(t)
	state := w.begun(t)
	for k, p := range w.store.pending {
		p.ExpiresAt = time.Now().Add(-time.Second)
		w.store.pending[k] = p
	}
	if outcome, _ := w.callback(t, signedIn(state)); outcome != outcomeExpired || len(w.disc.exchanged) != 0 {
		t.Errorf("outcome %q, exchanged %d; want expired and nothing exchanged", outcome, len(w.disc.exchanged))
	}
}

// What comes back other than a clean code ends the sign-in, and nothing is
// written: no tokens, no connection.
func TestACallbackThatIsNotAClearYesWritesNothing(t *testing.T) {
	cases := map[string]struct {
		query   func(state string) url.Values
		setup   func(w *world)
		outcome string
	}{
		"the person declined": {func(s string) url.Values { return url.Values{"state": {s}, "error": {"access_denied"}} }, nil, outcomeDenied},
		"an error with a code": {func(s string) url.Values {
			q := signedIn(s)
			q.Set("error", "access_denied")
			return q
		}, nil, outcomeDenied},
		"no code": {func(s string) url.Values { return url.Values{"state": {s}, "iss": {"https://accounts.example"}} }, nil, outcomeDenied},
		"another issuer": {func(s string) url.Values {
			q := signedIn(s)
			q.Set("iss", "https://evil.example")
			return q
		}, nil, outcomeRefused},
		"no issuer, when it is sent": {func(s string) url.Values {
			q := signedIn(s)
			q.Del("iss")
			return q
		}, nil, outcomeRefused},
		"the code refused":          {signedIn, func(w *world) { w.disc.exchErr = discovery.ErrSignInAgain }, outcomeRefused},
		"the exchange fails":        {signedIn, func(w *world) { w.disc.exchErr = errors.New("connection reset") }, outcomeFailed},
		"the server became static":  {signedIn, func(w *world) { w.edit(func(s *db.McpServer) { s.AuthKind = "static" }) }, outcomeRefused},
		"the tokens cannot be kept": {signedIn, func(w *world) { w.secrets.putErr = errors.New("sealed") }, outcomeFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			state := w.begun(t)
			if tc.setup != nil {
				tc.setup(w)
			}
			if outcome, _ := w.callback(t, tc.query(state)); outcome != tc.outcome {
				t.Errorf("outcome = %q, want %q", outcome, tc.outcome)
			}
			if len(w.secrets.written) != 0 || len(w.store.connected) != 0 {
				t.Errorf("written %v, connected %d; want nothing", w.secrets.written, len(w.store.connected))
			}
		})
	}
}

// The sign-in is the person's work; a server that then fails to list its
// tools keeps it, and says so.
func TestASignInWhoseToolsCannotBeFoundIsKept(t *testing.T) {
	w := newWorld(t)
	w.refresher.err = errors.New("the server answered 500")
	state := w.begun(t)

	if outcome, _ := w.callback(t, signedIn(state)); outcome != outcomeNoTools {
		t.Errorf("outcome = %q, want connected-no-tools", outcome)
	}
	if len(w.store.connected) != 1 {
		t.Error("the sign-in was dropped because discovery failed")
	}
}

// The browser goes back to the dashboard, whatever the request names.
func TestTheCallbackNeverSendsTheBrowserElsewhere(t *testing.T) {
	w := newWorld(t)
	q := signedIn("x")
	q.Set("redirect_uri", "https://evil.example")
	q.Set("next", "https://evil.example")
	w.callback(t, q) // callback asserts the destination
}

func TestStartingASignInClearsAbandonedOnes(t *testing.T) {
	w := newWorld(t)
	w.store.pending["old"] = db.McpOauthPending{StateHash: []byte("old"), ExpiresAt: time.Now().Add(-time.Hour)}

	w.begun(t)

	if _, ok := w.store.pending["old"]; ok || w.store.pruned != 1 {
		t.Errorf("pruned %d times, old still there %v; want it cleared", w.store.pruned, ok)
	}
}
