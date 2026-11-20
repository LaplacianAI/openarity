package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/coder/websocket"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

// fakeCDP is a browser that answers every command with an empty result, but
// for the few that make a tab, and refuses those listed in refuse. Enough of
// the protocol for chromedp to open a tab in it.
type fakeCDP struct {
	refuse map[string]bool
	// silent are methods it never answers.
	silent map[string]bool
	// loadsCanary is a browser that says it pauses requests and does not:
	// it tries the canary as it would anywhere. fetchesUnpaused pauses a
	// navigation but not a page's own fetch.
	loadsCanary     bool
	fetchesUnpaused bool
	// version is what /json/version says; "" is a 404, as Browserless's is.
	version string
	// send is one message the browser sends first, unasked.
	send []byte

	mu     sync.Mutex
	asked  []string
	dialed []string
}

func (f *fakeCDP) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			if f.version == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(f.version))
			return
		}
		f.mu.Lock()
		f.dialed = append(f.dialed, r.URL.RequestURI())
		f.mu.Unlock()
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ws.SetReadLimit(maxMessage)
		if f.send != nil {
			_ = ws.Write(r.Context(), websocket.MessageText, f.send)
		}
		for {
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var m struct {
				ID        int64  `json:"id"`
				Method    string `json:"method"`
				SessionID string `json:"sessionId"`
				Params    struct {
					URL        string `json:"url"`
					Expression string `json:"expression"`
				} `json:"params"`
			}
			_ = json.Unmarshal(data, &m)
			f.mu.Lock()
			f.asked = append(f.asked, m.Method)
			f.mu.Unlock()
			if f.silent[m.Method] {
				continue
			}
			reply := map[string]any{"id": m.ID, "result": map[string]any{}}
			switch {
			case m.Method == "Page.navigate" && strings.Contains(m.Params.URL, canaryHost) && !f.loadsCanary:
				reply["result"] = map[string]any{"frameId": "T1", "errorText": "net::ERR_BLOCKED_BY_CLIENT"}
			case m.Method == "Page.navigate" && strings.Contains(m.Params.URL, canaryHost):
				// Not paused, so tried: .invalid names nothing.
				reply["result"] = map[string]any{"frameId": "T1", "errorText": "net::ERR_NAME_NOT_RESOLVED"}
			case m.Method == "Target.createBrowserContext":
				reply["result"] = map[string]any{"browserContextId": "B1"}
			case m.Method == "Page.addScriptToEvaluateOnNewDocument":
				reply["result"] = map[string]any{"identifier": "1"}
			case f.refuse[m.Method]:
				reply = map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported"}}
			case m.Method == "Target.createTarget":
				reply["result"] = map[string]any{"targetId": "T1"}
			case m.Method == "Target.attachToTarget":
				reply["result"] = map[string]any{"sessionId": "S1"}
			case m.Method == "Runtime.evaluate":
				if strings.Contains(m.Params.Expression, canaryHost) && !f.fetchesUnpaused && !f.loadsCanary {
					// The page's fetch of the canary, paused as a browser that
					// pauses requests pauses it.
					ev, _ := json.Marshal(map[string]any{"method": "Fetch.requestPaused", "sessionId": m.SessionID, "params": map[string]any{
						"requestId": "canary", "request": map[string]any{"url": "http://" + canaryHost + "/sub", "method": "GET", "headers": map[string]any{}},
						"frameId": "T1", "resourceType": "Fetch",
					}})
					if err := ws.Write(r.Context(), websocket.MessageText, ev); err != nil {
						return
					}
				}
				reply["result"] = map[string]any{"result": map[string]any{"type": "string", "value": "complete"}}
			}
			if m.SessionID != "" {
				reply["sessionId"] = m.SessionID
			}
			out, _ := json.Marshal(reply)
			if err := ws.Write(r.Context(), websocket.MessageText, out); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeCDP) wasAsked(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.asked {
		if m == method {
			return true
		}
	}
	return false
}

func anyHost(context.Context, string) error { return nil }

func startOn(t *testing.T, address string, required bool) (*Page, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	return Start(ctx, Session{URL: address}, http.DefaultClient, anyHost, required)
}

// A browser of the team's own that will not have its requests checked is not
// used: nothing else stands between its pages and the team's network.
func TestABrowserThatRefusesTheCheckIsNotUsedWhenItMustBe(t *testing.T) {
	f := &fakeCDP{refuse: map[string]bool{"Fetch.enable": true}}
	srv := f.serve(t)

	p, err := startOn(t, srv.URL, true)
	if !errors.Is(err, ErrNoGuard) {
		p.Close()
		t.Fatalf("Start = %v, want ErrNoGuard", err)
	}
	if !f.wasAsked("Fetch.enable") {
		t.Error("the check was never asked for")
	}
}

// A hosted browser sits on its provider's network, so one that refuses the
// check is used all the same, and says so.
func TestAHostedBrowserThatRefusesTheCheckIsUsedAndSaysSo(t *testing.T) {
	f := &fakeCDP{refuse: map[string]bool{"Fetch.enable": true}}
	srv := f.serve(t)

	p, err := startOn(t, srv.URL, false)
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	defer p.Close()
	if !p.Unguarded() {
		t.Error("an unguarded browser is not said to be")
	}
}

// A browser of the team's own that says it pauses its requests and loads the
// canary anyway is not pausing them.
func TestABrowserThatLoadsTheCanaryIsNotUsed(t *testing.T) {
	f := &fakeCDP{loadsCanary: true}
	srv := f.serve(t)

	if p, err := startOn(t, srv.URL, true); !errors.Is(err, ErrNoGuard) {
		p.Close()
		t.Fatalf("Start = %v, want ErrNoGuard", err)
	}
	if !f.wasAsked("Page.navigate") {
		t.Error("the canary was never tried")
	}
}

// A browser that pauses a navigation but not the page's own fetch is not
// guarding what a page asks for, whatever it does with the address bar.
func TestABrowserThatDoesNotPauseAPagesFetchIsNotUsed(t *testing.T) {
	f := &fakeCDP{fetchesUnpaused: true}
	srv := f.serve(t)

	if p, err := startOn(t, srv.URL, true); !errors.Is(err, ErrNoGuard) {
		p.Close()
		t.Fatalf("Start = %v, want ErrNoGuard", err)
	}
}

// The team's own browser gets a browser context for the tab, so nothing a
// run leaves behind is there for the next; a hosted one makes its own.
func TestOnlyASelfHostedBrowserGetsAContextOfItsOwn(t *testing.T) {
	for _, required := range []bool{true, false} {
		f := &fakeCDP{}
		srv := f.serve(t)
		p, err := startOn(t, srv.URL, required)
		if err != nil {
			t.Fatalf("Start = %v", err)
		}
		p.Close()
		if got := f.wasAsked("Target.createBrowserContext"); got != required {
			t.Errorf("required %v: a browser context made %v", required, got)
		}
	}
}

// A browser that takes the connection and never answers is a failure in
// bounded time, not a refusal: a hosted one must not be used unguarded
// because it was slow.
func TestABrowserThatNeverAnswersFailsInTime(t *testing.T) {
	was := startTimeout
	startTimeout = 300 * time.Millisecond
	t.Cleanup(func() { startTimeout = was })

	for _, required := range []bool{true, false} {
		f := &fakeCDP{silent: map[string]bool{"Fetch.enable": true}}
		srv := f.serve(t)
		began := time.Now()
		p, err := startOn(t, srv.URL, required)
		if err == nil {
			p.Close()
			t.Fatalf("required %v: a browser that never answered was used", required)
		}
		if errors.Is(err, ErrNoGuard) || time.Since(began) > 5*time.Second {
			t.Errorf("required %v: Start = %v after %s, want a failure in time", required, err, time.Since(began))
		}
	}
}

// Close lets the session go once, however often it is called.
func TestClosingTwiceReleasesOnce(t *testing.T) {
	f := &fakeCDP{}
	srv := f.serve(t)
	var released atomic.Int32
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	p, err := Start(ctx, Session{URL: srv.URL, Release: func(context.Context) error {
		released.Add(1)
		return nil
	}}, http.DefaultClient, anyHost, true)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	p.Close()
	if n := released.Load(); n != 1 {
		t.Errorf("released %d times, want once", n)
	}
}

// Chrome serves its websocket at a path /json/version names; only the path is
// taken, so a browser cannot send the dial to another host.
func TestABrowsersRootIsDialedAtThePathItNames(t *testing.T) {
	f := &fakeCDP{version: `{"webSocketDebuggerUrl":"ws://elsewhere.example:9222/devtools/browser/abc"}`}
	srv := f.serve(t)

	p, err := startOn(t, srv.URL+"/?token=t", true)
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	p.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dialed) == 0 || f.dialed[0] != "/devtools/browser/abc?token=t" {
		t.Errorf("dialed %v, want the named path on this host, with the key kept", f.dialed)
	}
}

// A browser that names no path, or none at all, is dialed where it is.
func TestABrowserThatNamesNoPathIsDialedWhereItIs(t *testing.T) {
	for name, version := range map[string]string{
		"no /json/version":  "",
		"its root":          `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/"}`,
		"not json":          `<html>`,
		"an address at all": `{"webSocketDebuggerUrl":"::"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeCDP{version: version}
			srv := f.serve(t)
			p, err := startOn(t, srv.URL, true)
			if err != nil {
				t.Fatalf("Start = %v", err)
			}
			p.Close()
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.dialed) == 0 || f.dialed[0] != "/" {
				t.Errorf("dialed %v, want /", f.dialed)
			}
		})
	}
}

// The dial goes through the client it is given, so its dialer's refusal is
// the end of it, and the refusal does not carry the key.
func TestTheDialIsRefusedByTheClientsGuard(t *testing.T) {
	f := &fakeCDP{}
	srv := f.serve(t)
	refusing := &http.Client{Transport: websearch.Transport(func(netip.Addr) bool { return false }, "the browser")}

	_, err := dial(t.Context(), srv.URL+"/?token="+secret, refusing)
	if err == nil || !strings.Contains(err.Error(), "not an address the browser may reach") {
		t.Fatalf("dial = %v, want the guard's refusal", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error carries the key: %v", err)
	}
	if len(f.dialed) != 0 {
		t.Error("the browser was reached")
	}
}

// A screenshot is one message, far past the websocket library's own 32 KiB.
func TestAMessageAsLargeAsAScreenshotIsRead(t *testing.T) {
	big := strings.Repeat("A", 4<<20)
	f := &fakeCDP{send: []byte(`{"method":"Page.screencastFrame","params":{"data":"` + big + `"}}`)}
	srv := f.serve(t)

	c, err := dial(t.Context(), srv.URL, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var msg cdproto.Message
	if err := c.Read(t.Context(), &msg); err != nil {
		t.Fatalf("Read = %v", err)
	}
	if msg.Method != "Page.screencastFrame" || len(msg.Params) < len(big) {
		t.Errorf("read %s with %d bytes of params", msg.Method, len(msg.Params))
	}
}
