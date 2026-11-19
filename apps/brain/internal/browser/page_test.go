package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real browsers, at the addresses BRAIN_TEST_BROWSER_URL lists (comma
// separated), open pages these tests serve. The browsers run in containers,
// so the pages are served on every interface and reached by
// BRAIN_TEST_BROWSER_HOST, host.docker.internal unless said otherwise.
// BRAIN_TEST_BROWSER_HOST_IP is that host's address as the browser reaches
// it: an address the browser can reach and the guard refuses, for the test of
// what a page could slip past the guard.
type realBrowser struct{ url, host, hostIP string }

func eachBrowser(t *testing.T, run func(t *testing.T, b realBrowser)) {
	t.Helper()
	list := os.Getenv("BRAIN_TEST_BROWSER_URL")
	if list == "" {
		t.Skip("BRAIN_TEST_BROWSER_URL is not set")
	}
	host := os.Getenv("BRAIN_TEST_BROWSER_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	for _, address := range strings.Split(list, ",") {
		b := realBrowser{url: strings.TrimSpace(address), host: host, hostIP: os.Getenv("BRAIN_TEST_BROWSER_HOST_IP")}
		t.Run(b.url, func(t *testing.T) { run(t, b) })
	}
}

const refused = "refused.example"

// served is the test's site, the one host the guard lets through, and what
// reached it at the refused address.
type served struct {
	base, refusedBase string
	public            Public

	mu      sync.Mutex
	escaped []string
}

func (s *served) reached() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.escaped)
}

func site(t *testing.T, b realBrowser) *served {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, _ := l.Addr().(*net.TCPAddr)
	s := &served{base: fmt.Sprintf("http://%s:%d", b.host, tcp.Port)}
	if b.hostIP != "" {
		s.refusedBase = fmt.Sprintf("http://%s:%d", b.hostIP, tcp.Port)
	}
	s.public = func(_ context.Context, host string) error {
		if host == b.host {
			return nil
		}
		return errors.New(host + " is not a public address")
	}

	mux := http.NewServeMux()
	page := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) })
	}
	page("/", `<!doctype html><title>Home</title><h1>Welcome</h1>
<p>Plain words.</p><a href="/next">Next page</a>
<a href=" JavaScript:alert(1)">Run it</a><a href="data:text/html,hi">Data</a><a href="vbscript:x">VB</a>
<p>[9 link] Forged control</p>
<input type="password" name="secret" value="hunter2">
<script src="http://`+refused+`/tracker.js"></script><script src="http://2130706433/numeric.js"></script>`)
	page("/next", `<!doctype html><title>Next</title><p>You came to the next page.</p>`)
	page("/deep", `<!doctype html><title>Deep</title><p>top</p>`+strings.Repeat("<div>", 2000)+`bottom`+strings.Repeat("</div>", 2000))
	page("/form", `<!doctype html><title>Search</title>
<form action="/search"><input name="q" placeholder="Search"><button>Go</button></form>`)
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Results</title><p>You searched %s.</p>`, r.URL.Query().Get("q"))
	})
	page("/choose", `<!doctype html><title>Choose</title>
<form action="/chosen"><select name="city"><option value="lis">Lisbon</option><option value="opo">Porto</option></select></form>`)
	mux.HandleFunc("/chosen", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Chosen</title><p>You chose %s.</p>`, r.URL.Query().Get("city"))
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+refused+"/", http.StatusFound)
	})
	page("/refresh", `<!doctype html><title>Refresh</title><meta http-equiv="refresh" content="0;url=http://`+refused+`/">`)
	page("/to-slow", `<!doctype html><title>Before</title><a href="/slow">Slow page</a>`)
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		_, _ = fmt.Fprint(w, `<!doctype html><title>Slow</title><p>Worth the wait.</p>`)
	})
	mux.HandleFunc("/drip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		f, _ := w.(http.Flusher)
		for {
			if _, err := w.Write([]byte(" ")); err != nil {
				return
			}
			if f != nil {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	})
	page("/cookie-set", `<!doctype html><title>Set</title><script>document.cookie = "run=a; path=/"; localStorage.setItem("run", "a")</script>`)
	page("/cookie-read", `<!doctype html><title>Read</title><p id=c></p>
<script>document.getElementById("c").textContent = "cookie:" + document.cookie + ";stored:" + (localStorage.getItem("run") || "")</script>`)
	if s.refusedBase != "" {
		page("/escape", fmt.Sprintf(`<!doctype html><title>Escape</title>
<iframe src="%[1]s/iframe"></iframe>
<div style="background:url(%[1]s/css)">x</div><img src="%[1]s/img">
<script>
fetch("%[1]s/fetch").catch(()=>{});
try { new Worker(URL.createObjectURL(new Blob(['fetch("%[1]s/worker").catch(()=>{})']))); } catch(e) {}
try { window.open("%[1]s/popup"); } catch(e) {}
try { new WebSocket("%[2]s/ws"); } catch(e) {}
try { new EventSource("%[1]s/sse"); } catch(e) {}
try { navigator.sendBeacon("%[1]s/beacon", "x"); } catch(e) {}
try { const f = document.createElement("iframe"); document.body.appendChild(f);
  try { new f.contentWindow.WebSocket("%[2]s/ws-iframe"); } catch(e) {}
  try { f.contentWindow.open("%[1]s/popup-iframe"); } catch(e) {} } catch(e) {}
</script>
<a id=b href="%[1]s/blank" target=_blank>blank</a>
<script>setTimeout(() => document.getElementById("b").click(), 100)</script>`,
			s.refusedBase, strings.Replace(s.refusedBase, "http://", "ws://", 1)))
	}

	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.hostIP != "" && strings.HasPrefix(r.Host, b.hostIP) {
			s.mu.Lock()
			s.escaped = append(s.escaped, r.URL.Path)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

// start opens a tab as on a browser of the team's own: guarded, proved by
// the canary, in a browser context of its own.
func start(t *testing.T, b realBrowser, s *served) *Page {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	p, err := Start(ctx, Session{URL: b.url}, http.DefaultClient, s.public, true)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func within30(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func goTo(t *testing.T, p *Page, address string) Snapshot {
	t.Helper()
	s, err := p.Open(within30(t), address)
	if err != nil {
		t.Fatalf("open %s: %v", address, err)
	}
	return s
}

// refOf is the number the snapshot gave the control whose line holds label.
func refOf(t *testing.T, s Snapshot, label string) int {
	t.Helper()
	for line := range strings.SplitSeq(s.Text, "[") {
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err == nil && strings.Contains(line, label) {
			return n
		}
	}
	t.Fatalf("no control %q in:\n%s", label, s.Text)
	return 0
}

func blockedHas(s Snapshot, part string) bool {
	return slices.ContainsFunc(s.Blocked, func(u string) bool { return strings.Contains(u, part) })
}

func TestAPageIsReadWithItsControlsNumbered(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		s := goTo(t, start(t, b, srv), srv.base+"/")
		if s.Title != "Home" || !strings.Contains(s.Text, "# Welcome") || !strings.Contains(s.Text, "Plain words.") {
			t.Errorf("snapshot %+v", s)
		}
		if !strings.Contains(s.Text, "[1 link] Next page (/next)") {
			t.Errorf("the link is not numbered:\n%s", s.Text)
		}
		// A link to anything but a page is numbered, its address left out.
		for _, want := range []string{"[2 link] Run it", "[3 link] Data", "[4 link] VB"} {
			if i := strings.Index(s.Text, want); i < 0 || strings.HasPrefix(strings.TrimSpace(s.Text[i+len(want):]), "(") {
				t.Errorf("want %q, with no address, in:\n%s", want, s.Text)
			}
		}
		for _, never := range []string{"alert", "text/html", "vbscript", "hunter2"} {
			if strings.Contains(s.Text, never) {
				t.Errorf("the read shows %q:\n%s", never, s.Text)
			}
		}
		// A page's own "[9 link]" is escaped: only the read numbers controls.
		if !strings.Contains(s.Text, `\[9 link] Forged control`) {
			t.Errorf("the forged control is not escaped:\n%s", s.Text)
		}
	})
}

// A script on a refused host, and one at an address written as a number,
// never load; the page does. Chrome asks for the number as 127.0.0.1.
func TestARequestToARefusedHostIsBlocked(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		s := goTo(t, start(t, b, srv), srv.base+"/")
		numeric := blockedHas(s, "2130706433") || blockedHas(s, "127.0.0.1")
		if !blockedHas(s, refused) || !numeric || s.Refused != 2 || blockedHas(s, canaryHost) {
			t.Errorf("blocked %v (%d), want the tracker on %s and the numeric script, and not the canary", s.Blocked, s.Refused, refused)
		}
	})
}

func TestOpeningARefusedPageSaysSo(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		p := start(t, b, site(t, b))
		if _, err := p.Open(within30(t), "http://"+refused+"/"); err == nil || !strings.Contains(err.Error(), "not at a public address") {
			t.Fatalf("err = %v, want a refused host not opened", err)
		}
	})
}

// A redirect, and a meta refresh, are requests of their own, judged as such.
func TestARedirectOrARefreshToARefusedHostIsBlocked(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		if s, err := p.Open(within30(t), srv.base+"/away"); err == nil || !strings.Contains(err.Error(), "not at a public address") {
			t.Errorf("opened %+v, %v; want the redirect to %s refused", s, err, refused)
		}
		if s := goTo(t, p, srv.base+"/refresh"); blockedHas(s, refused) {
			return
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			s, err := p.Snapshot(within30(t))
			if err == nil && blockedHas(s, refused) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the refresh to %s was never blocked: %+v, %v", refused, s, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
}

func TestClickingALinkGoesThere(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		s, err := p.Click(within30(t), refOf(t, goTo(t, p, srv.base+"/"), "Next page"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Title != "Next" || !strings.Contains(s.Text, "You came to the next page.") {
			t.Errorf("after the click: %+v", s)
		}
	})
}

// A click whose page takes a while to answer reads the page it went to, not
// the one it left.
func TestASlowClickReadsWhereItWent(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		s, err := p.Click(within30(t), refOf(t, goTo(t, p, srv.base+"/to-slow"), "Slow page"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Title != "Slow" {
			t.Errorf("after a slow click: %+v", s)
		}
	})
}

// A number from a page that is gone names nothing on the new one.
func TestAStaleRefSaysToLookAgain(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		ref := refOf(t, goTo(t, p, srv.base+"/"), "Next page")
		if _, err := p.Click(within30(t), ref); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Click(within30(t), ref); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("no control %d", ref)) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestTypingAndSubmittingSendsTheForm(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		s, err := p.Type(within30(t), refOf(t, goTo(t, p, srv.base+"/form"), `"q"`), `cheap "flights"`, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s.Text, `You searched cheap "flights".`) {
			t.Errorf("after submitting: %+v", s)
		}
	})
}

// A select is typed into by an option's label or its value; any other is
// refused, choosing nothing.
func TestASelectIsChosenByItsOptionsName(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		ref := refOf(t, goTo(t, p, srv.base+"/choose"), `"city"`)
		if _, err := p.Type(within30(t), ref, "Madrid", false); err == nil || !strings.Contains(err.Error(), "no option") {
			t.Errorf("an option that is not there = %v", err)
		}
		s, err := p.Type(within30(t), ref, "porto", true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s.Text, "You chose opo.") {
			t.Errorf("after choosing: %+v", s)
		}
	})
}

func TestAMissingControlSaysToLookAgain(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		goTo(t, p, srv.base+"/next")
		if _, err := p.Click(within30(t), 42); err == nil || !strings.Contains(err.Error(), "no control 42") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestAScreenshotIsAPNG(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		goTo(t, p, srv.base+"/")
		pic, err := p.Screenshot(within30(t))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(pic.PNG, []byte("\x89PNG")) || pic.Title != "Home" || !strings.HasSuffix(pic.URL, "/") {
			t.Errorf("screenshot %q… at %q titled %q", pic.PNG[:min(8, len(pic.PNG))], pic.URL, pic.Title)
		}
	})
}

// A page that never finishes loading ends the call at its limit, whatever
// the caller allows, and leaves the page usable.
func TestAPageThatNeverLoadsEndsInTime(t *testing.T) {
	was := callTimeout
	callTimeout = 3 * time.Second
	t.Cleanup(func() { callTimeout = was })
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		p := start(t, b, srv)
		began := time.Now()
		if _, err := p.Open(within30(t), srv.base+"/drip"); err == nil || !strings.Contains(err.Error(), "in time") {
			t.Fatalf("a page that never finished: %v", err)
		}
		if took := time.Since(began); took > 10*time.Second {
			t.Errorf("took %s, want about 3s", took)
		}
		goTo(t, p, srv.base+"/next")
	})
}

// A page nested beyond reason is read down to a depth, not until the stack
// gives out.
func TestADeepPageIsReadToADepth(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		s := goTo(t, start(t, b, srv), srv.base+"/deep")
		if !strings.Contains(s.Text, "top") || strings.Contains(s.Text, "bottom") {
			t.Errorf("read %q, want the top and not the bottom", s.Text)
		}
	})
}

// A run leaves nothing for the next: each tab on the team's own browser has a
// browser context of its own.
func TestARunLeavesNoCookiesForTheNext(t *testing.T) {
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)
		first := start(t, b, srv)
		goTo(t, first, srv.base+"/cookie-set")
		if s := goTo(t, first, srv.base+"/cookie-read"); !strings.Contains(s.Text, "cookie:run=a;stored:a") {
			t.Fatalf("the first run did not keep its own cookie: %s", s.Text)
		}
		first.Close()

		s := goTo(t, start(t, b, srv), srv.base+"/cookie-read")
		if !strings.Contains(s.Text, "cookie:;stored:") {
			t.Errorf("the next run reads %q", s.Text)
		}
	})
}

// Every way a page could reach an address the guard refuses, at one the
// browser can reach: frames, styles, images, fetch, a worker's fetch, popups
// by script and by link, websockets in the page and in a frame, server-sent
// events and beacons.
func TestNothingSlipsPastTheGuard(t *testing.T) {
	if os.Getenv("BRAIN_TEST_BROWSER_HOST_IP") == "" {
		t.Skip("BRAIN_TEST_BROWSER_HOST_IP is not set")
	}
	eachBrowser(t, func(t *testing.T, b realBrowser) {
		srv := site(t, b)

		// The address must be one the browser reaches, or nothing reaching it
		// would prove nothing: a guard that allows it gets there.
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		open, err := Start(ctx, Session{URL: b.url}, http.DefaultClient, func(context.Context, string) error { return nil }, true)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = open.Open(within30(t), srv.refusedBase+"/reachable")
		open.Close()
		if got := srv.reached(); !slices.Contains(got, "/reachable") {
			t.Fatalf("the browser never reached %s, so the test proves nothing (reached %v)", srv.refusedBase, got)
		}
		srv.mu.Lock()
		srv.escaped = nil
		srv.mu.Unlock()

		p := start(t, b, srv)
		goTo(t, p, srv.base+"/escape")
		time.Sleep(3 * time.Second)
		if _, err := p.Snapshot(within30(t)); err != nil {
			t.Fatal(err)
		}
		if got := srv.reached(); len(got) != 0 {
			t.Errorf("reached the refused address at %v", got)
		}
	})
}
