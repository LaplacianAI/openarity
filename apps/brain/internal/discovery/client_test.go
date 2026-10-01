package discovery

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func allowAll(netip.Addr) bool  { return true }
func allowNone(netip.Addr) bool { return false }

// counting is a server that records what reached it, so a refusal can be
// shown to have sent nothing rather than merely to have returned an error.
type counting struct {
	hits atomic.Int32
	mu   sync.Mutex
	auth []string
}

func (c *counting) handler(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		c.mu.Lock()
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		c.mu.Unlock()
		_, _ = w.Write(body)
	}
}

// clientFor is the client discovery builds, with a deadline far enough away
// that only the tests about deadlines meet it.
func clientFor(p Policy, token string) *http.Client {
	return newClient(newTransport(p), token, time.Now().Add(time.Minute))
}

func trusting(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

func get(t *testing.T, c *http.Client, target string) (int, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// --- where it may dial ---

// httptest listens on loopback, which is not public, so the default answer is
// no. The refusal happens at dial time, so the server never sees a request.
func TestLoopbackIsRefusedUnlessAllowed(t *testing.T) {
	t.Parallel()

	var srv counting
	s := httptest.NewServer(srv.handler([]byte("ok")))
	t.Cleanup(s.Close)

	_, err := get(t, clientFor(Policy{Allow: allowNone}, ""), s.URL)
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("a refused dial still reached the server")
	}

	if status, err := get(t, clientFor(Policy{Allow: allowAll}, ""), s.URL); err != nil || status != http.StatusOK {
		t.Fatalf("allowed: %d, %v", status, err)
	}
}

// Allow is asked about the address being dialled, after resolution — never
// the name in the URL. That is what closes DNS rebinding: there is no second
// lookup between the check and the connect.
func TestAllowIsAskedAboutTheResolvedAddress(t *testing.T) {
	t.Parallel()

	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(s.Close)
	addr, ok := s.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T", s.Listener.Addr())
	}
	port := strconv.Itoa(addr.Port)

	var mu sync.Mutex
	var asked []netip.Addr
	record := func(a netip.Addr) bool {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, a)
		return false
	}

	_, err := get(t, clientFor(Policy{Allow: record}, ""), "http://localhost:"+port)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(asked) == 0 {
		t.Fatal("Allow was never asked")
	}
	for _, a := range asked {
		if !a.IsLoopback() {
			t.Errorf("Allow was asked about %v, want the loopback address localhost resolved to", a)
		}
	}
}

// Link-local is where cloud metadata answers with the instance's credentials.
// It is refused whatever Allow says, and before a packet leaves: Control runs
// before connect, so this needs no link-local network to prove.
func TestLinkLocalIsNeverDialledWhateverAllowSays(t *testing.T) {
	t.Parallel()

	c := clientFor(Policy{Allow: allowAll}, "")
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://169.254.0.1:8080/",
		"http://[::ffff:169.254.169.254]/",
		"http://[fe80::1%25lo0]/",
	} {
		_, err := get(t, c, target)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", target, err)
		}
	}
}

func TestDialable(t *testing.T) {
	t.Parallel()

	for addr, want := range map[string]bool{
		"203.0.113.7":            true,
		"10.0.0.1":               true,
		"169.254.169.254":        false,
		"169.254.0.0":            false,
		"169.254.255.255":        false,
		"::ffff:169.254.169.254": false,
		"fe80::1":                false,
		"febf:ffff::1":           false,
		"169.253.255.255":        true,
		"169.255.0.0":            true,
		"fec0::1":                true,
	} {
		if got := dialable(netip.MustParseAddr(addr), allowAll); got != want {
			t.Errorf("dialable(%s) = %v, want %v", addr, got, want)
		}
	}

	if dialable(netip.MustParseAddr("203.0.113.7"), allowNone) {
		t.Error("an address Allow refused was dialable")
	}
}

// Allow sees the plain IPv4 form of a mapped address, so a policy written in
// IPv4 prefixes cannot be sidestepped by writing the address as IPv6.
func TestAllowSeesMappedAddressesUnmapped(t *testing.T) {
	t.Parallel()

	var got netip.Addr
	dialable(netip.MustParseAddr("::ffff:10.0.0.1"), func(a netip.Addr) bool { got = a; return true })
	if got != netip.MustParseAddr("10.0.0.1") {
		t.Errorf("Allow saw %v, want 10.0.0.1", got)
	}
}

// netip.Prefix.Contains is false for any zoned address, so a zone would slip
// fe80::1%eth0 past the link-local check and fd00::1%eth0 past an operator's
// fd00::/8. The zone names an interface, not a place, and is dropped first.
func TestAZoneIsDroppedBeforeAnyCheck(t *testing.T) {
	t.Parallel()

	if dialable(netip.MustParseAddr("fe80::1%eth0"), allowAll) {
		t.Error("a zoned link-local address was dialable")
	}

	var got netip.Addr
	dialable(netip.MustParseAddr("fd00::1%eth0"), func(a netip.Addr) bool { got = a; return true })
	if got != netip.MustParseAddr("fd00::1") {
		t.Errorf("Allow saw %v, want fd00::1 with no zone", got)
	}
}

// Proxy stays nil. With ProxyFromEnvironment, Control would vet the proxy's
// address instead of the server's, and the guard would check the wrong thing.
func TestTheEnvironmentsProxyIsNeverUsed(t *testing.T) {
	t.Parallel()

	tr, ok := newTransport(Policy{Allow: allowAll}).(*http.Transport)
	if !ok {
		t.Fatalf("newTransport returned %T", tr)
	}
	if tr.Proxy != nil {
		t.Error("the transport has a Proxy, so Control checks the proxy rather than the target")
	}
}

// --- TLS ---

func TestAServerCertificateNotInTheRootsIsRefused(t *testing.T) {
	t.Parallel()

	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(s.Close)

	if _, err := get(t, clientFor(Policy{Allow: allowAll}, ""), s.URL); err == nil {
		t.Error("an untrusted certificate was accepted")
	}
	if _, err := get(t, clientFor(Policy{Allow: allowAll, RootCAs: trusting(s)}, ""), s.URL); err != nil {
		t.Errorf("a trusted certificate was refused: %v", err)
	}
}

// --- the token ---

func TestTheTokenArrivesAsABearerOverHTTPS(t *testing.T) {
	t.Parallel()

	var srv counting
	s := httptest.NewTLSServer(srv.handler(nil))
	t.Cleanup(s.Close)

	c := clientFor(Policy{Allow: allowAll, RootCAs: trusting(s)}, "tok-123")
	if _, err := get(t, c, s.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(srv.auth) != 1 || srv.auth[0] != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", srv.auth)
	}
}

// Over plain http the token would cross the network readable, so the request
// is not sent at all, and its body is closed as a RoundTripper must.
func TestTheTokenIsNeverSentOverHTTP(t *testing.T) {
	t.Parallel()

	var srv counting
	s := httptest.NewServer(srv.handler(nil))
	t.Cleanup(s.Close)

	body := &closeTracker{Reader: strings.NewReader(`{"jsonrpc":"2.0"}`)}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientFor(Policy{Allow: allowAll}, "tok-123").Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}

	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "only sent over https") {
		t.Errorf("err = %v, want ErrRefused naming https", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("the request reached the server")
	}
	if !body.closed.Load() {
		t.Error("the refused request's body was not closed")
	}
}

type closeTracker struct {
	io.Reader
	closed atomic.Bool
}

func (c *closeTracker) Close() error { c.closed.Store(true); return nil }

// With no token there is nothing to protect, so plain http is fine — and no
// Authorization header appears from nowhere.
func TestNoTokenSendsNoAuthorization(t *testing.T) {
	t.Parallel()

	var srv counting
	s := httptest.NewServer(srv.handler(nil))
	t.Cleanup(s.Close)

	if _, err := get(t, clientFor(Policy{Allow: allowAll}, ""), s.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(srv.auth) != 1 || srv.auth[0] != "" {
		t.Errorf("Authorization = %q, want none", srv.auth)
	}
}

// A RoundTripper must not modify the request it was given.
func TestTheBearerLeavesTheCallersRequestAlone(t *testing.T) {
	t.Parallel()

	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(s.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientFor(Policy{Allow: allowAll, RootCAs: trusting(s)}, "tok-123").Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("the caller's request now carries Authorization %q", got)
	}
}

// A redirect would carry the token to wherever the server pointed: the bearer
// is added to every request, including the redirected one. So none is
// followed, and the target never hears from the brain.
func TestARedirectIsRefusedBeforeTheTokenTravels(t *testing.T) {
	t.Parallel()

	var target counting
	elsewhere := httptest.NewTLSServer(target.handler(nil))
	t.Cleanup(elsewhere.Close)

	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)

	c := clientFor(Policy{Allow: allowAll, RootCAs: trusting(origin)}, "tok-123")
	_, err := get(t, c, origin.URL)

	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "may not redirect") {
		t.Errorf("err = %v, want ErrRefused naming the redirect", err)
	}
	if target.hits.Load() != 0 {
		t.Errorf("the redirect target received %d requests, with Authorization %q", target.hits.Load(), target.auth)
	}
}

// Every redirect is refused, token or not, so the rule does not depend on
// remembering which servers have one.
func TestARedirectIsRefusedWithoutAToken(t *testing.T) {
	t.Parallel()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	t.Cleanup(s.Close)

	if _, err := get(t, clientFor(Policy{Allow: allowAll}, ""), s.URL); !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

// --- the size cap ---

func readAllFrom(t *testing.T, size int) ([]byte, error) {
	t.Helper()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.Copy(w, io.LimitReader(zeroes{}, int64(size)))
	}))
	t.Cleanup(s.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL, http.NoBody)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	resp, err := clientFor(Policy{Allow: allowAll}, "").Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestAReplyOfExactlyTheCapIsRead(t *testing.T) {
	t.Parallel()

	got, err := readAllFrom(t, maxBodyBytes)
	if err != nil || len(got) != maxBodyBytes {
		t.Errorf("read %d bytes, err %v; want all %d", len(got), err, maxBodyBytes)
	}
}

func TestAReplyOneByteOverTheCapIsRefused(t *testing.T) {
	t.Parallel()

	got, err := readAllFrom(t, maxBodyBytes+1)
	if !errors.Is(err, errTooLarge) {
		t.Errorf("err = %v, want errTooLarge", err)
	}
	if len(got) > maxBodyBytes {
		t.Errorf("read %d bytes, more than the cap", len(got))
	}
}

// The cap is on the body a server sends, not on how the caller reads it:
// small reads add up the same way.
func TestTheCapCountsAcrossSmallReads(t *testing.T) {
	t.Parallel()

	l := &limited{body: io.NopCloser(bytes.NewReader(make([]byte, 10))), left: 4}
	var total int
	buf := make([]byte, 3)
	for {
		n, err := l.Read(buf)
		total += n
		if err != nil {
			if !errors.Is(err, errTooLarge) {
				t.Errorf("err = %v, want errTooLarge", err)
			}
			break
		}
	}
	if total != 4 {
		t.Errorf("read %d bytes, want exactly the 4 allowed", total)
	}
}

func TestClosingTheCappedBodyClosesTheReal(t *testing.T) {
	t.Parallel()

	inner := &closeTracker{Reader: strings.NewReader("x")}
	if err := (&limited{body: inner, left: 1}).Close(); err != nil || !inner.closed.Load() {
		t.Errorf("close: %v, inner closed %v", err, inner.closed.Load())
	}
}

// --- the deadline ---

// go-sdk sends follow-up requests on contexts of its own after ours has
// ended, so the deadline is enforced below it: a request begun after the
// deadline never leaves.
func TestARequestAfterTheDeadlineNeverLeaves(t *testing.T) {
	t.Parallel()

	var srv counting
	s := httptest.NewServer(srv.handler(nil))
	t.Cleanup(s.Close)

	c := newClient(newTransport(Policy{Allow: allowAll}), "", time.Now().Add(-time.Second))
	_, err := get(t, c, s.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if srv.hits.Load() != 0 {
		t.Error("a request after the deadline reached the server")
	}
}

// A request in flight when the deadline passes is cut off then, however long
// the server would have held it.
func TestARequestInFlightEndsAtTheDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(release) })

	start := time.Now()
	_, err := get(t, newClient(newTransport(Policy{Allow: allowAll}), "", start.Add(200*time.Millisecond)), s.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v with a 200ms deadline", time.Since(start))
	}
}

// The context lives until the body is closed. Cancelling it when RoundTrip
// returned would cut every streamed reply off after its headers.
func TestTheBodyStaysReadableUntilClosed(t *testing.T) {
	t.Parallel()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("after the headers"))
	}))
	t.Cleanup(s.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newClient(newTransport(Policy{Allow: allowAll}), "", time.Now().Add(time.Minute)).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "after the headers" {
		t.Errorf("body = %q, %v", body, err)
	}
}

// Closing the body releases the context, so a finished request does not hold
// a timer until the deadline.
func TestClosingTheBodyCancelsItsContext(t *testing.T) {
	t.Parallel()

	var cancelled atomic.Bool
	c := &cancelling{ReadCloser: io.NopCloser(strings.NewReader("")), cancel: func() { cancelled.Store(true) }}
	if err := c.Close(); err != nil || !cancelled.Load() {
		t.Errorf("close: %v, cancelled %v", err, cancelled.Load())
	}
}
