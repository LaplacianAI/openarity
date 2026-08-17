package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/cli/internal/auth"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

// The silent renewal is the one piece of the login that nobody ever watches
// run. Every failure in it is quiet: a token that stops being renewed logs
// somebody out an hour later, and a transient provider failure treated as a
// dead login logs out everybody at once. Neither shows up in normal use until
// it matters.

const storeLocation = "the test store"

// fakeStore is a credential.Store that counts what was done to it. Location is
// the load-bearing part: renewIfExpired compares it against the source Resolve
// picked, and that comparison is the whole rule for which credential may be
// replaced.
type fakeStore struct {
	cred     credential.Credential
	location string

	setCalls    []credential.Credential
	deleteCalls []string
	setErr      error
}

func (f *fakeStore) Get(string) (credential.Credential, error) { return f.cred, nil }

func (f *fakeStore) Set(_ string, cred credential.Credential) error {
	f.setCalls = append(f.setCalls, cred)
	if f.setErr != nil {
		return f.setErr
	}
	f.cred = cred
	return nil
}

func (f *fakeStore) Delete(context string) error {
	f.deleteCalls = append(f.deleteCalls, context)
	return nil
}

func (f *fakeStore) Rename(string, string) error { return nil }

func (f *fakeStore) Location() string {
	if f.location == "" {
		return storeLocation
	}
	return f.location
}

// identityProvider is a brain and an OIDC provider in one server: /auth/config
// points discovery at the same host, so a test needs one URL. token decides
// what the refresh grant answers.
func identityProvider(t *testing.T, token http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var refreshes atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/auth/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "environment": "production",
		  "dev_token_accepted": false,
		  "oidc": {"issuer": "` + server.URL + `", "client_id": "openarity"}
		}`))
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "issuer": "` + server.URL + `",
		  "device_authorization_endpoint": "` + server.URL + `/device/",
		  "token_endpoint": "` + server.URL + `/token/"
		}`))
	})
	mux.HandleFunc("/token/", func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		token(w, r)
	})

	return server, &refreshes
}

func renewed(access, refresh string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + access +
			`","refresh_token":"` + refresh + `","expires_in":3600}`))
	}
}

func refusing(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// optionsFor assembles the parts of Options that renewal reads, the way Load
// leaves them. Source defaults to the store's own location — the case where a
// credential may be replaced — so a test that wants a different precedence
// says so explicitly.
func optionsFor(t *testing.T, server string, store *fakeStore, source string) *Options {
	t.Helper()

	api, err := NewClient(server)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if source == "" {
		source = store.Location()
	}

	return &Options{
		Saved:       config.Config{Current: "staging", Contexts: map[string]config.Context{"staging": {Server: server}}},
		Settings:    config.Settings{Server: config.Setting{Value: server}, Token: config.Setting{Source: source}},
		Credentials: store,
		credential:  store.cred,
		bare:        api,
	}
}

func expired() credential.Credential {
	return credential.Credential{
		Token:   "an-old-access-token",
		Refresh: "a-refresh-token",
		Expiry:  time.Now().Add(-time.Hour),
	}
}

func TestAnExpiredCredentialIsRenewedAndStored(t *testing.T) {
	t.Parallel()

	server, refreshes := identityProvider(t, renewed("a-new-access-token", "a-new-refresh-token"))
	store := &fakeStore{cred: expired()}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err != nil {
		t.Fatalf("renewIfExpired: %v", err)
	}

	if refreshes.Load() != 1 {
		t.Errorf("the provider was asked %d times, want 1", refreshes.Load())
	}
	if len(store.setCalls) != 1 {
		t.Fatalf("stored %d credentials, want 1", len(store.setCalls))
	}
	if store.setCalls[0].Token != "a-new-access-token" {
		t.Errorf("stored token = %q", store.setCalls[0].Token)
	}
	// Held in memory too, or the request about to be sent carries the token
	// that was just replaced.
	if opts.credential.Token != "a-new-access-token" {
		t.Errorf("in-memory token = %q, want the renewed one", opts.credential.Token)
	}
}

// A token from --token or OPENARITY_TOKEN has no refresh token behind it and
// is not ours to replace. The guard reuses the source Resolve already picked,
// so the rule for which credential is renewed cannot drift from the rule for
// which one is sent.
func TestOnlyTheStoredCredentialIsRenewed(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"--token", "OPENARITY_TOKEN", "", "a different store"} {
		server, refreshes := identityProvider(t, renewed("should-not-happen", "nor-this"))
		store := &fakeStore{cred: expired()}

		opts := &Options{
			Saved:       config.Config{Current: "staging"},
			Settings:    config.Settings{Server: config.Setting{Value: server.URL}, Token: config.Setting{Source: source}},
			Credentials: store,
			credential:  store.cred,
		}

		if err := opts.renewIfExpired(t.Context()); err != nil {
			t.Fatalf("source %q: %v", source, err)
		}
		if refreshes.Load() != 0 {
			t.Errorf("source %q: a credential that did not come from the store was renewed", source)
		}
		if len(store.setCalls) != 0 {
			t.Errorf("source %q: the store was written to", source)
		}
	}
}

// The common case, every command, all day. A renewal on a token with an hour
// left is a round trip to the identity provider before every request.
func TestALiveCredentialIsLeftAlone(t *testing.T) {
	t.Parallel()

	server, refreshes := identityProvider(t, renewed("should-not-happen", "nor-this"))
	store := &fakeStore{cred: credential.Credential{
		Token: "a-live-token", Refresh: "a-refresh-token", Expiry: time.Now().Add(time.Hour),
	}}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err != nil {
		t.Fatalf("renewIfExpired: %v", err)
	}
	if refreshes.Load() != 0 {
		t.Errorf("a live credential was renewed %d times", refreshes.Load())
	}
	if opts.credential.Token != "a-live-token" {
		t.Errorf("token = %q, want it untouched", opts.credential.Token)
	}
}

// A credential set by hand, or one whose provider issued no refresh token, has
// nothing to renew with. Asking anyway spends a round trip to be told so, and
// produces an error naming a grant rather than the situation.
func TestACredentialWithNoRefreshTokenIsNotRenewed(t *testing.T) {
	t.Parallel()

	server, refreshes := identityProvider(t, renewed("should-not-happen", "nor-this"))
	store := &fakeStore{cred: credential.Credential{
		Token: "an-old-token", Expiry: time.Now().Add(-time.Hour),
	}}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err != nil {
		t.Fatalf("renewIfExpired: %v", err)
	}
	if refreshes.Load() != 0 {
		t.Errorf("a credential with nothing to renew from was sent %d times", refreshes.Load())
	}
}

// invalid_grant is the provider saying this login is over — revoked, expired,
// or already spent. Keeping it would mean every later command retries a grant
// that can never succeed.
func TestARevokedLoginIsDiscarded(t *testing.T) {
	t.Parallel()

	server, _ := identityProvider(t,
		refusing(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"revoked"}`))
	store := &fakeStore{cred: expired()}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err == nil {
		t.Fatal("a revoked login was reported as renewed")
	}
	if len(store.deleteCalls) != 1 || store.deleteCalls[0] != "staging" {
		t.Errorf("deleted %v, want the active context's credential", store.deleteCalls)
	}
}

// The one that would be quietly wrong for months. A provider having a bad
// minute is not a dead login: discarding here would log out everybody the
// moment the identity provider restarted, and from the outside that is
// indistinguishable from a genuine revocation.
func TestATransientProviderFailureKeepsTheCredential(t *testing.T) {
	t.Parallel()

	for name, reply := range map[string]http.HandlerFunc{
		"named as temporary": refusing(http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`),
		"a bare 500":         refusing(http.StatusInternalServerError, `{}`),
		"an upstream error":  refusing(http.StatusBadGateway, `{"error":"server_error"}`),
	} {
		server, _ := identityProvider(t, reply)
		store := &fakeStore{cred: expired()}
		opts := optionsFor(t, server.URL, store, "")

		if err := opts.renewIfExpired(t.Context()); err == nil {
			t.Fatalf("%s: a failed renewal reported success", name)
		}
		if len(store.deleteCalls) != 0 {
			t.Errorf("%s: a transient failure discarded the login", name)
		}
		if store.cred.Token != "an-old-access-token" {
			t.Errorf("%s: the stored credential changed to %q", name, store.cred.Token)
		}
	}
}

// The brain being unreachable is not a dead login either — and this runs
// before every authenticated command, so a VPN drop must not cost a
// credential.
func TestAnUnreachableBrainKeepsTheCredential(t *testing.T) {
	t.Parallel()

	server, _ := identityProvider(t, renewed("unused", "unused"))
	store := &fakeStore{cred: expired()}
	opts := optionsFor(t, server.URL, store, "")
	server.Close()

	if err := opts.renewIfExpired(t.Context()); err == nil {
		t.Fatal("renewal against an unreachable brain reported success")
	}
	if len(store.deleteCalls) != 0 {
		t.Errorf("an unreachable brain discarded the login: %v", store.deleteCalls)
	}
}

// Providers without rotation send no refresh_token back and expect the old one
// to be reused. Storing what came back gives a login that renews exactly once
// and then sends somebody to `oa login` an hour later — which looks identical
// to a rotating provider whose new token was dropped, and needs the opposite
// fix.
func TestARefreshTokenSurvivesAProviderThatSendsNoneBack(t *testing.T) {
	t.Parallel()

	server, _ := identityProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a-new-access-token","expires_in":3600}`))
	})
	store := &fakeStore{cred: expired()}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err != nil {
		t.Fatalf("renewIfExpired: %v", err)
	}
	if len(store.setCalls) != 1 {
		t.Fatalf("stored %d credentials, want 1", len(store.setCalls))
	}
	if store.setCalls[0].Refresh != "a-refresh-token" {
		t.Errorf("stored refresh token = %q, want the original kept", store.setCalls[0].Refresh)
	}
}

// A keychain that refuses the write is a failure, not a renewal. Carrying on
// would use a token in memory that nothing can renew from next time, and the
// command after this one would be back at square one with no explanation.
func TestAFailedWriteIsReported(t *testing.T) {
	t.Parallel()

	server, _ := identityProvider(t, renewed("a-new-access-token", "a-new-refresh-token"))
	store := &fakeStore{cred: expired(), setErr: errNoRoom}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err == nil {
		t.Fatal("a failed write was reported as a successful renewal")
	}
	if opts.credential.Token != "an-old-access-token" {
		t.Errorf("in-memory token = %q, want it unchanged when the write failed", opts.credential.Token)
	}
}

// The whole point is that nobody sees this happen. Renewal runs inside API, so
// the request that follows must carry the new token without any command
// knowing a renewal took place.
func TestTheRenewedTokenIsWhatGetsSent(t *testing.T) {
	t.Parallel()

	var sent atomic.Value
	sent.Store("")

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/auth/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"environment":"production","dev_token_accepted":false,
		  "oidc":{"issuer":"` + server.URL + `","client_id":"openarity"}}`))
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `",
		  "device_authorization_endpoint":"` + server.URL + `/device/",
		  "token_endpoint":"` + server.URL + `/token/"}`))
	})
	mux.HandleFunc("/token/", renewed("a-new-access-token", "a-new-refresh-token"))
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		sent.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d",
		  "kind":"user","subject":"alice","teams":[]}`))
	})

	store := &fakeStore{cred: expired()}
	opts := optionsFor(t, server.URL, store, "")

	api, err := opts.API(t.Context())
	if err != nil {
		t.Fatalf("API: %v", err)
	}
	if _, err := api.GetWhoamiWithResponse(t.Context()); err != nil {
		t.Fatalf("whoami: %v", err)
	}

	got, ok := sent.Load().(string)
	if !ok {
		t.Fatalf("nothing recorded the Authorization header: %v", sent.Load())
	}
	if got != "Bearer a-new-access-token" {
		t.Errorf("sent %q, want the renewed token", got)
	}
}

// Nothing here may print a token, and an error is the easiest place for one to
// escape — the refresh token is a request parameter, so a client that echoed
// its request would carry it.
func TestRenewalNeverPrintsACredential(t *testing.T) {
	t.Parallel()

	const secret = "oa_refresh_7f3c9a_do_not_print"

	server, _ := identityProvider(t,
		refusing(http.StatusBadRequest, `{"error":"invalid_grant"}`))
	store := &fakeStore{cred: credential.Credential{
		Token: "an-old-access-token", Refresh: secret, Expiry: time.Now().Add(-time.Hour),
	}}
	opts := optionsFor(t, server.URL, store, "")

	err := opts.renewIfExpired(t.Context())
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error carries the refresh token: %v", err)
	}
}

// errNoRoom stands in for the keychain refusing an oversized secret, which is
// the realistic way a write fails.
var errNoRoom = &writeError{}

type writeError struct{}

func (*writeError) Error() string { return "the credential does not fit" }

// A renewal that is stored but not marshalled back is a renewal that happened
// twice — once now and once on the next command, because the expiry never
// moved. Guards the field the caller reads rather than the one it writes.
func TestTheExpiryMovesForward(t *testing.T) {
	t.Parallel()

	server, refreshes := identityProvider(t, renewed("a-new-access-token", "a-new-refresh-token"))
	store := &fakeStore{cred: expired()}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err != nil {
		t.Fatalf("first renewal: %v", err)
	}
	if err := opts.renewIfExpired(t.Context()); err != nil {
		t.Fatalf("second call: %v", err)
	}

	if refreshes.Load() != 1 {
		t.Errorf("renewed %d times across two calls — the expiry did not move",
			refreshes.Load())
	}
}

// Belt and braces on the shape of what is stored: a renewal that dropped the
// expiry would leave IsExpired reading a zero time, which means "never
// expires" and quietly disables renewal for good.
func TestARenewedCredentialCarriesAnExpiry(t *testing.T) {
	t.Parallel()

	server, _ := identityProvider(t, renewed("a-new-access-token", "a-new-refresh-token"))
	store := &fakeStore{cred: expired()}
	opts := optionsFor(t, server.URL, store, "")

	if err := opts.renewIfExpired(t.Context()); err != nil {
		t.Fatalf("renewIfExpired: %v", err)
	}

	stored := store.setCalls[0]
	if stored.Expiry.IsZero() {
		t.Fatal("the renewed credential has no expiry, so it will never be renewed again")
	}
	if !stored.Expiry.After(time.Now()) {
		t.Errorf("expiry = %v, want it in the future", stored.Expiry)
	}

	// Serialising through the store must not lose it either.
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "Expiry") && !strings.Contains(string(raw), "expiry") {
		t.Errorf("the expiry did not survive serialisation: %s", raw)
	}
}

// --- SaveLogin -----------------------------------------------------------

// The expiry is what makes renewal happen at all: IsExpired reads a zero time
// as "never expires", so a login stored without one is never renewed and dies
// silently an hour later with no way to notice. A mutation that dropped this
// field survived every other test in this module.
func TestASavedLoginCarriesItsExpiry(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	opts := &Options{
		Saved:       config.Config{Current: "staging", Contexts: map[string]config.Context{"staging": {}}},
		Credentials: store,
	}

	expiry := time.Now().Add(time.Hour).Round(time.Second)
	err := opts.SaveLogin(&auth.Token{
		Access: "an-access-token", Refresh: "a-refresh-token", Expiry: expiry,
	})
	if err != nil {
		t.Fatalf("SaveLogin: %v", err)
	}

	if len(store.setCalls) != 1 {
		t.Fatalf("stored %d credentials, want 1", len(store.setCalls))
	}
	saved := store.setCalls[0]
	if !saved.Expiry.Equal(expiry) {
		t.Errorf("expiry = %v, want %v", saved.Expiry, expiry)
	}
	if saved.IsExpired(time.Now()) {
		t.Error("a login saved with an hour left reads as already expired")
	}
	if saved.Token != "an-access-token" || saved.Refresh != "a-refresh-token" {
		t.Errorf("stored %+v, want both tokens", saved)
	}
	if opts.credential.Token != "an-access-token" {
		t.Error("the login was stored but not held in memory")
	}
}

// Nothing may reach a browser before this is known. Finding out there is
// nowhere to put a credential *after* somebody has approved is the worst place
// to fail, so login checks it first and this is the guard underneath.
func TestSavingWithNoContextIsRefused(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	opts := &Options{Saved: config.Config{}, Credentials: store}

	err := opts.SaveLogin(&auth.Token{Access: "a", Refresh: "r", Expiry: time.Now()})
	if err == nil {
		t.Fatal("a login was saved with no context to save it under")
	}
	if !strings.Contains(err.Error(), "oa context create") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
	if len(store.setCalls) != 0 {
		t.Error("something was written despite there being no context")
	}
}

// A store that refuses the write is a failed login, not a quiet one — the next
// command would otherwise ask for a credential that was never kept.
func TestASaveThatCannotBeWrittenIsAnError(t *testing.T) {
	t.Parallel()

	store := &fakeStore{setErr: errNoRoom}
	opts := &Options{
		Saved:       config.Config{Current: "staging", Contexts: map[string]config.Context{"staging": {}}},
		Credentials: store,
	}

	if err := opts.SaveLogin(&auth.Token{Access: "a", Refresh: "r"}); err == nil {
		t.Fatal("a failed write was reported as a successful login")
	}
	if opts.credential.Token != "" {
		t.Error("the credential was held in memory despite the write failing")
	}
}

// --- resolving a name ----------------------------------------------------

// An empty argument is a shell accident — `oa teams members add "$TEAM" …`
// with TEAM unset. It must not become a request, because an empty name would
// page the whole list to find nothing.
func TestResolvingAnEmptyReferenceNeverAsksTheBrain(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(server.Close)

	api, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	for _, ref := range []string{"", "   ", "\t"} {
		if _, err := ResolveTeam(t.Context(), api, ref); err == nil {
			t.Errorf("ResolveTeam(%q) was accepted", ref)
		}
		if _, err := ResolveMember(t.Context(), api, uuid.New(), ref); err == nil {
			t.Errorf("ResolveMember(%q) was accepted", ref)
		}
	}
	if asked.Load() != 0 {
		t.Errorf("the brain was asked %d times about an empty name", asked.Load())
	}
}

// A cursor that never ends is one server bug away from a command that hangs
// forever. The bound turns it into an error, and the error has to name the
// escape hatch rather than claiming the team does not exist.
func TestResolvingGivesUpOnAnEndlessCursor(t *testing.T) {
	t.Parallel()

	var pages atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	endless := func(w http.ResponseWriter, _ *http.Request) {
		pages.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"next_cursor":"always-more"}`))
	}
	mux.HandleFunc("GET /teams", endless)
	mux.HandleFunc("GET /teams/{id}/members", endless)

	api, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = ResolveTeam(t.Context(), api, "platform")
	if err == nil {
		t.Fatal("an endless cursor was followed to a result")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("the error does not offer the way out: %v", err)
	}
	if got := pages.Load(); got != maxLookupPages {
		t.Errorf("read %d pages, want the bound of %d", got, maxLookupPages)
	}

	pages.Store(0)
	if _, err := ResolveMember(t.Context(), api, uuid.New(), "alice"); err == nil {
		t.Fatal("an endless cursor was followed to a member")
	}
	if got := pages.Load(); got != maxLookupPages {
		t.Errorf("read %d member pages, want the bound of %d", got, maxLookupPages)
	}
}

// A name that matches nothing must say so in terms of the name. "not a uuid"
// describes an argument the person never meant to type.
func TestAnUnresolvableNameIsReportedAsAName(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	empty := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}
	mux.HandleFunc("GET /teams", empty)
	mux.HandleFunc("GET /teams/{id}/members", empty)

	api, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := ResolveTeam(t.Context(), api, "payments"); err == nil ||
		!strings.Contains(err.Error(), "payments") {
		t.Errorf("ResolveTeam error = %v, want it to name what was typed", err)
	}
	if _, err := ResolveMember(t.Context(), api, uuid.New(), "stranger"); err == nil ||
		!strings.Contains(err.Error(), "stranger") {
		t.Errorf("ResolveMember error = %v, want it to name what was typed", err)
	}
}

// A uuid is used as given, so a script passing ids pays no round trip at all.
func TestResolvingAnIDCostsNoRequest(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		asked.Add(1)
	}))
	t.Cleanup(server.Close)

	api, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	want := uuid.New()
	got, err := ResolveTeam(t.Context(), api, want.String())
	if err != nil || got != want {
		t.Errorf("ResolveTeam = %v, %v; want the id unchanged", got, err)
	}
	got, err = ResolveMember(t.Context(), api, uuid.New(), want.String())
	if err != nil || got != want {
		t.Errorf("ResolveMember = %v, %v; want the id unchanged", got, err)
	}
	if asked.Load() != 0 {
		t.Errorf("an id cost %d requests", asked.Load())
	}
}

// NewClient's error branch is unreachable today and the handling stays anyway.
// oapi-codegen's constructor stores the address without parsing it, so every
// one of these — including the empty string — builds a client and fails later
// at the transport.
//
// This test exists to fail if that ever changes: the branch would become
// reachable, and the message it produces would suddenly matter. Probed, not
// assumed.
func TestNewClientAcceptsAnyAddressToday(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"://not a url", "", "   ", "%%%", "http://[::1", "not a url at all",
	} {
		if _, err := NewClient(address); err != nil {
			t.Errorf("NewClient(%q) now fails with %v — the error branch is reachable, "+
				"so assert on the message it produces", address, err)
		}
	}
}

// A brain that says nothing about an identity provider cannot be logged in to,
// and the message has to name the server — somebody with three contexts needs
// to know which one is misconfigured.
func TestAProviderlessBrainNamesTheServer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"environment":"development","dev_token_accepted":true}`))
	}))
	t.Cleanup(server.Close)

	opts := optionsFor(t, server.URL, &fakeStore{}, "")

	_, err := opts.Provider(t.Context())
	if err == nil {
		t.Fatal("a brain with no identity provider produced a provider")
	}
	if !strings.Contains(err.Error(), server.URL) {
		t.Errorf("the error does not name the server: %v", err)
	}
	if !strings.Contains(err.Error(), "identity provider") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// A brain that answers something other than 200 is a different failure from
// one with no provider configured, and must not be reported as the latter.
func TestAnUnreadableAuthConfigIsReportedAsItself(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	opts := optionsFor(t, server.URL, &fakeStore{}, "")

	_, err := opts.Provider(t.Context())
	if err == nil {
		t.Fatal("a 500 produced a provider")
	}
	if strings.Contains(err.Error(), "no identity provider") {
		t.Errorf("a server error was reported as a missing provider: %v", err)
	}
}
