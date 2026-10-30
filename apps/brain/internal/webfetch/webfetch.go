package webfetch

import "context"

type Page struct {
	URL      string
	Title    string
	Markdown string
	Cut      bool
}

type Fetcher interface {
	Fetch(ctx context.Context, url string) (Page, error)
}
