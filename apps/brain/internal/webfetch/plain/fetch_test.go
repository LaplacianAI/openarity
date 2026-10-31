package plain

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
)

// loopback4 is the only address these tests let a fetch dial: the stub
// servers listen on it, and a redirect anywhere else has to be refused.
var loopback4 = netip.MustParseAddr("127.0.0.1")

func onlyLoopback4(a netip.Addr) bool { return a == loopback4 }

func everywhere(netip.Addr) bool { return true }

// serve starts a stub on 127.0.0.1 and closes it with the test.
func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// serveOn6 starts a stub on [::1], an address onlyLoopback4 refuses, and
// counts the requests that reach it.
func serveOn6(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("the private network"))
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &hits
}

func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// fetch reads one page through the plain fetcher on a client that may dial
// what allow says.
func fetch(t *testing.T, allow func(netip.Addr) bool, url string) (webfetch.Page, error) {
	t.Helper()
	return New(webfetch.NewClient(allow)).Fetch(t.Context(), url)
}

func TestARedirectToAPrivateAddressIsRefused(t *testing.T) {
	t.Parallel()

	private, hits := serveOn6(t)
	public := serve(t, redirectTo(private.URL+"/secrets"))

	_, err := fetch(t, onlyLoopback4, public.URL)
	if err == nil || !strings.Contains(err.Error(), "is not an address web fetch may reach") {
		t.Errorf("err = %v, want the dial refused", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the private server answered %d requests", n)
	}
}

// Link-local is where cloud metadata answers. It is refused even by an
// allow that would take anything, before anything is dialled.
func TestARedirectToMetadataIsRefusedWhateverAllowSays(t *testing.T) {
	t.Parallel()

	public := serve(t, redirectTo("http://169.254.169.254/latest/meta-data/"))

	_, err := fetch(t, everywhere, public.URL)
	if err == nil || !strings.Contains(err.Error(), "is not an address web fetch may reach") {
		t.Errorf("err = %v, want the dial refused", err)
	}
}

// chain serves n redirects, each to the next hop, then a page.
func chain(t *testing.T, n int) string {
	t.Helper()
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		var hop int
		_, _ = fmt.Sscanf(r.URL.Path, "/%d", &hop)
		if hop < n {
			http.Redirect(w, r, fmt.Sprintf("%s/%d", srv.URL, hop+1), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("arrived"))
	})
	return srv.URL + "/0"
}

func TestFiveRedirectsArrive(t *testing.T) {
	t.Parallel()

	p, err := fetch(t, onlyLoopback4, chain(t, 5))
	if err != nil || p.Markdown != "arrived" {
		t.Errorf("page %q err %v, want it to arrive after five redirects", p.Markdown, err)
	}
}

func TestSixRedirectsAreTooMany(t *testing.T) {
	t.Parallel()

	_, err := fetch(t, onlyLoopback4, chain(t, 6))
	if err == nil || !strings.Contains(err.Error(), "the address redirected more than 5 times") {
		t.Errorf("err = %v, want too many redirects", err)
	}
}

func TestARedirectToFTPIsNotFollowed(t *testing.T) {
	t.Parallel()

	public := serve(t, redirectTo("ftp://files.example/pub"))

	_, err := fetch(t, everywhere, public.URL)
	if err == nil || !strings.Contains(err.Error(), "a redirect went to ftp, which web fetch does not follow") {
		t.Errorf("err = %v, want the scheme refused", err)
	}
}

func TestTheFinalURLIsWhereThePageWasRead(t *testing.T) {
	t.Parallel()

	end := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("here"))
	})
	start := serve(t, redirectTo(end.URL+"/moved"))

	p, err := fetch(t, onlyLoopback4, start.URL)
	if err != nil || p.URL != end.URL+"/moved" {
		t.Errorf("url %q err %v, want %s/moved", p.URL, err, end.URL)
	}
}

func TestANon2xxIsAnErrorNamingTheStatus(t *testing.T) {
	t.Parallel()

	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope, and here is a secret", http.StatusForbidden)
	})

	_, err := fetch(t, onlyLoopback4, srv.URL)
	if err == nil || err.Error() != "the page answered 403" {
		t.Errorf("err = %v, want exactly %q", err, "the page answered 403")
	}
}

// A page that never answers ends with the context, and the error says so.
// The client's own fifteen seconds is pinned in webfetch's tests.
func TestASlowPageEndsWithItsDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New(webfetch.NewClient(onlyLoopback4)).Fetch(ctx, srv.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s, want about 200ms", took)
	}
}

func TestACancelledContextStopsTheFetch(t *testing.T) {
	t.Parallel()

	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := New(webfetch.NewClient(onlyLoopback4)).Fetch(ctx, srv.URL); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// Wikipedia refuses a request with Go's default User-Agent ("Please set a
// user-agent and respect our robot policy"), and so do others. Every hop of
// a redirect carries the brain's.
func TestEveryRequestSaysItIsOpenarity(t *testing.T) {
	t.Parallel()

	var agents []string
	var mu sync.Mutex
	record := func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		agents = append(agents, r.UserAgent())
	}
	end := serve(t, func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	start := serve(t, func(w http.ResponseWriter, r *http.Request) {
		record(r)
		http.Redirect(w, r, end.URL, http.StatusFound)
	})

	if _, err := fetch(t, onlyLoopback4, start.URL); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(agents) != 2 {
		t.Fatalf("%d requests, want 2", len(agents))
	}
	for i, ua := range agents {
		if ua != userAgent || !strings.HasPrefix(ua, "Openarity") || !strings.Contains(ua, "+https://github.com/LaplacianAI/openarity") {
			t.Errorf("request %d said %q, want %q", i, ua, userAgent)
		}
	}
}
