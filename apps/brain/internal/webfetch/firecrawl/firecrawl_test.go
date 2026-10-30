package firecrawl_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/webfetch/firecrawl"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/websearchtest"
)

const (
	key   = "fc-sekrit-0042"
	asked = "https://go.dev/doc"
	reply = `{"success":true,"data":{"markdown":"# Documentation\n\nRead me first",
		"metadata":{"title":"Documentation - The Go Programming Language","sourceURL":"https://go.dev/doc","url":"https://go.dev/doc/"}}}`
)

func TestAScrapeAsksForMarkdownWithTheKey(t *testing.T) {
	for _, p := range []websearch.Provider{websearch.FirecrawlCloud, websearch.FirecrawlOSS} {
		t.Run(string(p), func(t *testing.T) {
			srv := websearchtest.Serve(t, http.StatusOK, reply)

			if _, err := firecrawl.New(p, srv.URL, key, websearchtest.Client()).Fetch(t.Context(), asked); err != nil {
				t.Fatal(err)
			}
			want := websearchtest.Asked{
				Method: "POST", Path: "/v2/scrape", Auth: "Bearer " + key, Query: map[string][]string{},
				Body: map[string]any{"url": asked, "formats": []any{"markdown"}},
			}
			if got := srv.Requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Errorf("asked %+v, want %+v", got, want)
			}
		})
	}
}

// The page's address is where Firecrawl ended after its redirects
// (metadata.url), never the one asked for (sourceURL): the runtime judges it.
// The markdown is Firecrawl's, passed through; the cut is the runtime's.
func TestAScrapeReturnsThePage(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := firecrawl.New(websearch.FirecrawlCloud, srv.URL, key, websearchtest.Client()).Fetch(t.Context(), asked)
	want := webfetch.Page{
		URL:      "https://go.dev/doc/",
		Title:    "Documentation - The Go Programming Language",
		Markdown: "# Documentation\n\nRead me first",
	}
	if err != nil || got != want {
		t.Errorf("page = %+v, err %v; want %+v", got, err, want)
	}
}

// A redirect inside Firecrawl's network is where a private address hides, so
// the requested address is never taken for the final one.
func TestARedirectIsReportedWhereItEnded(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"success":true,"data":{"markdown":"x",
		"metadata":{"sourceURL":"https://go.dev/r","url":"http://169.254.169.254/latest"}}}`)

	got, err := firecrawl.New(websearch.FirecrawlCloud, srv.URL, key, websearchtest.Client()).Fetch(t.Context(), asked)
	if err != nil || got.URL != "http://169.254.169.254/latest" {
		t.Errorf("page = %+v, err %v; want the final address", got, err)
	}
}

// With no final address there is nothing to judge: the page says none, and
// the runtime refuses it, rather than trusting the address asked for.
func TestAPageWithNoFinalURLSaysNone(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"success":true,"data":{"markdown":"hi","metadata":{"sourceURL":"https://go.dev/doc"}}}`)

	got, err := firecrawl.New(websearch.FirecrawlCloud, srv.URL, key, websearchtest.Client()).Fetch(t.Context(), asked)
	if err != nil || got.URL != "" || got.Markdown != "hi" {
		t.Errorf("page = %+v, err %v; want no address", got, err)
	}
}

// The body of a failure may echo the key or the account; only the status
// reaches the agent.
func TestAFirecrawlErrorCarriesItsStatusNotItsText(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusPaymentRequired, `{"success":false,"error":"payment required: your key sk-0042"}`)

	_, err := firecrawl.New(websearch.FirecrawlCloud, srv.URL, key, websearchtest.Client()).Fetch(t.Context(), asked)
	if err == nil || err.Error() != "firecrawl-cloud answered 402" {
		t.Errorf("err = %v, want firecrawl-cloud answered 402", err)
	}
}

// A self-hosted instance may run with no key, and under a path of its own.
func TestTheSelfHostedScraperUsesItsOwnBase(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	if _, err := firecrawl.New(websearch.FirecrawlOSS, srv.URL+"/fc/", "", websearchtest.Client()).Fetch(t.Context(), asked); err != nil {
		t.Fatal(err)
	}
	if got := srv.Requests()[0]; got.Auth != "" || got.Path != "/fc/v2/scrape" {
		t.Errorf("asked %s with Authorization %q, want /fc/v2/scrape with none", got.Path, got.Auth)
	}
}

// websearch.Decode would say "not search results"; a page is not one.
func TestAReplyThatIsNotAPageSaysSo(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `<html>gateway</html>`)

	_, err := firecrawl.New(websearch.FirecrawlOSS, srv.URL, "", websearchtest.Client()).Fetch(t.Context(), asked)
	if err == nil || err.Error() != "firecrawl-oss sent something that is not a page" {
		t.Errorf("err = %v, want firecrawl-oss sent something that is not a page", err)
	}
	if err != nil && strings.Contains(err.Error(), "gateway") {
		t.Errorf("err %q carries the reply's text", err)
	}
}
