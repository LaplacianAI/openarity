package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// deviceProvider mounts discovery plus whatever the test wants at the device
// and token endpoints. Discovery is real rather than stubbed out because the
// endpoints StartDevice uses are the ones NewProvider found.
func deviceProvider(t *testing.T, device, token http.HandlerFunc) *Provider {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fullDocument(server.URL)))
	})
	if device != nil {
		mux.HandleFunc("/device/", device)
	}
	if token != nil {
		mux.HandleFunc("/token/", token)
	}

	p, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

type reply struct {
	status int
	body   string
}

// script answers each poll with the next reply and repeats the last one
// forever, recording the form and the moment of every call. The moments are
// what make slow_down observable — nothing in the return value says the gap
// changed.
type script struct {
	mu      sync.Mutex
	replies []reply
	forms   []url.Values
	at      []time.Time
}

func newScript(replies ...reply) *script {
	return &script{replies: replies}
}

func (s *script) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		s.mu.Lock()
		next := s.replies[min(len(s.forms), len(s.replies)-1)]
		s.forms = append(s.forms, r.Form)
		s.at = append(s.at, time.Now())
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.status)
		_, _ = w.Write([]byte(next.body))
	}
}

func (s *script) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.forms)
}

func (s *script) form(n int) url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forms[n]
}

func (s *script) moment(n int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at[n]
}

var pending = reply{http.StatusBadRequest, `{"error":"authorization_pending"}`}

// polling builds what StartDevice would have returned. The fields are
// unexported, so a test in this package is the only thing that can hand
// WaitForToken an interval short enough to run in.
func polling(interval time.Duration) *DeviceAuth {
	return &DeviceAuth{deviceCode: "a-device-code", interval: interval}
}

func TestStartDeviceReturnsWhatAPersonNeedsToSee(t *testing.T) {
	t.Parallel()

	p := deviceProvider(t, jsonHandler(http.StatusOK, `{
	  "device_code": "a-device-code",
	  "user_code": "WXYZ-ABCD",
	  "verification_uri": "https://auth.example.com/device",
	  "verification_uri_complete": "https://auth.example.com/device?code=WXYZ-ABCD",
	  "interval": 3,
	  "expires_in": 600
	}`), nil)

	got, err := p.StartDevice(t.Context())
	if err != nil {
		t.Fatalf("StartDevice: %v", err)
	}

	if got.UserCode != "WXYZ-ABCD" {
		t.Errorf("UserCode = %q", got.UserCode)
	}
	if got.VerificationURI != "https://auth.example.com/device" {
		t.Errorf("VerificationURI = %q", got.VerificationURI)
	}
	if got.Complete != "https://auth.example.com/device?code=WXYZ-ABCD" {
		t.Errorf("Complete = %q", got.Complete)
	}
	if got.interval != 3*time.Second {
		t.Errorf("interval = %v, want the provider's 3s", got.interval)
	}
	if got.ExpiresIn != 10*time.Minute {
		t.Errorf("ExpiresIn = %v, want the provider's 600s", got.ExpiresIn)
	}
	if got.deviceCode != "a-device-code" {
		t.Errorf("deviceCode = %q — polling would ask about nothing", got.deviceCode)
	}
}

// The scope is the whole reason a login survives past its first hour. Dropping
// offline_access still logs in, so nothing fails until the access token dies
// and there is nothing to renew from.
func TestStartDeviceAsksForOfflineAccess(t *testing.T) {
	t.Parallel()

	asked := newScript(reply{http.StatusOK, `{
	  "device_code": "d", "user_code": "u", "verification_uri": "https://example.com/device"
	}`})
	p := deviceProvider(t, asked.handler(), nil)

	if _, err := p.StartDevice(t.Context()); err != nil {
		t.Fatalf("StartDevice: %v", err)
	}

	scope := asked.form(0).Get("scope")
	for _, want := range []string{"openid", "offline_access"} {
		if !strings.Contains(scope, want) {
			t.Errorf("scope = %q, missing %q", scope, want)
		}
	}
	if got := asked.form(0).Get("client_id"); got != "a-client" {
		t.Errorf("client_id = %q — the provider cannot tell who is asking", got)
	}
}

// A provider that cannot do the device flow still refreshes, so this has to
// fail here rather than in the constructor. The message names the issuer
// because the usual cause is being pointed at the wrong one.
func TestAProviderWithNoDeviceEndpointCannotStartOne(t *testing.T) {
	t.Parallel()

	server := stubProvider(t, func(issuer string) string {
		return `{"issuer": "` + issuer + `", "token_endpoint": "` + issuer + `/token/"}`
	})

	p, err := NewProvider(t.Context(), server.Client(), server.URL, "a-client")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	_, err = p.StartDevice(t.Context())
	if err == nil {
		t.Fatal("a provider with no device endpoint started a device flow")
	}
	if !strings.Contains(err.Error(), "device flow") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
	if !strings.Contains(err.Error(), server.URL) {
		t.Errorf("the error does not name the issuer: %v", err)
	}
}

// interval and expires_in are both optional in RFC 8628. Falling through to
// zero would poll in a tight loop and give the caller a deadline that has
// already passed.
func TestStartDeviceFallsBackWhenTheProviderNamesNoTimings(t *testing.T) {
	t.Parallel()

	p := deviceProvider(t, jsonHandler(http.StatusOK, `{
	  "device_code": "d", "user_code": "u", "verification_uri": "https://example.com/device"
	}`), nil)

	got, err := p.StartDevice(t.Context())
	if err != nil {
		t.Fatalf("StartDevice: %v", err)
	}
	if got.interval != defaultInterval {
		t.Errorf("interval = %v, want the default %v", got.interval, defaultInterval)
	}
	if got.ExpiresIn != defaultLifetime {
		t.Errorf("ExpiresIn = %v, want the default %v", got.ExpiresIn, defaultLifetime)
	}
}

// Each of these leaves the login unable to proceed, and without the check it
// proceeds anyway: a blank code printed at a blank address, then polling that
// never succeeds.
func TestADeviceResponseMissingAnythingEssentialIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
	}{
		{"no device code", `{"user_code": "u", "verification_uri": "https://example.com/d"}`},
		{"no user code", `{"device_code": "d", "verification_uri": "https://example.com/d"}`},
		{"no address", `{"device_code": "d", "user_code": "u"}`},
		{"nothing at all", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := deviceProvider(t, jsonHandler(http.StatusOK, tc.body), nil)

			if _, err := p.StartDevice(t.Context()); err == nil {
				t.Fatalf("a device authorization with %s was accepted", tc.name)
			}
		})
	}
}

// Unlike the polling loop, there is no "keep waiting" here — a named error at
// the device endpoint is the end of the login, and its description is the only
// thing that explains why.
func TestAnOAuthErrorStartingTheDeviceFlowEndsIt(t *testing.T) {
	t.Parallel()

	p := deviceProvider(t, jsonHandler(http.StatusBadRequest,
		`{"error":"invalid_client","error_description":"unknown client openarity"}`), nil)

	_, err := p.StartDevice(t.Context())
	if err == nil {
		t.Fatal("a refused device authorization was accepted")
	}
	if !strings.Contains(err.Error(), "unknown client openarity") {
		t.Errorf("the error drops the provider's description: %v", err)
	}
}

func TestWaitForTokenReturnsTheTokenOnceApproved(t *testing.T) {
	t.Parallel()

	polls := newScript(pending, pending,
		reply{http.StatusOK, `{"access_token":"an-access","refresh_token":"a-refresh","expires_in":3600}`})
	p := deviceProvider(t, nil, polls.handler())

	token, err := p.WaitForToken(t.Context(), polling(time.Millisecond))
	if err != nil {
		t.Fatalf("WaitForToken: %v", err)
	}
	if token.Access != "an-access" || token.Refresh != "a-refresh" {
		t.Errorf("token = %+v", token)
	}
	if polls.calls() != 3 {
		t.Errorf("polled %d times, want 3 — a 400 carrying authorization_pending is not a failure", polls.calls())
	}
}

// The device code is the only thing tying this poll to the code the person is
// looking at, and the grant type is what tells the provider which flow this is.
func TestWaitForTokenSendsTheDeviceGrant(t *testing.T) {
	t.Parallel()

	polls := newScript(reply{http.StatusOK, `{"access_token":"a","expires_in":60}`})
	p := deviceProvider(t, nil, polls.handler())

	if _, err := p.WaitForToken(t.Context(), polling(time.Millisecond)); err != nil {
		t.Fatalf("WaitForToken: %v", err)
	}

	form := polls.form(0)
	for field, want := range map[string]string{
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": "a-device-code",
		"client_id":   "a-client",
	} {
		if got := form.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

// Not parallel: it swaps a package var, and every polling test reads it.
//
// slow_down is the one instruction with no visible result — the token still
// arrives either way, so a loop that ignores it looks correct and gets rate
// limited on a real provider. The third gap is what proves the widening is
// permanent rather than applied for one tick.
func TestSlowDownWidensTheGapAndKeepsItWide(t *testing.T) {
	original := slowDownStep
	slowDownStep = 40 * time.Millisecond
	t.Cleanup(func() { slowDownStep = original })

	const interval = 10 * time.Millisecond

	polls := newScript(
		reply{http.StatusBadRequest, `{"error":"slow_down"}`},
		pending,
		reply{http.StatusOK, `{"access_token":"a","expires_in":60}`},
	)
	p := deviceProvider(t, nil, polls.handler())

	started := time.Now()
	if _, err := p.WaitForToken(t.Context(), polling(interval)); err != nil {
		t.Fatalf("WaitForToken: %v", err)
	}
	if polls.calls() != 3 {
		t.Fatalf("polled %d times, want 3", polls.calls())
	}

	first := polls.moment(0).Sub(started)
	second := polls.moment(1).Sub(polls.moment(0))
	third := polls.moment(2).Sub(polls.moment(1))

	if second < interval+slowDownStep {
		t.Errorf("the gap after slow_down was %v, want at least %v", second, interval+slowDownStep)
	}
	if third < interval+slowDownStep {
		t.Errorf("the gap narrowed again to %v — slow_down was applied for one tick only", third)
	}
	if second <= first {
		t.Errorf("gaps were %v then %v — slow_down changed nothing", first, second)
	}
}

// Someone pressed cancel. Suggesting they try again is a loop they cannot
// escape, so the caller gets a sentinel rather than prose to match on.
func TestACancelledLoginIsASentinelAndNotAnInvitation(t *testing.T) {
	t.Parallel()

	polls := newScript(pending, reply{http.StatusBadRequest, `{"error":"access_denied"}`})
	p := deviceProvider(t, nil, polls.handler())

	_, err := p.WaitForToken(t.Context(), polling(time.Millisecond))
	if !errors.Is(err, ErrLoginRefused) {
		t.Fatalf("err = %v, want ErrLoginRefused", err)
	}
	if strings.Contains(err.Error(), "again") {
		t.Errorf("a refusal offers a retry: %v", err)
	}
}

// The code timed out. This one is worth retrying, and saying so is the whole
// difference from the refusal above.
func TestAnExpiredCodeSaysToStartAgain(t *testing.T) {
	t.Parallel()

	polls := newScript(reply{http.StatusBadRequest, `{"error":"expired_token"}`})
	p := deviceProvider(t, nil, polls.handler())

	_, err := p.WaitForToken(t.Context(), polling(time.Millisecond))
	if err == nil {
		t.Fatal("an expired device code was polled past")
	}
	if errors.Is(err, ErrLoginRefused) {
		t.Error("a timeout was reported as a refusal")
	}
	if !strings.Contains(err.Error(), "oa login") {
		t.Errorf("the error does not say what to do: %v", err)
	}
}

// The bug the switch exists to prevent. A loop that treats an unrecognised
// code as "keep waiting" hangs forever, prints nothing, and reads as a network
// problem — so this asserts the poll count too, not only the error.
func TestAnUnrecognisedErrorEndsTheLoopRatherThanContinuingIt(t *testing.T) {
	t.Parallel()

	polls := newScript(pending, reply{
		http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"device code already redeemed"}`,
	})
	p := deviceProvider(t, nil, polls.handler())

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)

	_, err := p.WaitForToken(ctx, polling(time.Millisecond))
	if err == nil {
		t.Fatal("an unrecognised OAuth error was accepted")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("the loop kept polling on an unrecognised error until the deadline")
	}
	if !strings.Contains(err.Error(), "device code already redeemed") {
		t.Errorf("the error drops the only description of what went wrong: %v", err)
	}
	if polls.calls() != 2 {
		t.Errorf("polled %d times, want 2 — the loop continued past an error it does not know", polls.calls())
	}
}

// How long a person gets is the caller's to decide, so a cancelled context has
// to end the wait rather than being noticed one interval later.
//
// The interval is deliberately far longer than the deadline. A plain sleep
// passes every other assertion here — the context reaches the HTTP request
// too, so the loop still stops — and the only thing that separates the two is
// how long it takes. Ctrl-C on a five second interval has to return now, not
// in five seconds.
func TestACancelledContextStopsTheWaitImmediately(t *testing.T) {
	t.Parallel()

	polls := newScript(pending)
	p := deviceProvider(t, nil, polls.handler())

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	t.Cleanup(cancel)

	started := time.Now()
	_, err := p.WaitForToken(ctx, polling(time.Minute))
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("the wait outlived its context")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's own cause", err)
	}
	if elapsed > time.Second {
		t.Errorf("took %v to notice a 20ms deadline — the wait is not interruptible", elapsed)
	}
	if polls.calls() != 0 {
		t.Errorf("polled %d times after the context was already done", polls.calls())
	}
}

// Polling the instant the code is printed buys one guaranteed
// authorization_pending before anyone could possibly have approved it, and
// some providers count it towards the limit that produces slow_down.
func TestTheFirstPollWaitsForTheInterval(t *testing.T) {
	t.Parallel()

	polls := newScript(reply{http.StatusOK, `{"access_token":"a","expires_in":60}`})
	p := deviceProvider(t, nil, polls.handler())

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	t.Cleanup(cancel)

	if _, err := p.WaitForToken(ctx, polling(time.Minute)); err == nil {
		t.Fatal("a token arrived from a poll that should not have happened yet")
	}
	if polls.calls() != 0 {
		t.Errorf("polled %d times before the first interval elapsed", polls.calls())
	}
}
