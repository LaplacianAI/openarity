package e2b

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// network is E2B's network config for a sandbox. AllowPublicTraffic is always
// sent, and false: E2B's own default may be public, and a room's ports are
// reachable only with the traffic token.
type network struct {
	AllowPublicTraffic bool     `json:"allowPublicTraffic"`
	AllowOut           []string `json:"allowOut,omitempty"`
	DenyOut            []string `json:"denyOut"`
}

const denyAll = "0.0.0.0/0"

// egress maps the spec's host:port pairs onto E2B's rules, which are not
// host:port. allowOut takes CIDRs, bare IPs and domain names, and has no
// ports. A name is matched only on port 80 (the Host header) and port 443 (the
// TLS SNI); on every other port E2B filters by address alone
// (https://docs.e2b.dev/sandbox/internet-access). So:
//
//   - Everything is denied (0.0.0.0/0) and what follows is allowed.
//   - A host on port 80 or 443 becomes a domain rule. It allows both web
//     ports of that name and nothing else. A wildcard such as *.example.com
//     is accepted.
//   - A host on any other port (IMAP 993, SMTP 465) cannot be matched by name.
//     It is resolved here, now, and each IPv4 address is allowed. That is
//     wider than asked in two ways: every port of those addresses is open,
//     not just the one, and an address the host moves to later is not. A
//     name that has no address, or a wildcard, is ErrRefused, because
//     nothing can be allowed for it.
//   - An IP literal is allowed as that address, whatever its port.
//
// Any domain rule also leaves 8.8.8.8 fully open, so the sandbox can resolve
// names; that is E2B's doing and cannot be turned off while a domain is
// allowed.
func (c *Client) egress(ctx context.Context, hp []sandbox.HostPort) (network, error) {
	n := network{DenyOut: []string{denyAll}}
	allow := map[string]bool{}
	for _, h := range hp {
		if h.Host == "" || h.Port < 1 || h.Port > 65535 || strings.ContainsAny(h.Host, "/: ") {
			return n, fmt.Errorf("%w: %q is not a host and a port", sandbox.ErrRefused, fmt.Sprintf("%s:%d", h.Host, h.Port))
		}
		if ip := net.ParseIP(h.Host); ip != nil {
			allow[ip.String()] = true
			continue
		}
		if h.Port == 80 || h.Port == 443 {
			allow[strings.ToLower(h.Host)] = true
			continue
		}
		if strings.Contains(h.Host, "*") {
			return n, fmt.Errorf("%w: E2B can only match a wildcard host on ports 80 and 443, not %s:%d", sandbox.ErrRefused, h.Host, h.Port)
		}
		ips, err := c.Resolve(ctx, h.Host)
		if err != nil {
			return n, fmt.Errorf("e2b: resolve %s for egress: %w", h.Host, err)
		}
		if len(ips) == 0 {
			return n, fmt.Errorf("%w: %s has no IPv4 address E2B could allow", sandbox.ErrRefused, h.Host)
		}
		for _, ip := range ips {
			allow[ip] = true
		}
	}
	for a := range allow {
		n.AllowOut = append(n.AllowOut, a)
	}
	sort.Strings(n.AllowOut)
	return n, nil
}
