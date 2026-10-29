package tavily_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/tavily"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/websearchtest"
)

const (
	key   = "tvly-sekrit-0042"
	reply = `{"query":"q","results":[
		{"title":"Invoices due","url":"https://a.example/due","content":"Three are due","score":0.9},
		{"title":"Billing","url":"https://b.example/","content":"How billing works","score":0.5}]}`
)

func TestTheHostIsTavilys(t *testing.T) {
	if tavily.Host != "https://api.tavily.com" {
		t.Errorf("Host = %s", tavily.Host)
	}
}

func TestItAsksAsTavilyExpects(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	if _, err := tavily.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "invoices due", 3); err != nil {
		t.Fatal(err)
	}
	want := websearchtest.Asked{
		Method: "POST", Path: "/search", Auth: "Bearer " + key, Query: map[string][]string{},
		Body: map[string]any{"query": "invoices due", "max_results": float64(3), "search_depth": "basic"},
	}
	if got := srv.Requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("asked %+v, want %+v", got, want)
	}
}

func TestItReadsItsHits(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := tavily.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits) {
		t.Errorf("hits = %+v, err %v; want %+v", got, err, websearchtest.TwoHits)
	}
}

func TestNoMoreHitsThanAskedFor(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := tavily.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 1)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits[:1]) {
		t.Errorf("hits = %+v, err %v; want the first alone", got, err)
	}
}

func TestNoHitsIsAnEmptyList(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"results":[]}`)

	got, err := tavily.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("hits = %#v, err %v; want an empty list", got, err)
	}
}

func TestAnythingElseIsNotSearchResults(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `not json`)

	_, err := tavily.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err == nil || err.Error() != "tavily sent something that is not search results" {
		t.Errorf("err = %v", err)
	}
}
