package discovery

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// authWorld is one server playing an MCP server, its protected resource
// metadata, an authorization server under /as and its token endpoint, each
// part switchable, recording what reached the token endpoint.
type authWorld struct {
	*httptest.Server

	challenge   string // the 401's WWW-Authenticate; "" answers 200
	prmAt       string // "path", "root", "challenge" or "" for none
	asServers   bool   // whether the metadata names the authorization server
	asSlash     bool   // whether it is named with a trailing slash, as Google's is
	pkce        []string
	offline     bool
	tokenStatus int
	tokenBody   string

	mu    sync.Mutex
	forms []url.Values
	basic []string
}

func newAuthWorld(t *testing.T) *authWorld {
	t.Helper()
	w := &authWorld{
		prmAt: "path", asServers: true, pkce: []string{"S256"}, tokenStatus: http.StatusOK,
		tokenBody: `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600,"token_type":"Bearer","scope":"files:read"}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", func(rw http.ResponseWriter, _ *http.Request) {
		if w.challenge == "" {
			rw.WriteHeader(http.StatusOK)
			return
		}
		rw.Header().Set("WWW-Authenticate", strings.ReplaceAll(w.challenge, "{srv}", w.URL))
		rw.WriteHeader(http.StatusUnauthorized)
	})
	prm := func(at, resource string) http.HandlerFunc {
		return func(rw http.ResponseWriter, _ *http.Request) {
			if w.prmAt != at {
				http.NotFound(rw, nil)
				return
			}
			rw.Header().Set("Content-Type", "application/json")
			doc := map[string]any{"resource": strings.ReplaceAll(resource, "{srv}", w.URL), "scopes_supported": []string{"files:read", "files:write"}}
			if w.asServers {
				as := w.URL + "/as"
				if w.asSlash {
					as += "/"
				}
				doc["authorization_servers"] = []string{as}
			}
			_ = json.NewEncoder(rw).Encode(doc)
		}
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", prm("path", "{srv}/mcp"))
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", prm("root", "{srv}"))
	mux.HandleFunc("GET /meta", prm("challenge", "{srv}/mcp"))
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/as", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		doc := map[string]any{
			"issuer": w.URL + "/as", "authorization_endpoint": w.URL + "/as/authorize", "token_endpoint": w.URL + "/as/token",
			"revocation_endpoint": w.URL + "/as/revoke", "response_types_supported": []string{"code"},
			"code_challenge_methods_supported": w.pkce, "authorization_response_iss_parameter_supported": true,
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		}
		if w.offline {
			doc["scopes_supported"] = []string{"offline_access"}
		}
		_ = json.NewEncoder(rw).Encode(doc)
	})
	mux.HandleFunc("POST /as/token", func(rw http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.mu.Lock()
		w.forms = append(w.forms, r.PostForm)
		w.basic = append(w.basic, r.Header.Get("Authorization"))
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(w.tokenStatus)
		_, _ = rw.Write([]byte(w.tokenBody))
	})
	w.Server = httptest.NewServer(mux)
	t.Cleanup(w.Close)
	return w
}

func (w *authWorld) discoverer() *Discoverer {
	return New(&fakeSecrets{}, Policy{Allow: allowAll}, 5*time.Second)
}

func (w *authWorld) lastForm(t *testing.T) url.Values {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.forms) == 0 {
		t.Fatal("nothing reached the token endpoint")
	}
	return w.forms[len(w.forms)-1]
}

func TestTheChallengeNamesWhereTheMetadataIsAndWhichScopes(t *testing.T) {
	w := newAuthWorld(t)
	w.challenge = `Bearer resource_metadata="{srv}/meta", scope="files:read"`
	w.prmAt = "challenge"

	as, err := w.discoverer().DiscoverAuth(t.Context(), w.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if as.Resource != w.URL+"/mcp" || as.Issuer != w.URL+"/as" || as.TokenURL != w.URL+"/as/token" ||
		as.RevokeURL != w.URL+"/as/revoke" || !as.IssResponse {
		t.Errorf("auth server = %+v", as)
	}
	if !slices.Equal(as.Scopes, []string{"files:read"}) {
		t.Errorf("scopes = %v, want the challenge's, not every one supported", as.Scopes)
	}
}

// Gmail answers initialize with 200 before any sign-in, so a server with no
// challenge is still looked for at its well-known paths.
func TestAServerThatSendsNoChallengeIsFoundAtItsPath(t *testing.T) {
	w := newAuthWorld(t)

	as, err := w.discoverer().DiscoverAuth(t.Context(), w.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if as.Resource != w.URL+"/mcp" || !slices.Equal(as.Scopes, []string{"files:read", "files:write"}) {
		t.Errorf("resource %q, scopes %v; want the path's metadata and its scopes_supported", as.Resource, as.Scopes)
	}
}

func TestMetadataAtTheRootNamesTheRootAsTheResource(t *testing.T) {
	w := newAuthWorld(t)
	w.prmAt = "root"

	as, err := w.discoverer().DiscoverAuth(t.Context(), w.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if as.Resource != w.URL {
		t.Errorf("resource = %q, want the root %q", as.Resource, w.URL)
	}
}

func TestAServerThatCannotBeSignedInToSaysSo(t *testing.T) {
	cases := map[string]func(w *authWorld){
		"no metadata anywhere":          func(w *authWorld) { w.prmAt = "" },
		"metadata naming no authority":  func(w *authWorld) { w.asServers = false },
		"an authority without PKCE":     func(w *authWorld) { w.pkce = nil },
		"an authority with plain alone": func(w *authWorld) { w.pkce = []string{"plain"} },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			w := newAuthWorld(t)
			setup(w)
			if _, err := w.discoverer().DiscoverAuth(t.Context(), w.URL+"/mcp"); err == nil {
				t.Fatal("discovered a sign-in the server cannot do")
			}
		})
	}
	t.Run("PKCE without S256 is named", func(t *testing.T) {
		w := newAuthWorld(t)
		w.pkce = []string{"plain"}
		if _, err := w.discoverer().DiscoverAuth(t.Context(), w.URL+"/mcp"); !errors.Is(err, ErrNoPKCE) {
			t.Errorf("err = %v, want ErrNoPKCE", err)
		}
	})
}

// Metadata names URLs the brain then fetches, so every fetch is held to the
// addresses discovery may reach.
func TestDiscoveryReachesOnlyWhatThePolicyAllows(t *testing.T) {
	w := newAuthWorld(t)
	d := New(&fakeSecrets{}, Policy{Allow: allowNone}, 5*time.Second)
	if _, err := d.DiscoverAuth(t.Context(), w.URL+"/mcp"); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}

func authorizeQuery(t *testing.T, as AuthServer, scopes []string) url.Values {
	t.Helper()
	start, err := AuthorizeURL(as, Client{ID: "client-1"}, "https://dash.test/oauth/mcp/callback", scopes)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != start.State || start.Verifier == "" {
		t.Fatalf("start = %+v; want its state in the URL and a verifier kept", start)
	}
	return q
}

var exampleAS = AuthServer{
	Issuer: "https://auth.test", AuthorizeURL: "https://auth.test/authorize", TokenURL: "https://auth.test/token",
	Resource: "https://mcp.test/mcp", Scopes: []string{"files:read"},
}

func TestTheAuthorizationURLCarriesPKCEAndTheResource(t *testing.T) {
	q := authorizeQuery(t, exampleAS, nil)
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Errorf("pkce = %q %q, want S256", q.Get("code_challenge_method"), q.Get("code_challenge"))
	}
	if q.Get("resource") != "https://mcp.test/mcp" || q.Get("scope") != "files:read" ||
		q.Get("redirect_uri") != "https://dash.test/oauth/mcp/callback" || q.Get("client_id") != "client-1" {
		t.Errorf("query = %v", q)
	}
}

// Gmail lists every scope it has, full mailbox access included: the server's
// own scopes, when set, win over what discovery found.
func TestScopesGivenWinOverTheDiscoveredOnes(t *testing.T) {
	q := authorizeQuery(t, exampleAS, []string{"gmail.readonly", "gmail.compose"})
	if q.Get("scope") != "gmail.readonly gmail.compose" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestARefreshTokenIsAskedForTheWayTheAuthorityTakesIt(t *testing.T) {
	t.Run("offline_access when it is listed", func(t *testing.T) {
		as := exampleAS
		as.OfflineAccess = true
		if q := authorizeQuery(t, as, nil); q.Get("scope") != "files:read offline_access" || q.Get("access_type") != "" {
			t.Errorf("query = %v", q)
		}
	})
	t.Run("Google's parameters for Google alone", func(t *testing.T) {
		as := exampleAS
		as.Issuer, as.OfflineAccess = googleIssuer, true
		q := authorizeQuery(t, as, nil)
		if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || strings.Contains(q.Get("scope"), "offline_access") {
			t.Errorf("query = %v", q)
		}
	})
	t.Run("nothing for anyone else", func(t *testing.T) {
		if q := authorizeQuery(t, exampleAS, nil); q.Get("access_type") != "" || q.Get("prompt") != "" {
			t.Errorf("query = %v", q)
		}
	})
}

func TestEveryStateIsItsOwn(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		start, err := AuthorizeURL(exampleAS, Client{ID: "c"}, "https://dash.test/cb", nil)
		if err != nil {
			t.Fatal(err)
		}
		if seen[start.State] || len(start.State) < 43 {
			t.Fatalf("state %q repeated or short", start.State)
		}
		seen[start.State] = true
	}
}

func (w *authWorld) as() AuthServer {
	return AuthServer{
		Issuer: w.URL + "/as", TokenURL: w.URL + "/as/token", Resource: w.URL + "/mcp",
		TokenAuth: []string{"client_secret_basic", "client_secret_post"},
	}
}

func TestTheCodeIsExchangedWithTheVerifierAndTheResource(t *testing.T) {
	w := newAuthWorld(t)

	tok, err := w.discoverer().Exchange(t.Context(), w.as(), Client{ID: "client-1", Secret: "s3cret"},
		"https://dash.test/cb", "code-1", "verifier-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "at-1" || tok.Refresh != "rt-1" || !slices.Equal(tok.Scopes, []string{"files:read"}) || time.Until(tok.Expiry) < 50*time.Minute {
		t.Errorf("tokens = %+v", tok)
	}
	f := w.lastForm(t)
	if f.Get("code") != "code-1" || f.Get("code_verifier") != "verifier-1" || f.Get("resource") != w.URL+"/mcp" ||
		f.Get("redirect_uri") != "https://dash.test/cb" {
		t.Errorf("form = %v", f)
	}
	if f.Get("client_secret") != "" || !strings.HasPrefix(w.basic[0], "Basic ") {
		t.Errorf("a confidential client's secret went in the form, not the header: %v", f)
	}
}

func TestACodeTheAuthorityRefusesMeansSigningInAgain(t *testing.T) {
	w := newAuthWorld(t)
	w.tokenStatus, w.tokenBody = http.StatusBadRequest, `{"error":"invalid_grant"}`

	_, err := w.discoverer().Exchange(t.Context(), w.as(), Client{ID: "c", Secret: "s"}, "https://dash.test/cb", "used", "v")
	if !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("err = %v, want ErrSignInAgain", err)
	}
}

func TestARefreshNamesTheResource(t *testing.T) {
	w := newAuthWorld(t)
	w.tokenBody = `{"access_token":"at-2","expires_in":3600,"token_type":"Bearer"}`

	tok, err := w.discoverer().Refresh(t.Context(), w.as(), Client{ID: "client-1", Secret: "s3cret"}, "rt-1")
	if err != nil {
		t.Fatal(err)
	}
	f := w.lastForm(t)
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rt-1" || f.Get("resource") != w.URL+"/mcp" {
		t.Errorf("form = %v", f)
	}
	if tok.Access != "at-2" || tok.Refresh != "" {
		t.Errorf("tokens = %+v; want the new access token and no refresh token, so the old is kept", tok)
	}
}

func TestAPublicClientNamesItselfInTheForm(t *testing.T) {
	w := newAuthWorld(t)
	if _, err := w.discoverer().Refresh(t.Context(), w.as(), Client{ID: "public-1"}, "rt-1"); err != nil {
		t.Fatal(err)
	}
	if f := w.lastForm(t); f.Get("client_id") != "public-1" || w.basic[0] != "" {
		t.Errorf("form %v, authorization %q; want the client in the form, no header", f, w.basic[0])
	}
}

// Only a refused grant is a sign-in lost. An authority having a bad minute is
// not, or every outage would sign the team out of its servers.
func TestOnlyARefusedGrantMeansSigningInAgain(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		again  bool
	}{
		"invalid_grant":  {http.StatusBadRequest, `{"error":"invalid_grant"}`, true},
		"unavailable":    {http.StatusServiceUnavailable, `<html>down</html>`, false},
		"invalid_client": {http.StatusUnauthorized, `{"error":"invalid_client","error_description":"rt-1 is wrong"}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newAuthWorld(t)
			w.tokenStatus, w.tokenBody = tc.status, tc.body
			_, err := w.discoverer().Refresh(t.Context(), w.as(), Client{ID: "c", Secret: "s"}, "rt-1")
			if err == nil || errors.Is(err, ErrSignInAgain) != tc.again {
				t.Fatalf("err = %v, want sign in again %v", err, tc.again)
			}
			if strings.Contains(err.Error(), "rt-1") {
				t.Errorf("the error repeats what was sent: %v", err)
			}
		})
	}
}

func TestASecretOnlyPostedGoesInTheForm(t *testing.T) {
	as := AuthServer{TokenAuth: []string{"client_secret_post"}}
	if authStyle(as, Client{ID: "c", Secret: "s"}) != oauth2.AuthStyleInParams {
		t.Error("a post-only authority would be sent the secret in a header it ignores")
	}
}

// Google's resource metadata names https://accounts.google.com/, whose
// metadata answers only without the slash.
func TestAnAuthorityNamedWithATrailingSlashIsFound(t *testing.T) {
	w := newAuthWorld(t)
	w.asSlash = true

	as, err := w.discoverer().DiscoverAuth(t.Context(), w.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if as.Issuer != w.URL+"/as" {
		t.Errorf("issuer = %q, want it without the slash", as.Issuer)
	}
}
