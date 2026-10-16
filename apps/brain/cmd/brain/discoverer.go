package main

import (
	"net/netip"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
)

const discoveryTimeout = 15 * time.Second

func newDiscoverer(cfg *config.Config, secretStore secrets.Store) *discovery.Discoverer {
	return discovery.New(secretStore, discovery.Policy{Allow: reachable(cfg.MCPPrivateNetworks)}, discoveryTimeout)
}

func reachable(private []netip.Prefix) func(netip.Addr) bool {
	return func(a netip.Addr) bool {
		if hub.Public(a) {
			return true
		}
		for _, p := range private {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
}
