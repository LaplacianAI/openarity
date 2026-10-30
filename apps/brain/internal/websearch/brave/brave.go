package brave

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

const Host = "https://api.search.brave.com"

type searcher struct {
	base, key string
	client    *http.Client
}

func New(base, key string, client *http.Client) websearch.Searcher {
	return searcher{base: base, key: key, client: client}
}

func (s searcher) Search(ctx context.Context, q string, n int) ([]websearch.Hit, error) {
	endpoint, err := url.JoinPath(s.base, "res", "v1", "web", "search")
	if err != nil {
		return nil, err
	}
	query := url.Values{"q": {q}, "count": {strconv.Itoa(n)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", s.key)
	body, err := websearch.Fetch(s.client, websearch.Brave, req)
	if err != nil {
		return nil, err
	}

	var r struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := websearch.Decode(websearch.Brave, body, &r); err != nil {
		return nil, err
	}
	found := r.Web.Results[:min(max(n, 0), len(r.Web.Results))]
	hits := make([]websearch.Hit, 0, len(found))
	for _, x := range found {
		hits = append(hits, websearch.Hit{Title: x.Title, URL: x.URL, Snippet: x.Description})
	}
	return hits, nil
}
