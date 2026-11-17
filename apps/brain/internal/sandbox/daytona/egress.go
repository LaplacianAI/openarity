package daytona

import (
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
// So a Spec.Egress is honoured only where Daytona can keep to it:
//
//	no entries                     -> networkBlockAll
//	hostnames, every port 443      -> domainAllowList (ports on a host cannot
//	                                  be narrowed, but HTTPS is all it opens)
//	IP literals, any port          -> networkAllowList /32 (every port on that
//	                                  address is open: wider than asked)
//	a hostname on another port     -> ErrRefused: the domain list is HTTPS
//	                                  only, and the CIDR list has no names
//	hostnames and IPs together     -> ErrRefused: the lists are exclusive
//
// A mail server over IMAPS or SMTPS (993, 465) is therefore refused rather
// than made with a room that cannot reach it.

const (
	maxDomains = 100
	maxCIDRs   = 10
	httpsPort  = 443
)

var domainPattern = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

// network is the three network fields of Daytona's create request.
type network struct {
	blockAll bool
	cidrs    []string
	domains  []string
}

func mapEgress(egress []sandbox.HostPort) (network, error) {
	if len(egress) == 0 {
		return network{blockAll: true}, nil
	}
	domains := map[string]bool{}
	cidrs := map[string]bool{}
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
		if e.Port != httpsPort {
			return network{}, refuse(
				"egress %s:%d: Daytona's domain allow list covers HTTPS only, and its IP allow list cannot name a host",
				host, e.Port)
		}
		domains[host] = true
	}
	if len(domains) > 0 && len(cidrs) > 0 {
		return network{}, refuse("Daytona takes a domain allow list or an IP allow list, not both")
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
