package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

// hostsAsked answers every request with an empty page and records where it
// was sent, so a test can see which host a provider asks without a network.
type hostsAsked struct{ urls []string }

func (h *hostsAsked) RoundTrip(r *http.Request) (*http.Response, error) {
	h.urls = append(h.urls, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path)
	return &http.Response{
		StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)),
		Header: http.Header{}, Request: r,
	}, nil
}

// The hosted providers are asked at their own hosts, whatever URL a row
// holds; the self-hosted ones at the row's.
func TestEachProviderIsAskedWhereItLives(t *testing.T) {
	for p, want := range map[websearch.Provider]string{
		websearch.Brave:          "https://api.search.brave.com/res/v1/web/search",
		websearch.Tavily:         "https://api.tavily.com/search",
		websearch.FirecrawlCloud: "https://api.firecrawl.dev/v2/search",
		websearch.Searxng:        "http://search.internal:8080/search",
		websearch.FirecrawlOSS:   "http://search.internal:8080/v2/search",
	} {
		t.Run(string(p), func(t *testing.T) {
			asked := &hostsAsked{}
			s, err := newSearchers(&http.Client{Transport: asked})(p, "http://search.internal:8080", "k")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Search(t.Context(), "x", 1); err != nil {
				t.Fatal(err)
			}
			if len(asked.urls) != 1 || asked.urls[0] != want {
				t.Errorf("asked %v, want %s", asked.urls, want)
			}
		})
	}
}

func TestOnlyAProviderThatCanSearchIsBuilt(t *testing.T) {
	for _, tc := range []struct {
		p    websearch.Provider
		url  string
		want string
	}{
		{"none", "", `"none" is not a search provider`},
		{"bing", "", `"bing" is not a search provider`},
		{websearch.Searxng, "", "searxng needs the address of its instance"},
		{websearch.FirecrawlOSS, "", "firecrawl-oss needs the address of its instance"},
	} {
		s, err := newSearchers(&http.Client{})(tc.p, tc.url, "")
		if err == nil || err.Error() != tc.want || s != nil {
			t.Errorf("(%q, %q) = %v, %v; want %q", tc.p, tc.url, s, err, tc.want)
		}
	}
}
