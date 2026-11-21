package guard

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var loopback = netip.MustParseAddr("127.0.0.1")

// publicRoom opens a public room on loopback with dial standing in for the
// guard's, and returns the proxy's address.
func publicRoom(t *testing.T, g *Guard) string {
	t.Helper()
	g.proxyPort = freePort(t)
	if err := g.Open("r1", loopback, nil, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close("r1") })
	return "127.0.0.1:" + strconv.Itoa(g.proxyPort)
}

// connectThrough sends a CONNECT for target and returns the tunnel and the
// proxy's status line.
func connectThrough(t *testing.T, proxy, target string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(t.Context(), "tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, _ = c.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"))
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Time{})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return c, br, resp.Status + " " + string(body)
	}
	return c, br, resp.Status
}

func TestAConnectIsDialledByNameAndRelayed(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{got: make(chan []byte, 1)}
	g := New(quiet())
	g.dial = up.dial
	proxy := publicRoom(t, g)

	c, _, status := connectThrough(t, proxy, "api.example.com:443")
	if !strings.HasPrefix(status, "200") {
		t.Fatalf("status %q", status)
	}
	_, _ = c.Write([]byte("the tunnel's bytes"))
	closeWrite(c)
	if got := within(t, up.got); string(got) != "the tunnel's bytes" {
		t.Fatalf("upstream got %q", got)
	}
	if d := up.dialed(); len(d) != 1 || d[0] != "api.example.com:443" {
		t.Fatalf("dialed %v: the name must reach the dialer, which resolves and checks it", d)
	}
}

func TestPlainHTTPIsForwardedWithoutTheProxysHeaders(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Clone(context.Background())
		mu.Unlock()
		_, _ = w.Write([]byte("from upstream"))
	}))
	t.Cleanup(backend.Close)

	var dialed []string
	g := New(quiet())
	g.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, backend.Listener.Addr().String())
	}
	proxy := publicRoom(t, g)

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy, User: url.UserPassword("u", "p")})}}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/page", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "from upstream" {
		t.Fatalf("body %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 || dialed[0] != "example.com:80" {
		t.Fatalf("dialed %v", dialed)
	}
	if seen.Host != "example.com" || seen.URL.Path != "/page" || seen.Header.Get("Proxy-Authorization") != "" {
		t.Fatalf("upstream saw host %q path %q proxy auth %q", seen.Host, seen.URL.Path, seen.Header.Get("Proxy-Authorization"))
	}
}

// The real dialer, not a stand-in: a name or address that lands somewhere
// private is refused whichever way the room asks for it.
func TestThePrivateNetworkIsRefusedThroughTheProxy(t *testing.T) {
	t.Parallel()
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a secret"))
	}))
	t.Cleanup(inside.Close)
	g := New(quiet())
	proxy := publicRoom(t, g)

	_, _, status := connectThrough(t, proxy, inside.Listener.Addr().String())
	if !strings.HasPrefix(status, "502") || !strings.Contains(status, "private") {
		t.Fatalf("CONNECT to loopback: %q", status)
	}

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy})}}
	for _, target := range []string{inside.URL, "http://localhost:" + strconv.Itoa(portOf(inside.Listener)) + "/"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway || strings.Contains(string(body), "a secret") {
			t.Fatalf("%s: %d %q", target, resp.StatusCode, body)
		}
	}
}

func TestTheProxyIsNotAWebServer(t *testing.T) {
	t.Parallel()
	g := New(quiet())
	proxy := publicRoom(t, g)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+proxy+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an origin-form request: %d", resp.StatusCode)
	}
}

func TestARoomThatIsNotPublicHasNoProxy(t *testing.T) {
	t.Parallel()
	g := New(quiet())
	g.proxyPort = freePort(t)
	if err := g.Open("r1", loopback, nil, false); err != nil {
		t.Fatal(err)
	}
	defer g.Close("r1")
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(g.proxyPort))
	if err == nil {
		_ = c.Close()
		t.Fatal("a room with a list of hosts was given the proxy")
	}
}

func TestClosingAPublicRoomCutsItsTunnels(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{got: make(chan []byte, 1)}
	g := New(quiet())
	g.dial = up.dial
	proxy := publicRoom(t, g)
	c, br, status := connectThrough(t, proxy, "api.example.com:443")
	if !strings.HasPrefix(status, "200") {
		t.Fatal(status)
	}
	g.Close("r1")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the tunnel outlived its room")
	} else if ne := net.Error(nil); errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("the tunnel was left open when its room closed")
	}
	if c, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", proxy); err == nil {
		_ = c.Close()
		t.Fatal("the proxy still listens for a closed room")
	}
}

// pipeDial is a dialer whose upstream end is served by serve.
func pipeDial(serve func(net.Conn)) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go serve(b)
		return a, nil
	}
}

func TestAMailPortIsRefused(t *testing.T) {
	t.Parallel()
	up := &fakeUpstream{got: make(chan []byte, 1)}
	g := New(quiet())
	g.dial = up.dial
	proxy := publicRoom(t, g)
	for _, target := range []string{"smtp.example.com:25", "smtp.example.com:465", "smtp.example.com:587"} {
		if _, _, status := connectThrough(t, proxy, target); !strings.HasPrefix(status, "403") {
			t.Errorf("CONNECT %s: %q, want 403", target, status)
		}
	}
	// The dialer takes these for 25 and 465, so none may pass as written.
	for _, target := range []string{"smtp.example.com:025", "smtp.example.com:0025", "smtp.example.com:smtp", "smtp.example.com:submissions"} {
		if _, _, status := connectThrough(t, proxy, target); !strings.HasPrefix(status, "400") {
			t.Errorf("CONNECT %s: %q, want it refused as no port number", target, status)
		}
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy})}}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://smtp.example.com:25/", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a forwarded request to port 25: %d, want 403", resp.StatusCode)
	}
	if d := up.dialed(); len(d) != 0 {
		t.Fatalf("dialed %v", d)
	}
}

func TestARoomHoldsSoManyTunnelsAtOnce(t *testing.T) {
	t.Parallel()
	g := New(quiet())
	g.dial = pipeDial(func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	proxy := publicRoom(t, g)
	for i := range MaxConns {
		if _, _, status := connectThrough(t, proxy, "a.example.com:443"); !strings.HasPrefix(status, "200") {
			t.Fatalf("tunnel %d: %q", i+1, status)
		}
	}
	if _, _, status := connectThrough(t, proxy, "a.example.com:443"); !strings.HasPrefix(status, "429") {
		t.Fatalf("one tunnel past the cap: %q, want 429", status)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy})}}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://a.example.com/", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a forwarded request past the cap: %d, want 429", resp.StatusCode)
	}
}

// A tunnel that carries nothing is cut; one carrying bytes either way is
// kept, though the other side is quiet.
func TestAnIdleTunnelIsCutAndABusyOneIsNot(t *testing.T) {
	t.Parallel()
	g := New(quiet())
	g.tunnelIdle = 300 * time.Millisecond
	g.dial = pipeDial(func(c net.Conn) {
		defer c.Close()
		for range 8 {
			if _, err := c.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		_, _ = io.Copy(io.Discard, c)
	})
	proxy := publicRoom(t, g)
	c, br, status := connectThrough(t, proxy, "a.example.com:443")
	if !strings.HasPrefix(status, "200") {
		t.Fatalf("status %q", status)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 8)
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("a tunnel busy downstream was cut after %q: %v", got, err)
	}
	start := time.Now()
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the quiet tunnel carried more")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("the idle tunnel was cut after %s, want about %s", waited, g.tunnelIdle)
	}
}
