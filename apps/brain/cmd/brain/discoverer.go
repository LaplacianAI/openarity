package main

import (
	"net/netip"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

const discoveryTimeout = 15 * time.Second

// newDiscoverer is MCP discovery and the clients runs call servers with. With
// a store and a secret store that writes, a server that signs in has its
// tokens renewed; without, it is called with the token it signed in with.
func newDiscoverer(cfg *config.Config, secretStore secrets.Store, dbStore *store.Store) *discovery.Discoverer {
	d := discovery.New(secretStore, discovery.Policy{Allow: reachable(cfg.MCPPrivateNetworks)}, discoveryTimeout)
	if w, ok := secretStore.(secrets.Writer); ok && dbStore != nil {
		d.SignsInWith(oauthConnections{store: dbStore, writer: w})
	}
	return d
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
