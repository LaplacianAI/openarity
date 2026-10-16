package hub

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// loopbackOnly lets a test reach its own TLS server and nothing else. Every
// test that is about the address check uses Public instead.
func loopbackOnly(a netip.Addr) bool { return a.IsLoopback() }

type server struct {
	*httptest.Server
	hits atomic.Int64
}

func newServer(t *testing.T, h http.HandlerFunc) *server {
	t.Helper()

	s := &server{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) roots() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return pool
}

func ok(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "the skill") }

func get(t *testing.T, c *http.Client, url string) (string, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	return string(body), err
}

func wantRefused(t *testing.T, err error, mentions string) {
	t.Helper()

	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if !strings.Contains(err.Error(), mentions) {
		t.Errorf("err = %q, want it to mention %q", err, mentions)
	}
}

// The success path, so that every refusal below is a refusal and not a client
// that can reach nothing at all.
func TestAnAllowedHostIsFetched(t *testing.T) {
	t.Parallel()

	s := newServer(t, ok)
	c := NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: s.roots()})

	if body, err := get(t, c, s.URL); err != nil || body != "the skill" {
		t.Errorf("got %q, %v", body, err)
	}
}

// The check runs on the address being dialled. A host allowed by name that
// resolves to loopback is refused, which is DNS rebinding: the name is fine,
// the answer is not.
func TestAnAllowedNameThatResolvesToAPrivateAddressIsRefusedAtDial(t *testing.T) {
	t.Parallel()

	s := newServer(t, ok)
	port := s.Listener.Addr().String()
	port = port[strings.LastIndex(port, ":")+1:]
	c := NewClient(Policy{Hosts: []string{"localhost", "127.0.0.1"}, Allow: Public, RootCAs: s.roots()})

	for _, url := range []string{"https://localhost:" + port, "https://127.0.0.1:" + port, "https://127.1:" + port} {
		t.Run(url, func(t *testing.T) {
			_, err := get(t, c, url)
			if strings.Contains(url, "127.1") {
				// Not on the list by name, so refused before any dial.
				wantRefused(t, err, "not an allowed source")
				return
			}
			wantRefused(t, err, "is not a public address")
		})
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
}

// A host off the list is refused before a connection is opened: the server
// that would have answered never hears from us.
func TestAHostOffTheListIsNeverContacted(t *testing.T) {
	t.Parallel()

	s := newServer(t, ok)
	c := NewClient(Policy{Hosts: []string{"github.com"}, Allow: loopbackOnly, RootCAs: s.roots()})

	_, err := get(t, c, s.URL)
	wantRefused(t, err, "not an allowed source")
	if n := s.hits.Load(); n != 0 {
		t.Errorf("an unlisted host was contacted %d times", n)
	}
}

func TestPlainHTTPIsRefused(t *testing.T) {
	t.Parallel()

	c := NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly})
	_, err := get(t, c, "http://127.0.0.1:1/skill.zip")
	wantRefused(t, err, "http://127.0.0.1:1 is not an allowed source")
}

// An allowed host may redirect, and the redirect is checked like the first
// request: off the list, and the target is never contacted.
func TestARedirectOffTheListIsRefusedBeforeItIsFollowed(t *testing.T) {
	t.Parallel()

	target := newServer(t, ok)
	targetPort := target.URL[strings.LastIndex(target.URL, ":")+1:]
	origin := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://localhost:"+targetPort+"/", http.StatusFound)
	})
	c := NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: origin.roots()})

	_, err := get(t, c, origin.URL)
	wantRefused(t, err, "https://localhost:"+targetPort+" is not an allowed source")
	if n := target.hits.Load(); n != 0 {
		t.Errorf("the redirect target was contacted %d times", n)
	}
}

// A redirect down to plain HTTP would send the rest of the exchange in the
// clear, to whatever answers on the path.
func TestARedirectToPlainHTTPIsRefused(t *testing.T) {
	t.Parallel()

	origin := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/skill.zip", http.StatusFound)
	})
	c := NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: origin.roots()})

	_, err := get(t, c, origin.URL)
	wantRefused(t, err, "http://127.0.0.1:1 is not an allowed source")
}

// hops redirects to itself until it has sent n redirects, then answers.
func hops(t *testing.T, n int) *server {
	t.Helper()

	return newServer(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ := strconv.Atoi(r.URL.Query().Get("sent"))
		if sent < n {
			http.Redirect(w, r, fmt.Sprintf("/?sent=%d", sent+1), http.StatusFound)
			return
		}
		ok(w, r)
	})
}

// GitHub's API and codeload redirect once or twice; a loop never ends. Five
// redirects are followed, the sixth is refused.
func TestFiveRedirectsAreFollowedAndTheSixthIsRefused(t *testing.T) {
	t.Parallel()

	five := hops(t, maxRedirects)
	c := NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: five.roots()})
	if body, err := get(t, c, five.URL); err != nil || body != "the skill" {
		t.Errorf("five redirects: got %q, %v", body, err)
	}

	six := hops(t, maxRedirects+1)
	c = NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: six.roots()})
	_, err := get(t, c, six.URL)
	wantRefused(t, err, "redirects")
}

// Names are compared case-insensitively, as DNS compares them. A list entry
// written in capitals must not quietly allow nothing.
func TestHostsAreComparedCaseInsensitively(t *testing.T) {
	t.Parallel()

	var reached []string
	a := allowlist{
		hosts: map[string]bool{"codeload.github.com": true},
		next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			reached = append(reached, r.URL.Host)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
	}
	for _, host := range []string{"codeload.github.com", "CodeLoad.GitHub.COM"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+host+"/x", nil)
		res, err := a.RoundTrip(req)
		if err != nil {
			t.Errorf("%s: %v", host, err)
			continue
		}
		_ = res.Body.Close()
	}

	upper := NewClient(Policy{Hosts: []string{"CODELOAD.GITHUB.COM"}, Allow: Public})
	if !allowlistOf(t, upper).hosts["codeload.github.com"] {
		t.Error("a list entry in capitals allowed nothing")
	}
	if len(reached) != 2 {
		t.Errorf("reached %v, want both spellings", reached)
	}
}

// A refused request still has its body closed: the transport owns it once
// RoundTrip is called, whatever it answers.
func TestARefusedRequestClosesItsBody(t *testing.T) {
	t.Parallel()

	body := &closeRecorder{Reader: strings.NewReader("x")}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://unlisted.example/x", body)
	a := allowlist{hosts: map[string]bool{}, next: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("a refused request reached the transport")
		return nil, nil
	})}

	res, err := a.RoundTrip(req)
	if res != nil {
		_ = res.Body.Close()
	}
	wantRefused(t, err, "not an allowed source")
	if !body.closed {
		t.Error("the body of a refused request was left open")
	}
}

// Through a proxy the address dialled is the proxy's, and the address check
// never sees the target. This is the property a well-meant
// http.DefaultTransport.Clone() would undo, so it is asserted as a value.
func TestTheClientNeverUsesAProxy(t *testing.T) {
	t.Parallel()

	c := NewClient(Policy{Hosts: []string{"github.com"}, Allow: Public})
	next := allowlistOf(t, c).next
	transport, isTransport := next.(*http.Transport)
	if !isTransport {
		t.Fatalf("the allowlist wraps %T, not an *http.Transport", next)
	}
	if transport.Proxy != nil {
		t.Error("the transport has a Proxy, so the dial check sees the proxy rather than the target")
	}
	if transport.TLSClientConfig.MinVersion < 0x0303 {
		t.Errorf("TLS minimum is %#x, below 1.2", transport.TLSClientConfig.MinVersion)
	}
}

// The spec promises the whole import is bounded; a download that trickles
// forever would otherwise hold a request open for as long as it likes.
func TestAnImportIsBoundedInTime(t *testing.T) {
	t.Parallel()

	if c := NewClient(Policy{Allow: Public}); c.Timeout != 30*time.Second {
		t.Errorf("client timeout = %v, want 30s", c.Timeout)
	}
}

// Every range here is somewhere a request must not go. The standard library
// calls several of them global unicast; the ones marked are why denied exists.
func TestOnlyThePublicInternetIsPublic(t *testing.T) {
	t.Parallel()

	for addr, want := range map[string]bool{
		// Public.
		"1.1.1.1":              true,
		"140.82.112.3":         true, // github.com
		"2606:4700:4700::1111": true,
		"::ffff:1.1.1.1":       true,
		"100.63.255.255":       true, // just below CGNAT
		"100.128.0.0":          true, // just above it
		"198.17.255.255":       true, // just below benchmarking
		"198.20.0.0":           true, // just above it

		// What IsGlobalUnicast and IsPrivate already refuse.
		"127.0.0.1":              false,
		"::1":                    false,
		"10.0.0.5":               false,
		"172.16.0.1":             false,
		"192.168.1.1":            false,
		"169.254.169.254":        false, // the metadata service
		"fe80::1":                false,
		"fd00:ec2::254":          false, // the metadata service over IPv6
		"0.0.0.0":                false,
		"::":                     false,
		"224.0.0.1":              false,
		"ff02::1":                false,
		"255.255.255.255":        false,
		"::ffff:10.0.0.5":        false,
		"::ffff:169.254.169.254": false,

		// What only denied refuses.
		"0.1.2.3":            false, // "this network"
		"100.64.0.1":         false, // CGNAT, a cloud's internal range
		"100.127.255.255":    false,
		"192.0.0.1":          false,
		"198.18.0.1":         false,
		"198.19.255.255":     false,
		"240.0.0.1":          false,
		"64:ff9b::a9fe:a9fe": false, // NAT64 of the metadata service
		"64:ff9b::a00:5":     false, // NAT64 of 10.0.0.5
		"64:ff9b:1::1":       false,
		"2001::a9fe:a9fe":    false, // Teredo
		"2001:db8::1":        false, // documentation
		"2002:a9fe:a9fe::1":  false, // 6to4 of the metadata service
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v, want %v", addr, got, want)
		}
	}
}

// A zone is not a way around the check: fe80::1%eth0 is still link-local.
func TestAZonedAddressIsNotPublic(t *testing.T) {
	t.Parallel()

	if Public(netip.MustParseAddr("fe80::1%eth0")) {
		t.Error("a zoned link-local address counted as public")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// allowlistOf is the check every request passes through. A client whose
// transport is anything else has lost it, and nothing below would hold.
func allowlistOf(t *testing.T, c *http.Client) allowlist {
	t.Helper()

	a, isAllowlist := c.Transport.(allowlist)
	if !isAllowlist {
		t.Fatalf("the client's transport is %T, not the allowlist", c.Transport)
	}
	return a
}
