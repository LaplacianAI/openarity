package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

// A Firecrawl scrapes at the address it searches at: the cloud at its own
// host whatever the row holds, a self-hosted one at the row's.
func TestAFirecrawlScrapesWhereItLives(t *testing.T) {
	for p, want := range map[websearch.Provider]string{
		websearch.FirecrawlCloud: "https://api.firecrawl.dev/v2/scrape",
		websearch.FirecrawlOSS:   "http://search.internal:8080/v2/scrape",
	} {
		t.Run(string(p), func(t *testing.T) {
			asked := &hostsAsked{}
			f, err := newScrapers(&http.Client{Transport: asked})(p, "http://search.internal:8080", "k")
			if err != nil || f == nil {
				t.Fatalf("scraper %v, err %v", f, err)
			}
			if _, err := f.Fetch(t.Context(), "https://go.dev/"); err != nil {
				t.Fatal(err)
			}
			if len(asked.urls) != 1 || asked.urls[0] != want {
				t.Errorf("asked %v, want %s", asked.urls, want)
			}
		})
	}
}

// Any other provider has no scraper, and that is not an error: the page is
// read plainly.
func TestOnlyAFirecrawlHasAScraper(t *testing.T) {
	for _, p := range []websearch.Provider{websearch.Searxng, websearch.Brave, websearch.Tavily, "none"} {
		f, err := newScrapers(&http.Client{Transport: &hostsAsked{}})(p, "http://search.internal:8080", "k")
		if f != nil || err != nil {
			t.Errorf("%s: scraper %v, err %v; want neither", p, f, err)
		}
	}
}

func TestASelfHostedFirecrawlWithNoAddressHasNoScraper(t *testing.T) {
	f, err := newScrapers(&http.Client{})(websearch.FirecrawlOSS, "", "")
	if f != nil || err == nil || err.Error() != "firecrawl-oss needs the address of its instance" {
		t.Errorf("scraper %v, err %v", f, err)
	}
}

type fakeResolver struct {
	answers map[string][]netip.Addr
	err     error
	asked   []string
}

func (r *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	r.asked = append(r.asked, network+" "+host)
	return r.answers[host], r.err
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(s))
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func TestAPublicHostIsLetThrough(t *testing.T) {
	r := &fakeResolver{answers: map[string][]netip.Addr{"go.dev": addrs("216.239.32.21", "2001:4860:4802:32::15")}}

	if err := publicHost(r)(t.Context(), "go.dev"); err != nil {
		t.Errorf("err = %v", err)
	}
	if !slices.Equal(r.asked, []string{"ip go.dev"}) {
		t.Errorf("asked %v, want one lookup for both families", r.asked)
	}
}

// One private answer is enough to refuse: Firecrawl may dial any of them.
func TestAHostWithAnyPrivateAnswerIsRefused(t *testing.T) {
	for name, answers := range map[string][]netip.Addr{
		"private":     addrs("10.0.0.5"),
		"one of two":  addrs("216.239.32.21", "192.168.1.1"),
		"loopback v6": addrs("::1"),
		"metadata":    addrs("169.254.169.254"),
		"mapped":      addrs("::ffff:127.0.0.1"),
		"no answers":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeResolver{answers: map[string][]netip.Addr{"inside.example": answers}}

			err := publicHost(r)(t.Context(), "inside.example")
			if err == nil || err.Error() != "inside.example is not a public address" {
				t.Errorf("err = %v, want inside.example is not a public address", err)
			}
		})
	}
}

// An address in the URL is judged as it stands; nothing is looked up.
func TestAnAddressIsJudgedWithoutALookup(t *testing.T) {
	r := &fakeResolver{}
	public := publicHost(r)

	if err := public(t.Context(), "216.239.32.21"); err != nil {
		t.Errorf("a public address: %v", err)
	}
	for _, host := range []string{"127.0.0.1", "169.254.169.254", "fe80::1", "10.1.2.3"} {
		if err := public(t.Context(), host); err == nil || err.Error() != host+" is not a public address" {
			t.Errorf("%s: err = %v", host, err)
		}
	}
	if len(r.asked) != 0 {
		t.Errorf("looked up %v", r.asked)
	}
}

// What the resolver said is not passed on: the host is named, and the
// failure is what the model can act on.
func TestAHostThatDoesNotResolveIsRefused(t *testing.T) {
	r := &fakeResolver{err: errors.New("lookup nowhere.example on 10.0.0.2:53: no such host")}

	err := publicHost(r)(t.Context(), "nowhere.example")
	if err == nil || err.Error() != "nowhere.example could not be resolved" {
		t.Errorf("err = %v, want nowhere.example could not be resolved", err)
	}
}

// The plain fetch dials public addresses only, whatever the search client may
// reach; the scrape is judged by the same predicate before Firecrawl sees it.
func TestTheWiredFetchReachesPublicAddressesOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "inside")
	}))
	t.Cleanup(srv.Close)
	everything := &http.Client{}

	fetch := newFetch(everything, &fakeResolver{})

	if _, err := fetch.Plain.Fetch(t.Context(), srv.URL); err == nil {
		t.Error("the plain fetch read a loopback page")
	}
	if err := fetch.Public(t.Context(), "127.0.0.1"); err == nil {
		t.Error("the scrape check let a loopback address through")
	}
	asked := &hostsAsked{}
	f, err := newFetch(&http.Client{Transport: asked}, &fakeResolver{}).Scrapers(websearch.FirecrawlOSS, "http://firecrawl.internal:3002", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(t.Context(), "https://go.dev/"); err != nil || len(asked.urls) != 1 {
		t.Errorf("the scraper asked %v, err %v; want it on the search client", asked.urls, err)
	}
}

// A zone never matches a prefix, so a zoned address read as public whatever it
// was: the NAT64 form of the metadata address with a zone passed. A zoned
// address is a link's, never the internet's.
func TestAZonedAddressIsRefused(t *testing.T) {
	r := &fakeResolver{answers: map[string][]netip.Addr{"zoned.example": {netip.MustParseAddr("2001:4860:4802:32::15%eth0")}}}
	public := publicHost(r)

	for _, host := range []string{"64:ff9b::a9fe:a9fe%eth0", "fe80::1%eth0", "2001:4860:4802:32::15%eth0", "zoned.example"} {
		if err := public(t.Context(), host); err == nil || err.Error() != host+" is not a public address" {
			t.Errorf("%s: err = %v", host, err)
		}
	}
}

// A scrape renders the page on Firecrawl's side and takes longer than a
// search: its client waits 45 s, on the same guarded transport, and the
// search client keeps its own 15 s.
func TestAScrapeWaitsLongerThanASearch(t *testing.T) {
	search := &http.Client{Timeout: 15 * time.Second, Transport: &hostsAsked{}}

	scrape := scrapeClient(search)

	if scrape.Timeout != 45*time.Second || scrape.Transport != search.Transport || search.Timeout != 15*time.Second {
		t.Errorf("scrape %v on %T, search %v; want 45s on the same transport, search untouched", scrape.Timeout, scrape.Transport, search.Timeout)
	}
}

// deadlineAsked records the deadline each request carried: the client's
// Timeout reaches the transport as the request's context deadline.
type deadlineAsked struct{ left []time.Duration }

func (d *deadlineAsked) RoundTrip(r *http.Request) (*http.Response, error) {
	if dl, ok := r.Context().Deadline(); ok {
		d.left = append(d.left, time.Until(dl))
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}, Request: r}, nil
}

// The wired scraper waits the scrape's 45 s, not the search client's 15 s.
func TestTheWiredScraperWaitsTheScrapesTime(t *testing.T) {
	asked := &deadlineAsked{}
	search := &http.Client{Timeout: 15 * time.Second, Transport: asked}

	f, err := newFetch(search, &fakeResolver{}).Scrapers(websearch.FirecrawlCloud, "", "k")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Fetch(t.Context(), "https://go.dev/")
	if len(asked.left) != 1 || asked.left[0] < 30*time.Second {
		t.Errorf("the scrape had %v left; want about 45s", asked.left)
	}
}
