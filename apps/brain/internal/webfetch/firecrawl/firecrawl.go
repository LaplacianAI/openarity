package firecrawl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

type fetcher struct {
	p         websearch.Provider
	base, key string
	client    *http.Client
}

func New(p websearch.Provider, base, key string, client *http.Client) webfetch.Fetcher {
	return fetcher{p: p, base: base, key: key, client: client}
}

// scrapeTimeout is what Firecrawl is given to read a page: less than the
// brain's scrape client waits, so a slow page ends as Firecrawl's answer, with
// its status, rather than as the brain's own timeout.
const scrapeTimeout = 40 * time.Second

type scrape struct {
	URL     string   `json:"url"`
	Formats []string `json:"formats"`
	Timeout int64    `json:"timeout"`
}

func (f fetcher) Fetch(ctx context.Context, address string) (webfetch.Page, error) {
	endpoint, err := url.JoinPath(f.base, "v2", "scrape")
	if err != nil {
		return webfetch.Page{}, err
	}
	req, err := websearch.PostJSON(ctx, endpoint, f.key, scrape{URL: address, Formats: []string{"markdown"}, Timeout: scrapeTimeout.Milliseconds()})
	if err != nil {
		return webfetch.Page{}, err
	}
	body, err := websearch.Fetch(f.client, f.p, req)
	if err != nil {
		return webfetch.Page{}, err
	}

	var r struct {
		Data struct {
			Markdown string `json:"markdown"`
			Metadata struct {
				Title string `json:"title"`
				// URL is where the page ended after Firecrawl's redirects;
				// sourceURL, the address asked for, is never taken for it.
				URL string `json:"url"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return webfetch.Page{}, fmt.Errorf("%s sent something that is not a page", f.p)
	}
	return webfetch.Page{URL: r.Data.Metadata.URL, Title: r.Data.Metadata.Title, Markdown: r.Data.Markdown}, nil
}
