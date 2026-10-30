package websearch

import (
	"context"
	"encoding/json"
	"fmt"
)

type Provider string

const (
	Searxng        Provider = "searxng"
	Brave          Provider = "brave"
	Tavily         Provider = "tavily"
	FirecrawlCloud Provider = "firecrawl-cloud"
	FirecrawlOSS   Provider = "firecrawl-oss"
)

type Hit struct {
	Title   string
	URL     string
	Snippet string
}

type Searcher interface {
	Search(ctx context.Context, q string, n int) ([]Hit, error)
}

func Decode(p Provider, body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s sent something that is not search results", p)
	}
	return nil
}
