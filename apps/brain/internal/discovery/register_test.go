package discovery

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// registrar is an authorization server's registration endpoint, answering
// what the test sets, and keeping what it was sent.
func registrar(t *testing.T, status int, reply string) (*Discoverer, AuthServer, *map[string]any) {
	t.Helper()
	sent := map[string]any{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/register" {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	d := New(&lockedSecrets{m: map[string]string{}}, Policy{Allow: allowAll, RootCAs: pool}, 5*time.Second)
	return d, AuthServer{Issuer: srv.URL, RegisterURL: srv.URL + "/register"}, &sent
}

const redirect = "https://dash.example/oauth/mcp/callback"

func TestRegisteringAsksForAPublicClientForTheCallback(t *testing.T) {
	d, as, sent := registrar(t, http.StatusCreated, `{"client_id":"reg-1"}`)

	got, err := d.Register(t.Context(), as, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != "reg-1" || got.ClientSecret != "" || !got.SecretExpiresAt.IsZero() {
		t.Errorf("registration = %+v, want reg-1 with no secret", got)
	}
	want := map[string]any{
		"client_name": "openarity", "redirect_uris": []any{redirect},
		"grant_types": []any{"authorization_code", "refresh_token"}, "response_types": []any{"code"},
		"token_endpoint_auth_method": "none", "application_type": "web",
	}
	for k, v := range want {
		if b, _ := json.Marshal((*sent)[k]); string(b) != mustJSON(t, v) {
			t.Errorf("sent %s = %s, want %s", k, b, mustJSON(t, v))
		}
	}
}

func TestAnIssuedSecretAndItsExpiryAreReturned(t *testing.T) {
	d, as, _ := registrar(t, http.StatusCreated, `{"client_id":"reg-1","client_secret":"s3","client_secret_expires_at":2000000000}`)

	got, err := d.Register(t.Context(), as, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientSecret != "s3" || !got.SecretExpiresAt.Equal(time.Unix(2000000000, 0)) {
		t.Errorf("registration = %+v, want the secret and its expiry", got)
	}
}

// RFC 7591 defaults an omitted method to client_secret_basic, so the method
// is always named: none where it is allowed, else one the server lists.
func TestTheAuthMethodIsOneTheServerAllows(t *testing.T) {
	cases := []struct {
		supported []string
		want      string
	}{
		{nil, "none"},
		{[]string{"client_secret_basic", "none"}, "none"},
		{[]string{"client_secret_post", "client_secret_basic"}, "client_secret_basic"},
		{[]string{"client_secret_post"}, "client_secret_post"},
	}
	for _, tc := range cases {
		if got := authMethod(tc.supported); got != tc.want {
			t.Errorf("authMethod(%v) = %q, want %q", tc.supported, got, tc.want)
		}
	}
}

func TestNoRegistrationEndpointIsErrNoRegistration(t *testing.T) {
	d, as, _ := registrar(t, http.StatusCreated, `{"client_id":"reg-1"}`)
	as.RegisterURL = ""
	if _, err := d.Register(t.Context(), as, redirect); !errors.Is(err, ErrNoRegistration) {
		t.Errorf("err = %v, want ErrNoRegistration", err)
	}
}

func TestARefusalCarriesTheServersReason(t *testing.T) {
	d, as, _ := registrar(t, http.StatusBadRequest, `{"error":"invalid_redirect_uri","error_description":"redirect not allowed"}`)

	_, err := d.Register(t.Context(), as, redirect)
	var refused *RegistrationError
	if !errors.Is(err, ErrRegistrationRefused) || !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a RegistrationError", err)
	}
	if refused.Reason != "redirect not allowed" || !strings.HasSuffix(err.Error(), ": redirect not allowed") {
		t.Errorf("reason %q, message %q", refused.Reason, err.Error())
	}
}

func TestARefusalWithNoReasonShowsNoneOfTheReply(t *testing.T) {
	d, as, _ := registrar(t, http.StatusForbidden, `<html>echo of what was sent</html>`)

	_, err := d.Register(t.Context(), as, redirect)
	if !errors.Is(err, ErrRegistrationRefused) {
		t.Fatalf("err = %v, want ErrRegistrationRefused", err)
	}
	if err.Error() != ErrRegistrationRefused.Error() {
		t.Errorf("message %q shows the reply", err.Error())
	}
}

func TestAReasonIsClippedToASentence(t *testing.T) {
	long := strings.Repeat("é", 400)
	if got := clip(long); len([]rune(got)) != 301 || !strings.HasSuffix(got, "…") {
		t.Errorf("clip kept %d runes", len([]rune(got)))
	}
	if got := clip("short"); got != "short" {
		t.Errorf("clip(short) = %q", got)
	}
}

func TestARegistrationIsUsableOnlyWhileItFits(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	id := uuid.New()
	base := db.McpOauthClient{Issuer: "https://as.example/", RedirectUri: redirect, ClientID: "reg-1"}
	cases := map[string]struct {
		edit func(*db.McpOauthClient)
		want bool
	}{
		"as it was made":         {func(*db.McpOauthClient) {}, true},
		"a trailing slash apart": {func(c *db.McpOauthClient) { c.Issuer = "https://as.example" }, true},
		"another issuer":         {func(c *db.McpOauthClient) { c.Issuer = "https://other.example" }, false},
		"another redirect":       {func(c *db.McpOauthClient) { c.RedirectUri = "https://old.example/cb" }, false},
		"a secret still good":    {func(c *db.McpOauthClient) { c.SecretID, c.SecretExpiresAt = &id, &future }, true},
		"a secret expired":       {func(c *db.McpOauthClient) { c.SecretID, c.SecretExpiresAt = &id, &past }, false},
	}
	for name, tc := range cases {
		c := base
		tc.edit(&c)
		if got := Usable(c, "https://as.example", redirect, now); got != tc.want {
			t.Errorf("%s: Usable = %v, want %v", name, got, tc.want)
		}
	}
}

type clientsOf map[uuid.UUID]db.McpOauthClient

func (c clientsOf) GetOAuthClient(_ context.Context, server uuid.UUID) (db.McpOauthClient, error) {
	reg, ok := c[server]
	if !ok {
		return db.McpOauthClient{}, pgx.ErrNoRows
	}
	return reg, nil
}

func TestClientOfPrefersTheServersOwnClient(t *testing.T) {
	team, server, secretID := uuid.New(), uuid.New(), uuid.New()
	sec := &lockedSecrets{m: map[string]string{
		secrets.Path(team, secrets.KindMCPOAuthClient, secretID) + "#" + OAuthClientSecretKey: "reg-secret",
	}}
	clients := clientsOf{server: {ServerID: server, TeamID: team, ClientID: "reg-1", SecretID: &secretID}}
	own := "client-1"

	got, err := ClientOf(t.Context(), sec, clients, db.McpServer{ID: server, TeamID: team, OauthClientID: &own})
	if err != nil || got.ID != "client-1" || got.Secret != "" {
		t.Errorf("with its own client: %+v, %v; want client-1", got, err)
	}
	got, err = ClientOf(t.Context(), sec, clients, db.McpServer{ID: server, TeamID: team})
	if err != nil || got.ID != "reg-1" || got.Secret != "reg-secret" {
		t.Errorf("with none of its own: %+v, %v; want reg-1 and its secret", got, err)
	}
	got, err = ClientOf(t.Context(), sec, clientsOf{}, db.McpServer{ID: server, TeamID: team})
	if err != nil || got != (Client{}) {
		t.Errorf("with nothing registered: %+v, %v; want the empty client", got, err)
	}
}
