package browser

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const secret = "sk-TEST-ONLY"

// What New refuses is what the settings would never save: a hosted provider
// at a url, a cdp browser nowhere, a hosted one with no key.
func TestNewRefusesABrowserItCannotReach(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		p        Provider
		url, key string
		want     string
	}{
		{"none", None, "", "", "no browser"},
		{"unknown", "chrome", "", "", "not a browser provider"},
		{"hosted at a url", Browserbase, "https://bb.example", secret, "hosted only"},
		{"cdp nowhere", CDP, "", "", "needs the url"},
		{"hosted with no key", Kernel, "", "", "needs a key"},
		{"browserless cloud with no key", Browserless, "", "", "needs a key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tc.p, tc.url, tc.key, http.DefaultClient)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("New = %v, want an error saying %q", err, tc.want)
			}
		})
	}
}

// A self-hosted browser may have no key, and cdp never needs one.
func TestASelfHostedBrowserNeedsNoKey(t *testing.T) {
	t.Parallel()

	for _, p := range []Provider{Browserless, Steel, Lightpanda, CDP} {
		if _, err := New(p, "http://browser.internal:3000", "", http.DefaultClient); err != nil {
			t.Errorf("New(%s, a url, no key) = %v", p, err)
		}
	}
}

// A browser at one address is reached at it, as a WebSocket, its key as
// ?token=; a hosted one with no url at its provider's own.
func TestABrowserAtOneAddress(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		p        Provider
		url, key string
		want     string
	}{
		{"browserless cloud", Browserless, "", secret, "wss://production-sfo.browserless.io?token=sk-TEST-ONLY"},
		{"browserless self-hosted", Browserless, "http://browserless:3000", "tok", "ws://browserless:3000?token=tok"},
		{"browserless with no token", Browserless, "ws://browserless:3000", "", "ws://browserless:3000"},
		// The overlay's URL: its proxy flags survive the token, as Chrome
		// reads them, <-loopback> included.
		{
			"browserless with its proxy flags", Browserless,
			"ws://browserless:3000?--proxy-server=http://worker:3128&--proxy-bypass-list=%3C-loopback%3E", "tok",
			"ws://browserless:3000?--proxy-bypass-list=%3C-loopback%3E&--proxy-server=http%3A%2F%2Fworker%3A3128&token=tok",
		},
		{"lightpanda cloud", Lightpanda, "", "lp", "wss://euwest.cloud.lightpanda.io/ws?token=lp"},
		{"lightpanda self-hosted", Lightpanda, "ws://lightpanda:9222", "", "ws://lightpanda:9222"},
		{"cdp over https", CDP, "https://chrome.example/devtools", "", "wss://chrome.example/devtools"},
		{"a token that needs escaping", CDP, "ws://chrome:9222", "a&b=c", "ws://chrome:9222?token=a%26b%3Dc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, err := New(tc.p, tc.url, tc.key, http.DefaultClient)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			s, err := o.Open(t.Context())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if s.URL != tc.want || s.Release != nil {
				t.Errorf("Open = %q (release %v), want %q and nothing to release", s.URL, s.Release != nil, tc.want)
			}
		})
	}
}

func TestABrowserURLMustBeOneItCanDial(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"ftp://chrome:21", "chrome:9222", "://"} {
		o, err := New(CDP, raw, "", http.DefaultClient)
		if err != nil {
			continue
		}
		if _, err := o.Open(t.Context()); err == nil {
			t.Errorf("Open(%q) worked, want it refused", raw)
		}
	}
}

// Bright Data is its zone's user and password, in the address itself.
func TestBrightDataIsReachedAsItsZone(t *testing.T) {
	t.Parallel()

	o, err := New(BrightData, "", "brd-customer-1-zone-z:p@ss/word", http.DefaultClient)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s, err := o.Open(t.Context())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if want := "wss://brd-customer-1-zone-z:p%40ss%2Fword@brd.superproxy.io:9222"; s.URL != want {
		t.Errorf("Open = %q, want %q", s.URL, want)
	}

	for _, key := range []string{"no-colon", ":pass", "user:"} {
		if _, err := New(BrightData, "", key, http.DefaultClient); err == nil || strings.Contains(err.Error(), key) {
			t.Errorf("New(brightdata, %q) = %v, want refused without echoing the key", key, err)
		}
	}
}

// fakeAPI is a session provider: it answers its create call with created,
// and records every request.
type fakeAPI struct {
	mu      sync.Mutex
	got     []string
	headers []http.Header
	bodies  []string
	created string
	status  int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, r.Method+" "+r.URL.Path)
	f.headers = append(f.headers, r.Header.Clone())
	f.bodies = append(f.bodies, string(body))
	first := len(f.got) == 1
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if first {
		_, _ = io.WriteString(w, f.created)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func serve(t *testing.T, f *fakeAPI) string {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL
}

// Each session provider makes a session with its key on the call, as it
// wants it; hands back the address its answer names; and ends that session
// when the browser is let go.
func TestASessionProviderMakesAndEndsASession(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		p                Provider
		created          string
		create, release  string
		keyHeader, keyAs string
		createBody       string
		releaseBody      string
	}{
		{
			Steel, `{"id":"s1","websocketUrl":"wss://connect.steel.dev?sessionId=s1&apiKey=x"}`,
			"POST /v1/sessions", "POST /v1/sessions/s1/release", "Steel-Api-Key", secret, "{}", "{}",
		},
		{
			Browserbase, `{"id":"b1","connectUrl":"wss://connect.browserbase.com?signingKey=x"}`,
			"POST /v1/sessions", "POST /v1/sessions/b1", "X-Bb-Api-Key", secret, "{}", `{"status":"REQUEST_RELEASE"}`,
		},
		{
			Hyperbrowser, `{"id":"h1","wsEndpoint":"wss://connect.hyperbrowser.ai/?token=x"}`,
			"POST /api/session", "PUT /api/session/h1/stop", "X-Api-Key", secret, "{}", "",
		},
		{
			Anchor, `{"data":{"id":"a1","cdp_url":"wss://connect.anchorbrowser.io?sessionId=a1"}}`,
			"POST /v1/sessions", "DELETE /v1/sessions/a1", "Anchor-Api-Key", secret, "{}", "",
		},
		{
			Kernel, `{"session_id":"k1","cdp_ws_url":"wss://k1.onkernel.com/cdp?jwt=x"}`,
			"POST /browsers", "DELETE /browsers/k1", "Authorization", "Bearer " + secret,
			`{"headless":true,"timeout_seconds":300}`, "",
		},
	} {
		t.Run(string(tc.p), func(t *testing.T) {
			t.Parallel()
			f := &fakeAPI{created: tc.created}
			o, err := open(tc.p, serve(t, f), secret, http.DefaultClient)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			s, err := o.Open(t.Context())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !strings.Contains(tc.created, s.URL) || s.URL == "" {
				t.Errorf("Open = %q, want the address in %s", s.URL, tc.created)
			}
			if s.Release == nil {
				t.Fatal("a session to end, and no Release")
			}
			if err := s.Release(t.Context()); err != nil {
				t.Fatalf("Release: %v", err)
			}

			if len(f.got) != 2 || f.got[0] != tc.create || f.got[1] != tc.release {
				t.Errorf("calls = %v, want [%s %s]", f.got, tc.create, tc.release)
			}
			for i, h := range f.headers {
				if got := h.Get(tc.keyHeader); got != tc.keyAs {
					t.Errorf("call %d: %s = %q, want %q", i, tc.keyHeader, got, tc.keyAs)
				}
			}
			if f.bodies[0] != tc.createBody || f.bodies[1] != tc.releaseBody {
				t.Errorf("bodies = %q, want [%q %q]", f.bodies, tc.createBody, tc.releaseBody)
			}
		})
	}
}

// A provider that refuses, or answers with no address, is an error that
// names the provider and never the key.
func TestASessionThatCannotBeMadeSaysSo(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		f    *fakeAPI
		says string
	}{
		"refused":       {&fakeAPI{status: http.StatusUnauthorized}, "answered 401"},
		"no address":    {&fakeAPI{created: `{"id":"b1"}`}, "without saying where"},
		"no id":         {&fakeAPI{created: `{"connectUrl":"wss://x"}`}, "without saying where"},
		"not json":      {&fakeAPI{created: `<html>`}, "without saying where"},
		"too much text": {&fakeAPI{created: `{"id":"b1","connectUrl":"` + strings.Repeat("a", maxBody) + `"}`}, "more than 1 MiB"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o, _ := open(Browserbase, serve(t, tc.f), secret, http.DefaultClient)
			_, err := o.Open(t.Context())
			if err == nil || !strings.Contains(err.Error(), "browserbase") || !strings.Contains(err.Error(), tc.says) ||
				strings.Contains(err.Error(), secret) {
				t.Errorf("Open = %v, want an error naming browserbase, saying %q, and not the key", err, tc.says)
			}
		})
	}
}

// A release the provider refuses is an error, so the caller can log a
// session left running.
func TestAReleaseThatFailsIsAnError(t *testing.T) {
	t.Parallel()

	f := &fakeAPI{created: `{"session_id":"k1","cdp_ws_url":"wss://k1"}`}
	o, _ := open(Kernel, serve(t, f), secret, http.DefaultClient)
	s, err := o.Open(t.Context())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f.status = http.StatusInternalServerError
	if err := s.Release(t.Context()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("Release = %v, want the 500", err)
	}
}

// A self-hosted Steel names its address as localhost, whatever it is reached
// by; the run reaches it where Steel was reached. Hosted Steel's is used as
// it is.
func TestASelfHostedSteelIsReachedWhereItWasFound(t *testing.T) {
	t.Parallel()

	f := &fakeAPI{created: `{"id":"s1","websocketUrl":"ws://localhost:3000/"}`}
	base := serve(t, f)
	o, err := New(Steel, base, "", http.DefaultClient)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s, err := o.Open(t.Context())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if want := "ws://127.0.0.1:3000/"; s.URL != want {
		t.Errorf("Open = %q, want %q", s.URL, want)
	}
	if h, sent := f.headers[0]["Steel-Api-Key"]; sent {
		t.Errorf("a Steel with no key sent the header, as %q", h)
	}

	for cdp, want := range map[string]string{
		"ws://0.0.0.0:3000/":         "ws://steel.internal:3000/",
		"ws://[::1]/":                "ws://steel.internal:8443/",
		"ws://steel.public:3000/x":   "ws://steel.public:3000/x",
		"wss://connect.steel.dev/?a": "wss://connect.steel.dev/?a",
	} {
		got, err := reachedAt(cdp, "https://steel.internal:8443")
		if err != nil || got != want {
			t.Errorf("reachedAt(%q) = %q, %v; want %q", cdp, got, err, want)
		}
	}
}

func TestEveryProviderIsKnownAndOpens(t *testing.T) {
	t.Parallel()

	for _, p := range Providers {
		if !p.Known() {
			t.Errorf("%s is listed and not known", p)
		}
		_, err := open(p, "https://x.example", "u:p", http.DefaultClient)
		if p == None {
			if !errors.Is(err, ErrNoProvider) {
				t.Errorf("open(none) = %v, want ErrNoProvider", err)
			}
			continue
		}
		if err != nil {
			t.Errorf("open(%s) = %v", p, err)
		}
	}
}
