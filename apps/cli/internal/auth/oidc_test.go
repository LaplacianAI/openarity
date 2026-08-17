package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubProvider serves a discovery document naming itself, the way a real one
// does. The issuer is the server's own address because that is the check
// NewProvider makes — a fixed string here would test nothing.
func stubProvider(t *testing.T, document func(issuer string) string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(document(server.URL)))
	})
	return server
}

func fullDocument(issuer string) string {
	return `{
	  "issuer": "` + issuer + `",
	  "device_authorization_endpoint": "` + issuer + `/device/",
	  "token_endpoint": "` + issuer + `/token/"
	}`
}

func TestNewProviderReadsEveryEndpoint(t *testing.T) {
	t.Parallel()

	server := stubProvider(t, fullDocument)

	p, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if p.token != server.URL+"/token/" {
		t.Errorf("token endpoint = %q", p.token)
	}
	if p.device != server.URL+"/device/" {
		t.Errorf("device endpoint = %q", p.device)
	}
}

// The brain serves the issuer verbatim, trailing slash included. Joining
// naively gives `…//.well-known/…`, which some providers 404 and others
// redirect — a baffling failure for a URL nobody typed.
func TestATrailingSlashOnTheIssuerIsNotDoubled(t *testing.T) {
	t.Parallel()

	// A plain handler, deliberately not http.ServeMux: the mux rewrites `//`
	// to `/` and redirects, so the client follows and the doubled slash never
	// reaches the assertion. The bug would be invisible.
	var asked string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asked == "" {
			asked = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fullDocument(server.URL)))
	}))
	t.Cleanup(server.Close)

	if _, err := NewProvider(t.Context(), server.Client(), server.URL+"/", "a-client"); err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if asked != "/.well-known/openid-configuration" {
		t.Errorf("fetched %q, want no doubled slash", asked)
	}
}

// The one security check here. Without it, anything able to answer at the
// issuer's address names a token endpoint it controls and collects the login.
func TestAProviderNamingSomeoneElseIsRefused(t *testing.T) {
	t.Parallel()

	server := stubProvider(t, func(string) string {
		return `{
		  "issuer": "https://attacker.example.com/",
		  "device_authorization_endpoint": "https://attacker.example.com/device/",
		  "token_endpoint": "https://attacker.example.com/token/"
		}`
	})

	_, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client")
	if err == nil {
		t.Fatal("a discovery document naming a different issuer was accepted")
	}
	if !strings.Contains(err.Error(), "attacker.example.com") {
		t.Errorf("the error does not name what it found: %v", err)
	}
}

// The same value with and without a trailing slash is the same issuer. Failing
// here would refuse every correctly configured authentik, which serves the
// issuer with the slash and the document without it.
func TestATrailingSlashIsNotAnIssuerMismatch(t *testing.T) {
	t.Parallel()

	server := stubProvider(t, fullDocument)

	if _, err := NewProvider(t.Context(), server.Client(), server.URL+"/", "a-client"); err != nil {
		t.Errorf("a trailing slash was treated as a different issuer: %v", err)
	}
}

// Every path through Provider needs it, so it is a constructor invariant.
func TestADocumentWithNoTokenEndpointIsRefused(t *testing.T) {
	t.Parallel()

	server := stubProvider(t, func(issuer string) string {
		return `{"issuer": "` + issuer + `", "device_authorization_endpoint": "` + issuer + `/device/"}`
	})

	if _, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client"); err == nil {
		t.Fatal("a document with no token endpoint was accepted")
	}
}

// Refreshing needs only the token endpoint, so a provider that cannot do the
// device flow must still construct. Rejecting it here would break `oa teams
// list` on a provider that refreshes perfectly well.
func TestADocumentWithNoDeviceEndpointStillConstructs(t *testing.T) {
	t.Parallel()

	server := stubProvider(t, func(issuer string) string {
		return `{"issuer": "` + issuer + `", "token_endpoint": "` + issuer + `/token/"}`
	})

	p, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client")
	if err != nil {
		t.Fatalf("a provider without the device flow was refused: %v", err)
	}
	if p.device != "" {
		t.Errorf("device endpoint = %q, want empty", p.device)
	}
}

func TestANonJSONDocumentIsAnError(t *testing.T) {
	t.Parallel()

	server := stubProvider(t, func(string) string { return "<html>login page</html>" })

	if _, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client"); err == nil {
		t.Fatal("an HTML body was accepted as a discovery document")
	}
}

// Pointing at a host that is not an identity provider is the common typo, and
// the status is what says so.
func TestANonOKDiscoveryNamesTheStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	_, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client")
	if err == nil {
		t.Fatal("a 404 was accepted")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("the error does not carry the status: %v", err)
	}
}

// exchange is what both the device flow and the refresh end in.
func tokenServer(t *testing.T, status int, body string) (*Provider, *url.Values) {
	t.Helper()

	sent := &url.Values{}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fullDocument(server.URL)))
	})
	mux.HandleFunc("/token/", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*sent = r.Form
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	p, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p, sent
}

// expires_in is seconds from a response that has already arrived, so this
// package turns it into a moment. A caller storing a duration cannot answer
// "is this dead?" tomorrow.
func TestExchangeResolvesTheLifetimeToAMoment(t *testing.T) {
	t.Parallel()

	p, _ := tokenServer(t, http.StatusOK,
		`{"access_token":"an-access","refresh_token":"a-refresh","expires_in":3600}`)

	fixed := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return fixed }

	tok, oerr, err := p.exchange(t.Context(), url.Values{})
	if err != nil || oerr != nil {
		t.Fatalf("exchange: err=%v oauth=%v", err, oerr)
	}
	if tok.Access != "an-access" || tok.Refresh != "a-refresh" {
		t.Errorf("token = %+v", tok)
	}
	if want := fixed.Add(time.Hour); !tok.Expiry.Equal(want) {
		t.Errorf("Expiry = %v, want %v", tok.Expiry, want)
	}
}

// The client id goes on every form. A provider rejects a request without it,
// and the failure reads like a configuration problem rather than a missing
// field.
func TestExchangeAlwaysSendsTheClientID(t *testing.T) {
	t.Parallel()

	p, sent := tokenServer(t, http.StatusOK, `{"access_token":"a","expires_in":60}`)

	if _, _, err := p.exchange(t.Context(), url.Values{"grant_type": {"refresh_token"}}); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if got := sent.Get("client_id"); got != "a-client" {
		t.Errorf("client_id = %q", got)
	}
	if got := sent.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q — the caller's own fields were dropped", got)
	}
}

// A 400 carrying authorization_pending is a normal step in the device flow,
// not a failure. Returning it as an error would make the polling loop stop on
// its first tick.
func TestAnOAuthErrorIsDataNotAFailure(t *testing.T) {
	t.Parallel()

	p, _ := tokenServer(t, http.StatusBadRequest,
		`{"error":"authorization_pending","error_description":"waiting"}`)

	tok, oerr, err := p.exchange(t.Context(), url.Values{})
	if err != nil {
		t.Fatalf("a named OAuth error was returned as a transport failure: %v", err)
	}
	if tok != nil {
		t.Error("a token was returned alongside an error")
	}
	if oerr == nil || oerr.Code != "authorization_pending" {
		t.Fatalf("oauth error = %+v, want authorization_pending", oerr)
	}
	if oerr.Error() != "waiting" {
		t.Errorf("Error() = %q, want the description", oerr.Error())
	}
}

// A 200 with no access token is not a success. Treating it as one stores an
// empty credential and every later call fails with 401 instead.
func TestAnEmptyTokenIsAnError(t *testing.T) {
	t.Parallel()

	p, _ := tokenServer(t, http.StatusOK, `{"expires_in":3600}`)

	if _, _, err := p.exchange(t.Context(), url.Values{}); err == nil {
		t.Fatal("a response with no access token was accepted")
	}
}

// An unnamed non-200 must not be silently successful.
func TestAnUnnamedFailureNamesTheStatus(t *testing.T) {
	t.Parallel()

	p, _ := tokenServer(t, http.StatusInternalServerError, `{}`)

	_, _, err := p.exchange(t.Context(), url.Values{})
	if err == nil {
		t.Fatal("a 500 with no OAuth error was accepted")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error does not carry the status: %v", err)
	}
}

// Falls back to the code when the provider sends no description, because an
// error rendering as an empty string tells the caller nothing.
func TestAnOAuthErrorWithNoDescriptionStillReads(t *testing.T) {
	t.Parallel()

	e := oauthError{Code: "expired_token"}
	if e.Error() != "expired_token" {
		t.Errorf("Error() = %q, want the code", e.Error())
	}
}
