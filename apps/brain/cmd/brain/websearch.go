package main

import (
	"fmt"
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/brave"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/firecrawl"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/searxng"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/tavily"
)

func newSearchers(client *http.Client) func(p websearch.Provider, url, key string) (websearch.Searcher, error) {
	return func(p websearch.Provider, url, key string) (websearch.Searcher, error) {
		switch p {
		case websearch.Searxng, websearch.FirecrawlOSS:
			if url == "" {
				return nil, fmt.Errorf("%s needs the address of its instance", p)
			}
		case websearch.Brave, websearch.Tavily, websearch.FirecrawlCloud:
		default:
			return nil, fmt.Errorf("%q is not a search provider", string(p))
		}

		switch p {
		case websearch.Searxng:
			return searxng.New(url, client), nil
		case websearch.Brave:
			return brave.New(brave.Host, key, client), nil
		case websearch.Tavily:
			return tavily.New(tavily.Host, key, client), nil
		case websearch.FirecrawlCloud:
			return firecrawl.New(p, firecrawl.Host, key, client), nil
		case websearch.FirecrawlOSS:
			return firecrawl.New(p, url, key, client), nil
		}
		return nil, fmt.Errorf("%q is not a search provider", string(p))
	}
}
