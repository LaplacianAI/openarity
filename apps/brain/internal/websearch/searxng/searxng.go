package searxng

import (
	"context"
	"net/http"
	"net/url"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

type searcher struct {
	base   string
	client *http.Client
}

func New(base string, client *http.Client) websearch.Searcher {
	return searcher{base: base, client: client}
}

func (s searcher) Search(ctx context.Context, q string, n int) ([]websearch.Hit, error) {
	endpoint, err := url.JoinPath(s.base, "search")
	if err != nil {
		return nil, err
	}
	query := url.Values{"q": {q}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	body, err := websearch.Fetch(s.client, websearch.Searxng, req)
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
	if err := websearch.Decode(websearch.Searxng, body, &r); err != nil {
		return nil, err
	}
	hits := make([]websearch.Hit, 0, min(n, len(r.Results)))
	for _, x := range r.Results[:min(n, len(r.Results))] {
		hits = append(hits, websearch.Hit{Title: x.Title, URL: x.URL, Snippet: x.Content})
	}
	return hits, nil
}
