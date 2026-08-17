package login

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
	cmdcontext "github.com/LaplacianAI/openarity/apps/cli/internal/command/context"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential/store"
)

// Two commands, because a login needs somewhere to be stored and `oa context
// create` is what makes that exist. The coupling is the behaviour under test.
var commands = []clitest.Build{New, cmdcontext.New}

// identityProvider serves the three endpoints a device login touches. The
// interval is one second because it is the smallest RFC 8628 allows — the
// field is in whole seconds — so every test that reaches the polling loop pays
// for it.
func identityProvider(t *testing.T, token http.HandlerFunc) *httptest.Server {
	return providerSending(t, deviceBody(true), token)
}

// Not every provider sends verification_uri_complete, and a test that lets it
// stand in for the other two lines proves nothing: it carries the address and
// the code inside itself, so dropping both of the lines that matter still
// passes.
func deviceBody(complete bool) string {
	uri := ""
	if complete {
		uri = `"verification_uri_complete": "https://auth.example.com/device?code=WXYZ-ABCD",`
	}
	return `{
	  "device_code": "a-device-code",
	  "user_code": "WXYZ-ABCD",
	  "verification_uri": "https://auth.example.com/device",
	  ` + uri + `
	  "interval": 1,
	  "expires_in": 300
	}`
}

func providerSending(t *testing.T, device string, token http.HandlerFunc) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "issuer": "` + server.URL + `",
		  "device_authorization_endpoint": "` + server.URL + `/device/",
		  "token_endpoint": "` + server.URL + `/token/"
		}`))
	})
	mux.HandleFunc("/device/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(device))
	})
	mux.HandleFunc("/token/", token)

	return server
}

// brainStub publishes what `oa` needs to find the provider, and nothing else.
// oidc is a pointer in the generated type precisely so its absence can be
// tested, which is what oidcJSON is for.
func brainStub(t *testing.T, oidcJSON string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/auth/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "dev_token_accepted": false,
		  "environment": "production"` + oidcJSON + `
		}`))
	})

	return server
}

func withProvider(issuer string) string {
	return `,
		  "oidc": {"issuer": "` + issuer + `", "client_id": "openarity"}`
}

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func storedCredential(t *testing.T, name string) (token, refresh string) {
	t.Helper()

	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	cred, err := store.Open(dir).Get(name)
	if err != nil {
		t.Fatalf("read the credential for %s: %v", name, err)
	}
	return cred.Token, cred.Refresh
}

func TestLoginStoresACredentialForTheActiveContext(t *testing.T) {
	clitest.Isolate(t)

	idp := identityProvider(t, jsonHandler(http.StatusOK,
		`{"access_token":"an-access-token","refresh_token":"a-refresh-token","expires_in":3600}`))
	brain := brainStub(t, withProvider(idp.URL))

	clitest.Seed(t, commands, "context", "create", "staging", "--server", brain.URL)

	out, err := clitest.Execute(t, commands, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	token, refresh := storedCredential(t, "staging")
	if token != "an-access-token" {
		t.Errorf("stored token = %q", token)
	}
	// Without the refresh token the login works and dies an hour later with
	// nothing to renew from, which reads as a broken CLI.
	if refresh != "a-refresh-token" {
		t.Errorf("stored refresh token = %q", refresh)
	}
	if !strings.Contains(out, "logged in") {
		t.Errorf("the command did not report success:\n%s", out)
	}
}

// The code and the address are the whole interaction. A login that stores a
// credential but never tells anyone where to go is unusable.
func TestLoginPrintsTheCodeAndTheAddress(t *testing.T) {
	clitest.Isolate(t)

	idp := providerSending(t, deviceBody(false), jsonHandler(http.StatusOK,
		`{"access_token":"a","refresh_token":"r","expires_in":3600}`))
	brain := brainStub(t, withProvider(idp.URL))

	clitest.Seed(t, commands, "context", "create", "staging", "--server", brain.URL)

	out, err := clitest.Execute(t, commands, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	for _, want := range []string{"WXYZ-ABCD", "https://auth.example.com/device"} {
		if !strings.Contains(out, want) {
			t.Errorf("the prompt does not carry %q:\n%s", want, out)
		}
	}
}

// The one command whose whole purpose is handling a credential is the one most
// likely to print it by accident.
func TestLoginNeverPrintsTheToken(t *testing.T) {
	clitest.Isolate(t)

	const secret = "oa_live_7f3c9a_do_not_print"
	idp := identityProvider(t, jsonHandler(http.StatusOK,
		`{"access_token":"`+secret+`","refresh_token":"`+secret+`_refresh","expires_in":3600}`))
	brain := brainStub(t, withProvider(idp.URL))

	clitest.Seed(t, commands, "context", "create", "staging", "--server", brain.URL)

	out, err := clitest.Execute(t, commands, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the token is in the output:\n%s", out)
	}
	if token, _ := storedCredential(t, "staging"); token != secret {
		t.Errorf("stored token = %q — it was not printed, but it was not saved either", token)
	}
}

// authorization_pending arrives as an HTTP 400 that means "keep waiting", so
// the status alone can never be the answer. Stopping on the first one would
// make every real login fail, because nobody approves that fast.
func TestLoginWaitsThroughAuthorizationPending(t *testing.T) {
	clitest.Isolate(t)

	var polls atomic.Int32
	idp := identityProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if polls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600}`))
	})
	brain := brainStub(t, withProvider(idp.URL))

	clitest.Seed(t, commands, "context", "create", "staging", "--server", brain.URL)

	if _, err := clitest.Execute(t, commands, "login"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := polls.Load(); got != 2 {
		t.Errorf("polled %d times, want 2", got)
	}
	if token, _ := storedCredential(t, "staging"); token != "a" {
		t.Errorf("stored token = %q", token)
	}
}

// Someone pressed cancel. Nothing may be stored, and the message must not
// invite a retry of something that was refused on purpose.
func TestARefusedLoginStoresNothing(t *testing.T) {
	clitest.Isolate(t)

	idp := identityProvider(t, jsonHandler(http.StatusBadRequest, `{"error":"access_denied"}`))
	brain := brainStub(t, withProvider(idp.URL))

	clitest.Seed(t, commands, "context", "create", "staging", "--server", brain.URL)

	if _, err := clitest.Execute(t, commands, "login"); err == nil {
		t.Fatal("a refused login reported success")
	}
	if token, _ := storedCredential(t, "staging"); token != "" {
		t.Errorf("a refused login stored %q", token)
	}
}

// A development brain, or one still on a shared token, has no provider at all.
// The message has to say that rather than fail somewhere inside discovery with
// a URL built from an empty issuer.
func TestLoginSaysSoWhenTheBrainHasNoIdentityProvider(t *testing.T) {
	clitest.Isolate(t)

	brain := brainStub(t, "")
	clitest.Seed(t, commands, "context", "create", "staging", "--server", brain.URL)

	_, err := clitest.Execute(t, commands, "login")
	if err == nil {
		t.Fatal("logging in to a brain with no identity provider reported success")
	}
	if !strings.Contains(err.Error(), "identity provider") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
	if !strings.Contains(err.Error(), brain.URL) {
		t.Errorf("the error does not name the server: %v", err)
	}
}

// Nothing may reach a browser before this is known. Discovering there is
// nowhere to save a credential after someone has already approved is the worst
// possible place to fail.
func TestLoginRefusesBeforeAnyoneOpensABrowser(t *testing.T) {
	clitest.Isolate(t)

	var reached atomic.Bool
	idp := identityProvider(t, func(http.ResponseWriter, *http.Request) { reached.Store(true) })
	brain := brainStub(t, withProvider(idp.URL))
	t.Setenv("OPENARITY_SERVER", brain.URL)

	_, err := clitest.Execute(t, commands, "login")
	if err == nil {
		t.Fatal("logging in with no context reported success")
	}
	if !strings.Contains(err.Error(), "oa context create") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
	if reached.Load() {
		t.Error("the provider was contacted before the command knew where to store the result")
	}
}

// When the provider does send one, it is worth showing: it carries the code in
// the query string, so a browser opened at it needs nothing typed.
func TestTheCombinedLinkIsShownWhenTheProviderSendsOne(t *testing.T) {
	clitest.Isolate(t)

	idp := identityProvider(t, jsonHandler(http.StatusOK,
		`{"access_token":"a","refresh_token":"r","expires_in":3600}`))
	brain := brainStub(t, withProvider(idp.URL))

	clitest.Seed(t, commands, "context", "create", "staging", "--server", brain.URL)

	out, err := clitest.Execute(t, commands, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(out, "https://auth.example.com/device?code=WXYZ-ABCD") {
		t.Errorf("the combined link was not offered:\n%s", out)
	}
}
