package main

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch/firecrawl"
	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch/plain"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	searchfirecrawl "github.com/LaplacianAI/openarity/apps/brain/internal/websearch/firecrawl"
)

type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// newFetch reads a page plainly through a client that dials public addresses
// only, whatever MCP_PRIVATE_NETWORKS allows, and through a team's Firecrawl
// on searchClient, which reaches it as web search does.
func newFetch(searchClient *http.Client, r resolver) *runtime.FetchDeps {
	return &runtime.FetchDeps{
		Plain:    plain.New(webfetch.NewClient(hub.Public)),
		Scrapers: newScrapers(searchClient),
		Public:   publicHost(r),
	}
}

func newScrapers(client *http.Client) runtime.Scrapers {
	return func(p websearch.Provider, url, key string) (webfetch.Fetcher, error) {
		switch p {
		case websearch.FirecrawlCloud:
			return firecrawl.New(p, searchfirecrawl.Host, key, client), nil
		case websearch.FirecrawlOSS:
			if url == "" {
				return nil, fmt.Errorf("%s needs the address of its instance", p)
			}
			return firecrawl.New(p, url, key, client), nil
		case websearch.Searxng, websearch.Brave, websearch.Tavily:
		}
		return nil, nil //nolint:nilnil // no scraper is not a failure: the page is read plainly
	}
}

// publicHost refuses a host unless every address it names is public: Firecrawl
// dials from its own network and may pick any of them.
func publicHost(r resolver) func(ctx context.Context, host string) error {
	return func(ctx context.Context, host string) error {
		addrs := []netip.Addr{}
		if a, err := netip.ParseAddr(host); err == nil {
			addrs = append(addrs, a)
		} else {
			found, err := r.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return fmt.Errorf("%s could not be resolved", host)
			}
			addrs = found
		}
		if len(addrs) == 0 {
			return fmt.Errorf("%s is not a public address", host)
		}
		for _, a := range addrs {
			// A zone never matches a prefix, so hub.Public would read a zoned
			// address as public; a zoned address is a link's, never the internet's.
			if a.Zone() != "" || !hub.Public(a) {
				return fmt.Errorf("%s is not a public address", host)
			}
		}
		return nil
	}
}
