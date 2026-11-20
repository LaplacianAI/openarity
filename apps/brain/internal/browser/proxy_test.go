package browser

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
)

// upstream is a site that counts its visitors and says what it was sent.
type upstream struct {
	*httptest.Server
	visits atomic.Int32
	header atomic.Value
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.visits.Add(1)
		u.header.Store(r.Header.Clone())
		_, _ = io.WriteString(w, "hello from "+r.URL.Path)
	}))
	t.Cleanup(u.Close)
	return u
}

// proxied is a proxy that takes loopback for the internet, so the tests'
// own sites stand in for public ones, or refuses everything.
func proxied(t *testing.T, public bool) (*Proxy, *http.Client) {
	t.Helper()
	p := NewProxy(func(a netip.Addr) bool { return public && a.IsLoopback() }, slog.New(slog.DiscardHandler))
	// The tests' sites are on any port.
	p.port = func(uint16) bool { return true }
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	at, _ := url.Parse(srv.URL)
	return p, &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(at)}}
}

// connect asks for a tunnel to target on a connection of its own, and is
// the status the proxy answered and the connection, tunnelled if it was 200.
func connect(t *testing.T, p *Proxy, target string) (int, net.Conn) {
	t.Helper()
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return connectAt(t, strings.TrimPrefix(srv.URL, "http://"), target)
}

func connectAt(t *testing.T, proxy, target string) (int, net.Conn) {
	t.Helper()
	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	// As a CONNECT's answer, so a tunnel's is read as having no body.
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, c
}

// A page's request reaches a public site through the proxy, carrying nothing
// of the browser's network and nothing meant for the proxy.
func TestAPublicSiteIsReachedThroughTheProxy(t *testing.T) {
	site := newUpstream(t)
	_, client := proxied(t, true)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, site.URL+"/page", nil)
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello from /page" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	h, _ := site.header.Load().(http.Header)
	for _, name := range []string{"X-Forwarded-For", "Forwarded", "Proxy-Authorization"} {
		if v := h.Get(name); v != "" {
			t.Errorf("the site was sent %s: %q", name, v)
		}
	}
}

// A tunnel to a public address carries the browser's bytes both ways, as
// TLS and websockets need.
func TestATunnelToAPublicAddressCarriesBothWays(t *testing.T) {
	site := newUpstream(t)
	p, _ := proxied(t, true)

	status, c := connect(t, p, strings.TrimPrefix(site.URL, "http://"))
	if status != http.StatusOK {
		t.Fatalf("CONNECT = %d", status)
	}
	_, _ = io.WriteString(c, "GET /tunnelled HTTP/1.1\r\nHost: site\r\nConnection: close\r\n\r\n")
	got, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(got.Body)
	_ = got.Body.Close()
	if string(body) != "hello from /tunnelled" {
		t.Errorf("through the tunnel: %q", body)
	}
}

// Nothing but a public address is dialed, by tunnel or by request: the
// decision is on the address connected to, so a name gets no further than
// the addresses it names.
func TestAnAddressThatIsNotPublicIsNeverReached(t *testing.T) {
	site := newUpstream(t)
	p, client := proxied(t, false)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(site.URL, "http://"))

	for _, target := range []string{"127.0.0.1:" + port, "localhost:" + port} {
		if status, _ := connect(t, p, target); status != http.StatusForbidden {
			t.Errorf("CONNECT %s = %d, want refused", target, status)
		}
	}
	resp, err := client.Get(strings.Replace(site.URL, "127.0.0.1", "localhost", 1) + "/") //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET through the proxy = %d, want refused", resp.StatusCode)
	}
	if n := site.visits.Load(); n != 0 {
		t.Errorf("the site was reached %d times", n)
	}
}

// A link-local address, a cloud's metadata above all, is refused whatever
// allow says.
func TestALinkLocalAddressIsRefusedWhateverIsAllowed(t *testing.T) {
	p := NewProxy(func(netip.Addr) bool { return true }, slog.New(slog.DiscardHandler))
	for _, target := range []string{"169.254.169.254:80", "[fe80::1]:80"} {
		if status, _ := connect(t, p, target); status != http.StatusForbidden {
			t.Errorf("CONNECT %s = %d, want refused", target, status)
		}
	}
}

// Only a tunnel or an absolute http:// request is proxied: the proxy is no
// web server of its own.
func TestOnlyProxyRequestsAreServed(t *testing.T) {
	p, _ := proxied(t, true)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/") //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a plain request = %d", resp.StatusCode)
	}
}

// Past its connections the proxy refuses rather than queues.
func TestPastItsConnectionsTheProxyRefuses(t *testing.T) {
	site := newUpstream(t)
	p, client := proxied(t, true)
	for range cap(p.slots) {
		p.slots <- struct{}{}
	}
	resp, err := client.Get(site.URL) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || site.visits.Load() != 0 {
		t.Errorf("full: %d, %d visits", resp.StatusCode, site.visits.Load())
	}
}

// A tunnel ends with the proxy, not when the browser lets it go.
func TestATunnelEndsWhenTheProxyStops(t *testing.T) {
	site := newUpstream(t)
	p := NewProxy(func(a netip.Addr) bool { return a.IsLoopback() }, slog.New(slog.DiscardHandler))
	p.port = func(uint16) bool { return true }
	ctx, stop := context.WithCancel(t.Context())
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx, l) }()

	status, c := connectAt(t, l.Addr().String(), strings.TrimPrefix(site.URL, "http://"))
	if status != http.StatusOK {
		t.Fatalf("CONNECT = %d", status)
	}

	stop()
	if err := <-served; err != nil {
		t.Fatalf("Serve = %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ne net.Error
	if _, err := c.Read(make([]byte, 1)); err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Errorf("read after the proxy stopped = %v, want the tunnel closed", err)
	}
}

// Only a web port is dialed: the proxy is no relay for mail, SSH or a scan.
func TestOnlyAWebPortIsDialed(t *testing.T) {
	for port, want := range map[uint16]bool{80: true, 443: true, 8080: true, 8443: true, 0: false, 22: false, 25: false, 3128: false, 9222: false} {
		if webPort(port) != want {
			t.Errorf("port %d: %v, want %v", port, !want, want)
		}
	}
	site := newUpstream(t)
	p := NewProxy(func(a netip.Addr) bool { return a.IsLoopback() }, slog.New(slog.DiscardHandler))
	if status, _ := connect(t, p, strings.TrimPrefix(site.URL, "http://")); status != http.StatusForbidden {
		t.Errorf("CONNECT to the site's own port = %d, want refused", status)
	}
	if n := site.visits.Load(); n != 0 {
		t.Errorf("the site was reached %d times", n)
	}
}

// What the brain's own guard counts as not public, the proxy never dials:
// the forms an address hides behind included.
func TestTheProxyRefusesWhatIsNotPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":                true,
		"2606:4700::1111":        true,
		"::ffff:8.8.8.8":         true,
		"::ffff:169.254.169.254": false,
		"::ffff:127.0.0.1":       false,
		"169.254.169.254":        false,
		"100.100.100.200":        false,
		"100.64.0.1":             false,
		"10.0.0.1":               false,
		"0.0.0.0":                false,
		"::":                     false,
		"::1":                    false,
		"64:ff9b::a9fe:a9fe":     false,
		"224.0.0.1":              false,
		"ff02::1":                false,
		"fe80::1%eth0":           false,
		"fd00::1":                false,
		"255.255.255.255":        false,
	} {
		if got := reachable(netip.MustParseAddr(addr), hub.Public); got != want {
			t.Errorf("%s: reachable %v, want %v", addr, got, want)
		}
	}
}

// A CONNECT must name a port; without one nothing is dialed.
func TestACONNECTWithNoPortIsRefused(t *testing.T) {
	p, _ := proxied(t, true)
	if status, _ := connect(t, p, "localhost"); status != http.StatusBadRequest {
		t.Errorf("CONNECT localhost = %d, want a bad request", status)
	}
}

// A request's body reaches the site as the browser sent it.
func TestAPostedBodyArrivesWhole(t *testing.T) {
	var got atomic.Value
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(r.Method + " " + string(b))
	}))
	t.Cleanup(site.Close)
	_, client := proxied(t, true)
	body := strings.Repeat("form=value&", 50_000)
	resp, err := client.Post(site.URL+"/submit", "application/x-www-form-urlencoded", strings.NewReader(body)) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if s, _ := got.Load().(string); s != "POST "+body {
		t.Errorf("the site was sent %d bytes, want %d", len(s), len("POST "+body))
	}
}

// A browser that half-closes a tunnel after its request still gets all of
// the answer.
func TestAHalfClosedTunnelStillCarriesTheAnswer(t *testing.T) {
	answer := append(bytes.Repeat([]byte("x"), 16), bytes.Repeat([]byte("y"), 1<<20)...)
	sent := make(chan string, 1)
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		got, _ := io.ReadAll(c) // until the browser is done sending
		sent <- string(got)
		_, _ = c.Write(answer)
	}()
	p, _ := proxied(t, true)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	target := l.Addr().String()
	// The request's first bytes come with the CONNECT, as a browser's may.
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\nthe ")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", resp.StatusCode)
	}
	_, _ = io.WriteString(c, "request")
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("a %T, not TCP", c)
	}
	if err := tc.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, answer) {
		t.Errorf("after a half-close: %d bytes, want %d", len(got), len(answer))
	}
	if s := <-sent; s != "the request" {
		t.Errorf("the site was sent %q", s)
	}
}

// A site that cannot be reached is a 502 that says no more: the page
// learns nothing of the worker's networks from why.
func TestAnUnreachableSiteSaysNothingOfWhy(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()
	_, client := proxied(t, true)
	resp, err := client.Get("http://" + closed + "/") //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || strings.TrimSpace(string(body)) != "could not be reached" {
		t.Errorf("got %d %q", resp.StatusCode, body)
	}
}

// A tunnel that carries nothing either way is closed, and gives its
// connection back.
func TestAnIdleTunnelIsClosed(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	held := make(chan net.Conn, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			held <- c // a site that never answers, nor hangs up
		}
	}()
	t.Cleanup(func() {
		select {
		case c := <-held:
			_ = c.Close()
		default:
		}
	})
	p, _ := proxied(t, true)
	p.idle = 200 * time.Millisecond

	status, c := connect(t, p, l.Addr().String())
	if status != http.StatusOK {
		t.Fatalf("CONNECT = %d", status)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ne net.Error
	if _, err := c.Read(make([]byte, 1)); err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Fatalf("read from an idle tunnel = %v, want it closed", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(p.slots) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d connections still held", len(p.slots))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
