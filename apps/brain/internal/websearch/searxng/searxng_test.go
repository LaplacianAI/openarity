package searxng_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/searxng"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/websearchtest"
)

const reply = `{"results":[
	{"title":"Invoices due","url":"https://a.example/due","content":"Three are due"},
	{"title":"Billing","url":"https://b.example/","content":"How billing works"}]}`

func TestItAsksAsSearXNGExpects(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	if _, err := searxng.New(srv.URL, websearchtest.Client()).Search(t.Context(), "invoices due", 3); err != nil {
		t.Fatal(err)
	}
	want := websearchtest.Asked{
		Method: "GET", Path: "/search",
		Query: map[string][]string{"q": {"invoices due"}, "format": {"json"}},
	}
	if got := srv.Requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("asked %+v, want %+v", got, want)
	}
}

// An instance may sit under a path of its own, with or without a slash.
func TestAnInstanceKeepsItsPath(t *testing.T) {
	for _, under := range []string{"/searx", "/searx/"} {
		srv := websearchtest.Serve(t, http.StatusOK, reply)

		if _, err := searxng.New(srv.URL+under, websearchtest.Client()).Search(t.Context(), "x", 1); err != nil {
			t.Fatal(err)
		}
		if got := srv.Requests()[0].Path; got != "/searx/search" {
			t.Errorf("under %s asked %s, want /searx/search", under, got)
		}
	}
}

func TestItReadsItsHits(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := searxng.New(srv.URL, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits) {
		t.Errorf("hits = %+v, err %v; want %+v", got, err, websearchtest.TwoHits)
	}
}

// SearXNG takes no count, so its page is cut to what was asked for.
func TestNoMoreHitsThanAskedFor(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := searxng.New(srv.URL, websearchtest.Client()).Search(t.Context(), "x", 1)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits[:1]) {
		t.Errorf("hits = %+v, err %v; want the first alone", got, err)
	}
}

func TestNoHitsIsAnEmptyList(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"results":[]}`)

	got, err := searxng.New(srv.URL, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("hits = %#v, err %v; want an empty list", got, err)
	}
}

func TestAnythingElseIsNotSearchResults(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `<html>Too many requests</html>`)

	_, err := searxng.New(srv.URL, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err == nil || err.Error() != "searxng sent something that is not search results" {
		t.Errorf("err = %v", err)
	}
}

func TestARefusalNamesSearXNG(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusForbidden, `{}`)

	if _, err := searxng.New(srv.URL, websearchtest.Client()).Search(t.Context(), "x", 5); err == nil || err.Error() != "searxng answered 403" {
		t.Errorf("err = %v", err)
	}
}
