package tavily

import (
	"context"
	"net/http"
	"net/url"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

const Host = "https://api.tavily.com"

type searcher struct {
	base, key string
	client    *http.Client
}

func New(base, key string, client *http.Client) websearch.Searcher {
	return searcher{base: base, key: key, client: client}
}

type query struct {
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
}

func (s searcher) Search(ctx context.Context, q string, n int) ([]websearch.Hit, error) {
	endpoint, err := url.JoinPath(s.base, "search")
	if err != nil {
		return nil, err
	}
	req, err := websearch.PostJSON(ctx, endpoint, s.key, query{Query: q, MaxResults: n, SearchDepth: "basic"})
	if err != nil {
		return nil, err
	}
	body, err := websearch.Fetch(s.client, websearch.Tavily, req)
	if err != nil {
		return nil, err
	}

	var r struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := websearch.Decode(websearch.Tavily, body, &r); err != nil {
		return nil, err
	}
	found := r.Results[:min(n, len(r.Results))]
	hits := make([]websearch.Hit, 0, len(found))
	for _, x := range found {
		hits = append(hits, websearch.Hit{Title: x.Title, URL: x.URL, Snippet: x.Content})
	}
	return hits, nil
}
