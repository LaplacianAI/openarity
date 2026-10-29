package firecrawl

import (
	"context"
	"net/http"
	"net/url"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

const Host = "https://api.firecrawl.dev"

type searcher struct {
	p         websearch.Provider
	base, key string
	client    *http.Client
}

func New(p websearch.Provider, base, key string, client *http.Client) websearch.Searcher {
	return searcher{p: p, base: base, key: key, client: client}
}

type query struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s searcher) Search(ctx context.Context, q string, n int) ([]websearch.Hit, error) {
	endpoint, err := url.JoinPath(s.base, "v2", "search")
	if err != nil {
		return nil, err
	}
	req, err := websearch.PostJSON(ctx, endpoint, s.key, query{Query: q, Limit: n})
	if err != nil {
		return nil, err
	}
	body, err := websearch.Fetch(s.client, s.p, req)
	if err != nil {
		return nil, err
	}

	var r struct {
		Data struct {
			Web []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"web"`
		} `json:"data"`
	}
	if err := websearch.Decode(s.p, body, &r); err != nil {
		return nil, err
	}
	found := r.Data.Web[:min(n, len(r.Data.Web))]
	hits := make([]websearch.Hit, 0, len(found))
	for _, x := range found {
		hits = append(hits, websearch.Hit{Title: x.Title, URL: x.URL, Snippet: x.Description})
	}
	return hits, nil
}
