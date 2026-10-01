package discovery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

const (
	dialTimeout   = 10 * time.Second
	headerTimeout = 15 * time.Second
	maxBodyBytes  = 8 << 20
)

var (
	ErrRefused  = errors.New("refused")
	errTooLarge = errors.New("the server's reply is over 8 MiB")
)

var linkLocal = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
}

type Policy struct {
	Allow   func(netip.Addr) bool
	RootCAs *x509.CertPool
}

func newTransport(p Policy) http.RoundTripper {
	dialer := &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !dialable(ap.Addr(), p.Allow) {
				return fmt.Errorf("%w: %s is not an address discovery may reach", ErrRefused, address)
			}
			return nil
		},
	}

	return &http.Transport{
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: headerTimeout,
		ForceAttemptHTTP2:     true,
	}
}

func dialable(a netip.Addr, allow func(netip.Addr) bool) bool {
	a = a.Unmap().WithZone("")
	for _, p := range linkLocal {
		if p.Contains(a) {
			return false
		}
	}
	return allow(a)
}

func newClient(base http.RoundTripper, token string, deadline time.Time) *http.Client {
	var rt http.RoundTripper = bounded{deadline: deadline, next: capped{next: base}}
	if token != "" {
		rt = bearer{token: token, next: rt}
	}

	return &http.Client{
		Transport: rt,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("%w: an MCP server may not redirect", ErrRefused)
		},
	}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, fmt.Errorf("%w: a bearer token is only sent over https", ErrRefused)
	}

	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

type capped struct {
	next http.RoundTripper
}

func (c capped) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}

	resp.Body = &limited{body: resp.Body, left: maxBodyBytes}
	return resp, nil
}

type limited struct {
	body io.ReadCloser
	left int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.left == 0 {
		var one [1]byte
		n, err := l.body.Read(one[:])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}

	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.body.Read(p)
	l.left -= int64(n)
	return n, err
}

func (l *limited) Close() error { return l.body.Close() }

type bounded struct {
	deadline time.Time
	next     http.RoundTripper
}

func (b bounded) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithDeadline(r.Context(), b.deadline)
	resp, err := b.next.RoundTrip(r.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}

	resp.Body = &cancelling{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelling struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelling) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
