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
	"testing"
	"time"
)

// A real browser, at BRAIN_TEST_BROWSER_URL, opens pages this test serves.
// The browser runs in a container, so the pages are served on every
// interface and reached by BRAIN_TEST_BROWSER_HOST, host.docker.internal
// unless said otherwise.
type realBrowser struct{ url, host string }

func browserUnderTest(t *testing.T) realBrowser {
	t.Helper()
	address := os.Getenv("BRAIN_TEST_BROWSER_URL")
	if address == "" {
		t.Skip("BRAIN_TEST_BROWSER_URL is not set")
	}
	host := os.Getenv("BRAIN_TEST_BROWSER_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	return realBrowser{url: address, host: host}
}

const refused = "refused.example"

// site serves the pages the tests open, and is the one host the guard lets
// through; every other is refused.
func site(t *testing.T, b realBrowser) (base string, public Public) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<!doctype html><title>Home</title><h1>Welcome</h1>
<p>Plain words.</p><a href="/next">Next page</a>
<script src="http://`+refused+`/tracker.js"></script>`)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<!doctype html><title>Next</title><p>You came to the next page.</p>`)
	})
	mux.HandleFunc("/form", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<!doctype html><title>Search</title>
<form action="/search"><input name="q" placeholder="Search"><button>Go</button></form>`)
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Results</title><p>You searched %s.</p>`, r.URL.Query().Get("q"))
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+refused+"/", http.StatusFound)
	})

	// Every interface: the browser is in a container, not on loopback.
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	tcp, _ := l.Addr().(*net.TCPAddr)
	port := tcp.Port
	public = func(_ context.Context, host string) error {
		if host == b.host {
			return nil
		}
		return errors.New(host + " is not a public address")
	}
	return fmt.Sprintf("http://%s:%d", b.host, port), public
}

func start(t *testing.T, b realBrowser, public Public) *Page {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	p, err := Start(ctx, Session{URL: b.url}, http.DefaultClient, public, true)
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

func TestAPageIsReadWithItsControlsNumbered(t *testing.T) {
	b := browserUnderTest(t)
	base, public := site(t, b)
	p := start(t, b, public)

	s, err := p.Open(within30(t), base+"/")
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Home" || !strings.Contains(s.Text, "# Welcome") || !strings.Contains(s.Text, "Plain words.") {
		t.Errorf("snapshot %+v", s)
	}
	if !strings.Contains(s.Text, "[1 link] Next page (/next)") {
		t.Errorf("the link is not numbered:\n%s", s.Text)
	}
}

// The page's own script from a refused host never loads, and the page does.
func TestARequestToARefusedHostIsBlocked(t *testing.T) {
	b := browserUnderTest(t)
	base, public := site(t, b)
	p := start(t, b, public)

	s, err := p.Open(within30(t), base+"/")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(s.Blocked, func(u string) bool { return strings.Contains(u, refused) }) {
		t.Errorf("blocked %v, want the tracker on %s", s.Blocked, refused)
	}
}

func TestOpeningARefusedPageSaysSo(t *testing.T) {
	b := browserUnderTest(t)
	_, public := site(t, b)
	p := start(t, b, public)

	if _, err := p.Open(within30(t), "http://"+refused+"/"); err == nil || !strings.Contains(err.Error(), "not at a public address") {
		t.Fatalf("err = %v, want a refused host not opened", err)
	}
}

// A redirect is a request of its own, and judged as one.
func TestARedirectToARefusedHostIsBlocked(t *testing.T) {
	b := browserUnderTest(t)
	base, public := site(t, b)
	p := start(t, b, public)

	if s, err := p.Open(within30(t), base+"/away"); err == nil || !strings.Contains(err.Error(), "not at a public address") {
		t.Fatalf("opened %+v, %v; want the redirect to %s refused", s, err, refused)
	}
}

func TestClickingALinkGoesThere(t *testing.T) {
	b := browserUnderTest(t)
	base, public := site(t, b)
	p := start(t, b, public)

	s, err := p.Open(within30(t), base+"/")
	if err != nil {
		t.Fatal(err)
	}
	s, err = p.Click(within30(t), refOf(t, s, "Next page"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Next" || !strings.Contains(s.Text, "You came to the next page.") {
		t.Errorf("after the click: %+v", s)
	}
}

func TestTypingAndSubmittingSendsTheForm(t *testing.T) {
	b := browserUnderTest(t)
	base, public := site(t, b)
	p := start(t, b, public)

	s, err := p.Open(within30(t), base+"/form")
	if err != nil {
		t.Fatal(err)
	}
	s, err = p.Type(within30(t), refOf(t, s, `"q"`), `cheap "flights"`, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Text, `You searched cheap "flights".`) {
		t.Errorf("after submitting: %+v", s)
	}
}

func TestAMissingControlSaysToLookAgain(t *testing.T) {
	b := browserUnderTest(t)
	base, public := site(t, b)
	p := start(t, b, public)

	if _, err := p.Open(within30(t), base+"/next"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(within30(t), 42); err == nil || !strings.Contains(err.Error(), "no control 42") {
		t.Errorf("err = %v", err)
	}
}

func TestAScreenshotIsAPNG(t *testing.T) {
	b := browserUnderTest(t)
	base, public := site(t, b)
	p := start(t, b, public)

	if _, err := p.Open(within30(t), base+"/"); err != nil {
		t.Fatal(err)
	}
	png, err := p.Screenshot(within30(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Errorf("screenshot starts %q", png[:min(8, len(png))])
	}
}
