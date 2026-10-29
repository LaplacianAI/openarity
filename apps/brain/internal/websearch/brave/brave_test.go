package brave_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/brave"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/websearchtest"
)

const (
	key   = "BSA-sekrit-0042"
	reply = `{"type":"search","web":{"results":[
		{"title":"Invoices due","url":"https://a.example/due","description":"Three are due"},
		{"title":"Billing","url":"https://b.example/","description":"How billing works"}]}}`
)

func TestTheHostIsBraves(t *testing.T) {
	if brave.Host != "https://api.search.brave.com" {
		t.Errorf("Host = %s", brave.Host)
	}
}

func TestItAsksAsBraveExpects(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	if _, err := brave.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "invoices due", 3); err != nil {
		t.Fatal(err)
	}
	want := websearchtest.Asked{
		Method: "GET", Path: "/res/v1/web/search", Token: key,
		Query: map[string][]string{"q": {"invoices due"}, "count": {"3"}},
	}
	if got := srv.Requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("asked %+v, want %+v", got, want)
	}
}

func TestItReadsItsHits(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := brave.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits) {
		t.Errorf("hits = %+v, err %v; want %+v", got, err, websearchtest.TwoHits)
	}
}

func TestNoMoreHitsThanAskedFor(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	got, err := brave.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 1)
	if err != nil || !reflect.DeepEqual(got, websearchtest.TwoHits[:1]) {
		t.Errorf("hits = %+v, err %v; want the first alone", got, err)
	}
}

// A search Brave found nothing for has no web section at all.
func TestNoHitsIsAnEmptyList(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"type":"search"}`)

	got, err := brave.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("hits = %#v, err %v; want an empty list", got, err)
	}
}

func TestAnythingElseIsNotSearchResults(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"web":{"results":"`+key+`"}}`)

	_, err := brave.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", 5)
	if err == nil || err.Error() != "brave sent something that is not search results" {
		t.Errorf("err = %v", err)
	}
}

// Search is a port anyone may call; a count below one finds nothing rather
// than panicking the worker that asked.
func TestACountBelowOneFindsNothing(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, reply)

	for _, n := range []int{0, -1} {
		got, err := brave.New(srv.URL, key, websearchtest.Client()).Search(t.Context(), "x", n)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("n %d: hits %#v, err %v; want an empty list", n, got, err)
		}
	}
}
