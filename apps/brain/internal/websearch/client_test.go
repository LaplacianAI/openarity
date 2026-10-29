package websearch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch/websearchtest"
)

const key = "BSA-sekrit-0042"

func anywhere(netip.Addr) bool { return true }

func get(t *testing.T, url string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func fetch(t *testing.T, client *http.Client, url string) ([]byte, error) {
	t.Helper()
	return websearch.Fetch(client, websearch.Brave, get(t, url))
}

func TestFetchReturnsTheBody(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{"results":[]}`)

	body, err := fetch(t, websearch.NewClient(anywhere), srv.URL)
	if err != nil || string(body) != `{"results":[]}` {
		t.Errorf("body %q, err %v", body, err)
	}
}

// A provider's refusal can quote the key back; the error names the provider
// and the status, and nothing it said.
func TestAKeyIsNeverInAnError(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusUnauthorized, `{"error":"bad key `+key+`"}`)

	_, err := fetch(t, websearch.NewClient(anywhere), srv.URL)
	if err == nil || err.Error() != "brave answered 401" {
		t.Errorf("err = %v, want %q", err, "brave answered 401")
	}

	for _, body := range []string{`Unauthorized: ` + key, `{"web":{"results":"` + key + `"}}`} {
		var v struct {
			Web struct {
				Results []struct{} `json:"results"`
			} `json:"web"`
		}
		err := websearch.Decode(websearch.Brave, []byte(body), &v)
		if err == nil || err.Error() != "brave sent something that is not search results" {
			t.Errorf("Decode(%q) = %v", body, err)
		}
	}
}

func TestEveryStatusOutsideTwoHundredIsAnError(t *testing.T) {
	for _, status := range []int{http.StatusMultipleChoices, http.StatusTooManyRequests, http.StatusBadGateway} {
		srv := websearchtest.Serve(t, status, `{}`)

		_, err := fetch(t, websearch.NewClient(anywhere), srv.URL)
		if want := "brave answered " + itoa(status); err == nil || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
	for _, status := range []int{http.StatusOK, http.StatusNoContent, 299} {
		srv := websearchtest.Serve(t, status, ``)

		if _, err := fetch(t, websearch.NewClient(anywhere), srv.URL); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
	}
}

func TestAnAddressTheDeploymentRefusesIsRefusedAtDial(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{}`)
	public := func(a netip.Addr) bool { return !a.IsLoopback() && !a.IsPrivate() }

	_, err := fetch(t, websearch.NewClient(public), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "is not an address web search may reach") {
		t.Errorf("err = %v, want the address refused", err)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("%d requests arrived", n)
	}
}

// Link-local is cloud metadata, refused whatever the deployment allows.
func TestLinkLocalIsRefusedEvenWhenEverythingIsAllowed(t *testing.T) {
	for _, url := range []string{"http://169.254.169.254", "http://[fe80::1]", "http://[::ffff:169.254.169.254]"} {
		_, err := fetch(t, websearch.NewClient(anywhere), url)
		if err == nil || !strings.Contains(err.Error(), "is not an address web search may reach") {
			t.Errorf("%s: err = %v, want link-local refused", url, err)
		}
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	target := websearchtest.Serve(t, http.StatusOK, `{}`)
	srv := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	t.Cleanup(srv.Close)

	if _, err := fetch(t, websearch.NewClient(anywhere), srv.URL); err == nil {
		t.Error("a redirect was followed to an answer")
	}
	if n := len(target.Requests()); n != 0 {
		t.Errorf("the redirect's target was asked %d times", n)
	}
}

// A proxy would dial for the client, and the guard would only ever see the
// proxy's address. Checked on the transport: net/http reads the proxy
// settings once per process, so a test cannot set them and watch.
func TestNoProxyIsUsed(t *testing.T) {
	tr, ok := websearch.NewClient(anywhere).Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Errorf("transport %T has a proxy; want none", websearch.NewClient(anywhere).Transport)
	}
}

func TestABodyPastTheCapIsRefused(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, strings.Repeat("a", 2<<20+1))

	if _, err := fetch(t, websearch.NewClient(anywhere), srv.URL); err == nil || err.Error() != "brave sent more than 2 MiB" {
		t.Errorf("err = %v, want the body refused at its cap", err)
	}
}

func TestABodyAtTheCapIsRead(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, strings.Repeat("a", 2<<20))

	body, err := fetch(t, websearch.NewClient(anywhere), srv.URL)
	if err != nil || len(body) != 2<<20 {
		t.Errorf("read %d bytes, err %v; want all 2 MiB", len(body), err)
	}
}

func TestTheClientGivesUpAfterFifteenSeconds(t *testing.T) {
	if got := websearch.NewClient(anywhere).Timeout; got != 15*time.Second {
		t.Errorf("timeout = %s, want 15s", got)
	}
}

func TestPostJSONSendsTheBodyAndTheKey(t *testing.T) {
	srv := websearchtest.Serve(t, http.StatusOK, `{}`)

	req, err := websearch.PostJSON(t.Context(), srv.URL+"/search", key, map[string]any{"query": "x", "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := websearch.Fetch(websearchtest.Client(), websearch.Tavily, req); err != nil {
		t.Fatal(err)
	}
	got := srv.Requests()[0]
	if got.Method != "POST" || got.Auth != "Bearer "+key || got.Body["query"] != "x" || got.Body["limit"] != float64(3) {
		t.Errorf("asked %+v", got)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", req.Header.Get("Content-Type"))
	}
}

// A self-hosted instance may run with no key; then no Authorization is sent,
// not an empty bearer.
func TestPostJSONWithoutAKeySendsNoAuth(t *testing.T) {
	req, err := websearch.PostJSON(t.Context(), "http://firecrawl:3002/v2/search", "", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Errorf("Authorization = %q with no key, want none", req.Header.Get("Authorization"))
	}
}

func TestDecodeReadsWhatItIsGiven(t *testing.T) {
	var v struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	if err := websearch.Decode(websearch.Searxng, []byte(`{"results":[{"title":"a"}]}`), &v); err != nil || v.Results[0].Title != "a" {
		t.Errorf("decoded %+v, err %v", v, err)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
