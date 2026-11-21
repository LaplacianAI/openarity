package daytona

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// What Daytona can say about egress, from its API and docs
// (https://www.daytona.io/docs/en/network-limits/):
//
//   - networkBlockAll: no outbound traffic at all.
//   - networkAllowList: up to 10 IPv4 CIDRs. Every port on them is open; a
//     port cannot be named.
//   - domainAllowList: up to 100 hostnames, "*." wildcards allowed, no ports.
//     Daytona enforces it on HTTPS traffic by the host name of each request,
//     through a proxy that terminates TLS (its CA is mounted in the sandbox at
//     /etc/daytona/netleash/ca.crt). What it does with other protocols is not
//     documented.
//   - The two lists are mutually exclusive: at most one is non-empty.
//   - Tier 1 and 2 organizations on Daytona cloud cannot override the
//     organization's policy per sandbox at all.
//
// So on Daytona cloud a Spec.Egress maps to:
//
//	no entries                     -> networkBlockAll
//	hostnames, every port 443      -> domainAllowList (ports on a host cannot
//	                                  be narrowed, but HTTPS is all it opens)
//	anything else                  -> networkAllowList: each host resolved to
//	                                  its IPv4 /32s, each IP literal its /32,
//	                                  every port on them open (wider than
//	                                  asked, as on self-hosted below)
//
// The domain list is the narrower and is used whenever it can say all of it;
// one entry it cannot say (IMAPS on 993, an IP literal) moves the whole room
// to the address list, since the two are exclusive. An organization that
// cannot override its policy per sandbox gets Daytona's own refusal.
//
// Self-hosted Daytona (the open-source runner, probed at v0.187.0) accepts
// domainAllowList and does not enforce it: a room made with one reached every
// host. Only the CIDR list and block-all are enforced, at the IP level and on
// every port. So against a self-hosted Daytona a host name is resolved here,
// to the IPv4 addresses it has now, and sent as /32 networks. That reaches
// imap.gmail.com:993 and the few addresses behind it, on every port, but only
// while those addresses still serve that name: they are looked up when the
// room is made, and a room is short-lived. A wildcard cannot be resolved and
// is refused.

const (
	maxDomains = 100
	maxCIDRs   = 10
	httpsPort  = 443
)

var domainPattern = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

// network is the room's network fields. A public room on Daytona cloud has
// neither list and is not blocked: Daytona's runners are not on any network
// of ours, and its own sandbox network keeps a room off its hosts. A
// self-hosted Daytona runs on someone's own network, and can only allow at
// most ten CIDRs, never deny: "every public address" is about thirty of them,
// so a public room there is refused rather than made able to reach the
// host's network.
func (c *Client) network(ctx context.Context, s sandbox.Spec) (network, error) {
	switch s.Network {
	case sandbox.NetworkListed:
		return mapEgress(ctx, s.Egress, c.DomainFirewall, c.Resolve)
	case sandbox.NetworkPublic:
		if len(s.Egress) > 0 {
			return network{}, refuse("a public room lists no hosts")
		}
		if !c.DomainFirewall {
			return network{}, refuse("a self-hosted Daytona cannot keep a room off private addresses, so it cannot run a workspace; choose sandboxd, E2B or Daytona cloud")
		}
		return network{}, nil
	default:
		return network{}, refuse("no network %q", s.Network)
	}
}

// network is the three network fields of Daytona's create request.
type network struct {
	blockAll bool
	cidrs    []string
	domains  []string
}

// mapEgress turns egress into Daytona's network fields. domainFirewall says
// the Daytona enforces domain allow lists (its cloud); otherwise host names
// are resolved with resolve.
func mapEgress(ctx context.Context, egress []sandbox.HostPort, domainFirewall bool, resolve Resolver) (network, error) {
	if len(egress) == 0 {
		return network{blockAll: true}, nil
	}
	domains := map[string]bool{}
	cidrs := map[string]bool{}
	byName := domainFirewall && allHTTPSNames(egress)
	for _, e := range egress {
		if e.Port < 1 || e.Port > 65535 {
			return network{}, refuse("egress port %d is not a port", e.Port)
		}
		host := strings.ToLower(strings.TrimSpace(e.Host))
		if addr, err := netip.ParseAddr(host); err == nil {
			if !addr.Is4() {
				return network{}, refuse("egress %q is not an IPv4 address; Daytona allows IPv4 only", e.Host)
			}
			cidrs[addr.String()+"/32"] = true
			continue
		}
		if !domainPattern.MatchString(host) {
			return network{}, refuse("egress host %q is not a domain name Daytona can allow", e.Host)
		}
		if byName {
			domains[host] = true
			continue
		}
		addrs, err := resolveV4(ctx, resolve, host)
		if err != nil {
			return network{}, err
		}
		for _, a := range addrs {
			cidrs[a.String()+"/32"] = true
		}
	}
	n := network{domains: keys(domains), cidrs: keys(cidrs)}
	if len(n.domains) > maxDomains {
		return network{}, refuse("%d egress hosts: Daytona allows %d domains", len(n.domains), maxDomains)
	}
	if len(n.cidrs) > maxCIDRs {
		return network{}, refuse("%d egress addresses: Daytona allows %d networks", len(n.cidrs), maxCIDRs)
	}
	return n, nil
}

// allHTTPSNames says every entry is a host name on 443: what a domain allow
// list can say whole.
func allHTTPSNames(egress []sandbox.HostPort) bool {
	for _, e := range egress {
		if _, err := netip.ParseAddr(strings.TrimSpace(e.Host)); err == nil || e.Port != httpsPort {
			return false
		}
	}
	return true
}

// A Resolver looks up the addresses of a host name.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func resolveV4(ctx context.Context, resolve Resolver, host string) ([]netip.Addr, error) {
	if strings.HasPrefix(host, "*.") {
		return nil, refuse("egress %q: a wildcard cannot be turned into addresses", host)
	}
	addrs, err := resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("daytona: resolve egress host %q: %w", host, err)
	}
	var v4 []netip.Addr
	for _, a := range addrs {
		if a = a.Unmap(); a.Is4() {
			v4 = append(v4, a)
		}
	}
	if len(v4) == 0 {
		return nil, refuse("egress host %q has no IPv4 address", host)
	}
	return v4, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", sandbox.ErrRefused, fmt.Sprintf(format, a...))
}
