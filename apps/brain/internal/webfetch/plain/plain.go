package plain

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"

	"golang.org/x/net/html/charset"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
)

const (
	maxBody   = 2 << 20
	userAgent = "Openarity (+https://github.com/LaplacianAI/openarity)"
)

type fetcher struct{ client *http.Client }

func New(client *http.Client) webfetch.Fetcher { return fetcher{client: client} }

func (f fetcher) Fetch(ctx context.Context, url string) (webfetch.Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return webfetch.Page{}, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.client.Do(req) //nolint:gosec // G704: webfetch.NewClient's dialer refuses what allow does, and link-local always
	if err != nil {
		return webfetch.Page{}, fmt.Errorf("the page could not be read: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return webfetch.Page{}, fmt.Errorf("the page answered %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		media = "of no type it names"
	}
	html, ok := readable(media)
	if !ok {
		return webfetch.Page{}, fmt.Errorf("the page is %s, which web_fetch does not read", media)
	}

	limited := &io.LimitedReader{R: resp.Body, N: maxBody + 1}
	decoded, err := charset.NewReader(limited, contentType)
	if err != nil {
		return webfetch.Page{}, fmt.Errorf("the page's encoding could not be read: %w", err)
	}
	body, err := io.ReadAll(decoded)
	if err != nil {
		return webfetch.Page{}, fmt.Errorf("reading the page: %w", err)
	}

	page := webfetch.Page{URL: resp.Request.URL.String(), Markdown: string(body), Cut: limited.N == 0}
	if html {
		if page.Title, page.Markdown, err = toMarkdown(page.Markdown, resp.Request.URL); err != nil {
			return webfetch.Page{}, fmt.Errorf("the page could not be turned into markdown: %w", err)
		}
	}
	return page, nil
}

func readable(media string) (html, ok bool) {
	switch media {
	case "text/html", "application/xhtml+xml":
		return true, true
	case "text/plain", "text/markdown", "application/json":
		return false, true
	}
	return false, false
}
