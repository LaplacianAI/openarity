package firecrawl_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/firecrawl"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/websearchtest"
)

const (
	key   = "fc-sekrit-0042"
	reply = `{"success":true,"data":{"web":[
		{"title":"Invoices due","url":"https://a.example/due","description":"Three are due"},
		{"title":"Billing","url":"https://b.example/","description":"How billing works"}]}}`
)

func TestTheHostIsFirecrawls(t *testing.T) {
	if firecrawl.Host != "https://api.firecrawl.dev" {
		t.Errorf("Host = %s", firecrawl.Host)
	}
}

func TestItAsksAsFirecrawlV2Expects(t *testing.T) {
	for _, p := range []websearch.Provider{websearch.FirecrawlCloud, websearch.FirecrawlOSS} {
		t.Run(string(p), func(t *testing.T) {
			srv := websearchtest.Serve(t, http.StatusOK, reply)

			if _, err := firecrawl.New(p, srv.URL, key, websearchtest.Client()).Search(t.Context(), "invoices due", 3); err != nil {
				t.Fatal(err)
			}
			want := websearchtest.Asked{
				Method: "POST", Path: "/v2/search", Auth: "Bearer " + key, Query: map[string][]string{},
				Body: map[string]any{"query": "invoices due", "limit": float64(3)},
			}
			if got := srv.Requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Errorf("asked %+v, want %+v", got, want)
			}
		})
	}
}

// A self-hosted instance may run with no key, and under a path of its own.
func TestASelfHostedInstanceWithoutAKeySendsNoAuth(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	if _, err := firecrawl.New(websearch.FirecrawlOSS, srv.URL+"/fc/", "", websearchtest.Client()).Search(t.Context(), "x", 5); err != nil {
		t.Fatal(err)
	}
	if got := srv.Requests()[0]; got.Auth != "" || got.Path != "/fc/v2/search" {
		t.Errorf("asked %s with Authorization %q, want /fc/v2/search with none", got.Path, got.Auth)
	}
}

func TestItReadsItsHits(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := firecrawl.New(websearch.FirecrawlCloud, srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits) {
		t.Errorf("hits = %+v, err %v; want %+v", got, err, websearchtest.TwoHits)
	}
}

func TestNoMoreHitsThanAskedFor(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := firecrawl.New(websearch.FirecrawlCloud, srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 1)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits[:1]) {
		t.Errorf("hits = %+v, err %v; want the first alone", got, err)
	}
}

func TestNoHitsIsAnEmptyList(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"success":true,"data":{}}`)

	got, err := firecrawl.New(websearch.FirecrawlCloud, srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("hits = %#v, err %v; want an empty list", got, err)
	}
}

// Errors name which Firecrawl, so a team knows whose key or instance to check.
func TestErrorsNameTheProviderAsTheTeamSetIt(t *testing.T) {
	for _, p := range []websearch.Provider{websearch.FirecrawlCloud, websearch.FirecrawlOSS} {
		srv := websearchtest.Serve(t, http.StatusPaymentRequired, `{}`)

		_, err := firecrawl.New(p, srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
		if want := string(p) + " answered 402"; err == nil || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}
