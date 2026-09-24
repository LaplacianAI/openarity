package hub

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

const (
	fetchTimeout  = 30 * time.Second
	dialTimeout   = 10 * time.Second
	headerTimeout = 15 * time.Second
	maxRedirects  = 5
)

var ErrRefused = errors.New("refused")

var denied = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range denied {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

type Policy struct {
	Hosts   []string
	Allow   func(netip.Addr) bool
	RootCAs *x509.CertPool
}

type allowlist struct {
	hosts map[string]bool
	next  http.RoundTripper
}

func NewClient(p Policy) *http.Client {
	dialer := &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !p.Allow(ap.Addr()) {
				return fmt.Errorf("%w: %s is not a public address", ErrRefused, address)
			}
			return nil
		},
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: headerTimeout,
		ForceAttemptHTTP2:     true,
	}

	hosts := make(map[string]bool, len(p.Hosts))
	for _, h := range p.Hosts {
		hosts[strings.ToLower(h)] = true
	}

	return &http.Client{
		Transport: allowlist{hosts: hosts, next: transport},
		Timeout:   fetchTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("%w: more than %d redirects", ErrRefused, maxRedirects)
			}
			return nil
		},
	}
}

func (a allowlist) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || !a.hosts[strings.ToLower(r.URL.Hostname())] {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, fmt.Errorf("%w: %s://%s is not an allowed source", ErrRefused, r.URL.Scheme, r.URL.Host)
	}
	return a.next.RoundTrip(r)
}
