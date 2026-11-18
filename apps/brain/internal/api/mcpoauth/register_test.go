package mcpoauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const registerURL = "https://accounts.example/register"

// unregistered is a world whose server has no client of its own, signing in
// to an authorization server that registers clients.
func unregistered(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.edit(func(s *db.McpServer) { s.OauthClientID, s.OauthClientSecretRef = nil, nil })
	w.disc.as.RegisterURL = registerURL
	w.disc.issued = discovery.Registration{ClientID: "reg-1"}
	return w
}

func clientIn(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
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
	return u.Query().Get("client_id")
}

func (w *world) registration(t *testing.T) (db.McpOauthClient, bool) {
	t.Helper()
	c, ok := w.store.clients[w.serverID]
	return c, ok
}

func TestAServerWithNoClientRegistersOneAndSignsInWithIt(t *testing.T) {
	w := unregistered(t)

	if got := clientIn(t, w.start(t)); got != "reg-1" {
		t.Errorf("authorize client_id = %q, want the registered reg-1", got)
	}
	if want := publicURL + CallbackPath; len(w.disc.registered) != 1 || w.disc.registered[0] != want {
		t.Errorf("registered with %v, want once with %q", w.disc.registered, want)
	}
	c, ok := w.registration(t)
	if !ok || c.ClientID != "reg-1" || c.Issuer != "https://accounts.example" || c.RedirectUri != publicURL+CallbackPath || c.SecretID != nil {
		t.Errorf("kept %+v (%v), want reg-1 for the issuer and redirect, with no secret", c, ok)
	}
	if len(w.secrets.written) != 0 {
		t.Errorf("wrote %v; a public client has no secret to keep", w.secrets.written)
	}
}

func TestACallbackExchangesWithTheRegisteredClient(t *testing.T) {
	w := unregistered(t)
	state := w.begun(t)

	if outcome, _ := w.callback(t, signedIn(state)); outcome != outcomeConnected {
		t.Fatalf("outcome = %q, want connected", outcome)
	}
	if len(w.disc.exchanged) != 1 || !strings.HasSuffix(w.disc.exchanged[0], "|reg-1|") {
		t.Errorf("exchanged %v, want once as reg-1 with no secret", w.disc.exchanged)
	}
}

// Registering on every Connect floods the authorization server with clients;
// one that still fits is used again.
func TestARegistrationIsReusedWhileItFits(t *testing.T) {
	w := unregistered(t)
	clientIn(t, w.start(t))
	w.disc.issued = discovery.Registration{ClientID: "reg-2"}

	if got := clientIn(t, w.start(t)); got != "reg-1" {
		t.Errorf("second sign-in used %q, want reg-1 again", got)
	}
	if len(w.disc.registered) != 1 {
		t.Errorf("registered %d times, want once", len(w.disc.registered))
	}
}

func TestARegistrationThatNoLongerFitsIsReplaced(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	secret := uuid.New()
	cases := map[string]func(c *db.McpOauthClient){
		"another authorization server": func(c *db.McpOauthClient) { c.Issuer = "https://old-issuer.example" },
		"another dashboard address":    func(c *db.McpOauthClient) { c.RedirectUri = "https://old-dash.example" + CallbackPath },
		"an expired secret":            func(c *db.McpOauthClient) { c.SecretID, c.SecretExpiresAt = &secret, &past },
	}
	for name, stale := range cases {
		t.Run(name, func(t *testing.T) {
			w := unregistered(t)
			old := db.McpOauthClient{
				ServerID: w.serverID, TeamID: w.team, Issuer: "https://accounts.example",
				RedirectUri: publicURL + CallbackPath, ClientID: "reg-old",
			}
			stale(&old)
			w.store.clients = map[uuid.UUID]db.McpOauthClient{w.serverID: old}

			if got := clientIn(t, w.start(t)); got != "reg-1" {
				t.Errorf("signed in as %q, want the new reg-1", got)
			}
			if c, _ := w.registration(t); c.ClientID != "reg-1" {
				t.Errorf("kept %q, want reg-1 in place of reg-old", c.ClientID)
			}
		})
	}
}

// A client someone registered by hand is the one they meant, whatever the
// brain registered before it.
func TestAClientSetOnTheServerWinsOverARegistration(t *testing.T) {
	w := newWorld(t)
	w.disc.as.RegisterURL = registerURL
	w.store.clients = map[uuid.UUID]db.McpOauthClient{w.serverID: {
		ServerID: w.serverID, TeamID: w.team, Issuer: "https://accounts.example",
		RedirectUri: publicURL + CallbackPath, ClientID: "reg-1",
	}}

	if got := clientIn(t, w.start(t)); got != "client-1" {
		t.Errorf("signed in as %q, want the server's own client-1", got)
	}
	if len(w.disc.registered) != 0 {
		t.Errorf("registered %d times, want none", len(w.disc.registered))
	}
}

func TestAServerThatCannotRegisterSaysHowToFixIt(t *testing.T) {
	w := unregistered(t)
	w.disc.as.RegisterURL = ""

	rec := w.start(t)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "register a client") {
		t.Errorf("start = %d %q, want 409 naming the fix", rec.Code, rec.Body)
	}
	if _, ok := w.registration(t); ok || len(w.store.pending) != 0 {
		t.Errorf("kept a client or a pending sign-in; want nothing")
	}
}

// What the authorization server said is shown; what it sent is not: a raw
// reply may be a page of HTML, or echo the request.
func TestARefusedRegistrationSaysWhyAndNothingElse(t *testing.T) {
	w := unregistered(t)
	w.disc.regErr = &discovery.RegistrationError{
		Reason: "redirect_uri is not allowed",
		Cause:  errors.New("registration failed with status 400: <html>secret-echo</html>"),
	}

	rec := w.start(t)
	body := rec.Body.String()
	if rec.Code != http.StatusBadGateway || !strings.Contains(body, "redirect_uri is not allowed") {
		t.Errorf("start = %d %q, want 502 with the reason", rec.Code, body)
	}
	if strings.Contains(body, "secret-echo") {
		t.Errorf("answered with the raw reply: %q", body)
	}
	if _, ok := w.registration(t); ok || len(w.store.pending) != 0 {
		t.Error("kept a client or a pending sign-in; want nothing")
	}
}

func TestAnIssuedSecretIsKeptInTheSecretStoreAndUsed(t *testing.T) {
	w := unregistered(t)
	expires := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	w.disc.issued = discovery.Registration{ClientID: "reg-1", ClientSecret: "s3cret", SecretExpiresAt: expires}
	state := w.begun(t)

	c, _ := w.registration(t)
	if c.SecretID == nil || c.SecretExpiresAt == nil || !c.SecretExpiresAt.Equal(expires) {
		t.Fatalf("kept %+v, want a secret id and its expiry", c)
	}
	path := secrets.Path(w.team, secrets.KindMCPOAuthClient, *c.SecretID)
	if got := w.secrets.written[path][discovery.OAuthClientSecretKey]; got != "s3cret" {
		t.Errorf("secret at %s = %q, want s3cret", path, got)
	}
	if _, _ = w.callback(t, signedIn(state)); len(w.disc.exchanged) != 1 || !strings.HasSuffix(w.disc.exchanged[0], "|reg-1|s3cret") {
		t.Errorf("exchanged %v, want as reg-1 with its secret", w.disc.exchanged)
	}
}

// Two sign-ins that both registered keep the first registration; the second
// signs in with it, so its code is exchanged by the client it was issued to.
func TestTwoSignInsRegisteringAtOnceKeepTheFirst(t *testing.T) {
	w := unregistered(t)
	w.disc.onRegister = func() {
		w.store.clients = map[uuid.UUID]db.McpOauthClient{w.serverID: {
			ServerID: w.serverID, TeamID: w.team, Issuer: "https://accounts.example",
			RedirectUri: publicURL + CallbackPath, ClientID: "reg-first",
		}}
	}

	if got := clientIn(t, w.start(t)); got != "reg-first" {
		t.Errorf("signed in as %q, want reg-first, the registration kept", got)
	}
	if c, _ := w.registration(t); c.ClientID != "reg-first" {
		t.Errorf("kept %q, want reg-first", c.ClientID)
	}
}

func TestARefusedRegisteredClientIsForgottenSoTheNextConnectRegisters(t *testing.T) {
	cases := map[string]struct {
		query func(state string) url.Values
		setup func(w *world)
	}{
		"at the token endpoint": {signedIn, func(w *world) { w.disc.exchErr = discovery.ErrClientRefused }},
		"on the way back": {func(s string) url.Values {
			return url.Values{"state": {s}, "error": {"invalid_client"}}
		}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := unregistered(t)
			state := w.begun(t)
			if tc.setup != nil {
				tc.setup(w)
			}
			if outcome, _ := w.callback(t, tc.query(state)); outcome != outcomeUnregistered {
				t.Errorf("outcome = %q, want %q", outcome, outcomeUnregistered)
			}
			if _, ok := w.registration(t); ok || len(w.store.forgotten) != 1 || w.store.forgotten[0] != "reg-1" {
				t.Errorf("forgotten %v; want reg-1 gone", w.store.forgotten)
			}
		})
	}
}

func TestARefusedClientSetOnTheServerIsNotForgotten(t *testing.T) {
	w := newWorld(t)
	state := w.begun(t)
	w.disc.exchErr = discovery.ErrClientRefused

	if outcome, _ := w.callback(t, signedIn(state)); outcome != outcomeFailed {
		t.Errorf("outcome = %q, want %q", outcome, outcomeFailed)
	}
	if len(w.store.forgotten) != 0 {
		t.Errorf("forgot %v; a client set on the server is its person's", w.store.forgotten)
	}
}

func (w *world) forget(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
		"/teams/"+w.team.String()+"/mcp-servers/"+w.serverID.String()+"/oauth/registration", http.NoBody)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{
		ID: uuid.New(), Subject: "ann", Teams: []auth.Membership{{TeamID: w.team, Role: "member"}},
	}))
	rec := httptest.NewRecorder()
	w.mux(t).ServeHTTP(rec, req)
	return rec
}

func TestRegisterAgainForgetsTheRegisteredClient(t *testing.T) {
	w := unregistered(t)
	clientIn(t, w.start(t))

	if rec := w.forget(t); rec.Code != http.StatusNoContent {
		t.Fatalf("forget = %d %s", rec.Code, rec.Body)
	}
	if _, ok := w.registration(t); ok {
		t.Error("the registration is still there")
	}
	w.disc.issued = discovery.Registration{ClientID: "reg-2"}
	if got := clientIn(t, w.start(t)); got != "reg-2" {
		t.Errorf("next sign-in used %q, want a new registration, reg-2", got)
	}
}

func TestRegisterAgainIsRefusedWhereItMeansNothing(t *testing.T) {
	cases := map[string]struct {
		setup func(w *world)
		want  int
	}{
		"a client set on the server": {func(w *world) { w.edit(func(s *db.McpServer) { s.OauthClientID = new(string) }) }, http.StatusConflict},
		"a static server":            {func(w *world) { w.edit(func(s *db.McpServer) { s.AuthKind = "static" }) }, http.StatusConflict},
		"another team's":             {func(w *world) { w.edit(func(s *db.McpServer) { s.TeamID = uuid.New() }) }, http.StatusNotFound},
		"without tool:write":         {func(w *world) { w.allowed = false }, http.StatusForbidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := unregistered(t)
			w.store.clients = map[uuid.UUID]db.McpOauthClient{w.serverID: {ServerID: w.serverID, ClientID: "reg-1"}}
			tc.setup(w)
			if rec := w.forget(t); rec.Code != tc.want {
				t.Errorf("forget = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if _, ok := w.registration(t); !ok {
				t.Error("the registration was forgotten")
			}
		})
	}
}

func TestRegisterAgainWithNothingRegisteredChangesNothing(t *testing.T) {
	w := unregistered(t)
	if rec := w.forget(t); rec.Code != http.StatusNoContent {
		t.Errorf("forget = %d, want 204", rec.Code)
	}
}
