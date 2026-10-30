package plain

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

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

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return webfetch.Page{}, fmt.Errorf("reading the page: %w", err)
	}
	cut := len(raw) > maxBody
	if cut {
		raw = raw[:maxBody]
	}
	text, err := decode(raw, contentType, cut)
	if err != nil {
		return webfetch.Page{}, fmt.Errorf("the page's encoding could not be read: %w", err)
	}

	page := webfetch.Page{URL: resp.Request.URL.String(), Markdown: text, Cut: cut}
	if html {
		if page.Title, page.Markdown, err = toMarkdown(page.Markdown, resp.Request.URL); err != nil {
			return webfetch.Page{}, fmt.Errorf("the page could not be turned into markdown: %w", err)
		}
	}
	return page, nil
}

// decode keeps a charset the header names. Without one, a body that is valid
// UTF-8 is read as UTF-8: the sniffer reads only the first KiB and takes an
// ASCII start for windows-1252. Only a body that is not UTF-8 is sniffed, by
// its meta tag or its bytes. A cut can split a character; that half is dropped.
func decode(raw []byte, contentType string, cut bool) (string, error) {
	_, params, _ := mime.ParseMediaType(contentType)
	if _, declared := params["charset"]; !declared {
		whole := raw
		for i := 0; cut && i < utf8.UTFMax-1 && len(whole) > 0 && !utf8.Valid(whole); i++ {
			whole = whole[:len(whole)-1]
		}
		if utf8.Valid(whole) {
			return string(whole), nil
		}
	}
	enc, _, _ := charset.DetermineEncoding(raw, contentType)
	return enc.NewDecoder().String(string(raw))
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
