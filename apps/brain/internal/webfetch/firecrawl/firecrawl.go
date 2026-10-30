package firecrawl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

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

type scrape struct {
	URL     string   `json:"url"`
	Formats []string `json:"formats"`
}

func (f fetcher) Fetch(ctx context.Context, address string) (webfetch.Page, error) {
	endpoint, err := url.JoinPath(f.base, "v2", "scrape")
	if err != nil {
		return webfetch.Page{}, err
	}
	req, err := websearch.PostJSON(ctx, endpoint, f.key, scrape{URL: address, Formats: []string{"markdown"}})
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
				Title     string `json:"title"`
				SourceURL string `json:"sourceURL"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return webfetch.Page{}, fmt.Errorf("%s sent something that is not a page", f.p)
	}
	page := webfetch.Page{URL: r.Data.Metadata.SourceURL, Title: r.Data.Metadata.Title, Markdown: r.Data.Markdown}
	if page.URL == "" {
		page.URL = address
	}
	return page, nil
}
