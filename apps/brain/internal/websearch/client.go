package websearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
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
	timeout     = 15 * time.Second
	dialTimeout = 10 * time.Second
	maxBody     = 2 << 20
)

var linkLocal = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
}

func Transport(allow func(netip.Addr) bool, noun string) *http.Transport {
	dialer := &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !dialable(ap.Addr(), allow) {
				return fmt.Errorf("%s is not an address %s may reach", address, noun)
			}
			return nil
		},
	}
	return &http.Transport{
		DialContext:         dialer.DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: dialTimeout,
		ForceAttemptHTTP2:   true,
	}
}

func NewClient(allow func(netip.Addr) bool) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: Transport(allow, "web search"),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("a search provider may not redirect")
		},
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

func Fetch(client *http.Client, p Provider, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req) //nolint:gosec // G704: NewClient's dialer refuses what allow does, and link-local always
	if err != nil {
		return nil, fmt.Errorf("%s could not be reached: %w", p, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s answered %d", p, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("reading what %s sent: %w", p, err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%s sent more than 2 MiB", p)
	}
	return body, nil
}

func PostJSON(ctx context.Context, endpoint, key string, body any) (*http.Request, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req, nil
}
