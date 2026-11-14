package gateway

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

const downloadToken = "123:secret-token"

// downloadProvider downloads as Telegram does: a lookup of the ref, then the
// file at the path the lookup gave, both under a token in the URL. Its server
// is the test's.
type downloadProvider struct {
	// noFetch is a provider that cannot fetch, so a download is the only way.
	noFetch
	base            string
	perFile, budget time.Duration
	// endless asks for the lookup again and again, never final.
	endless bool
}

func (d *downloadProvider) DownloadRequest(ref string, prev []byte, _ Credentials) (OutboundRequest, bool, error) {
	if prev == nil || d.endless {
		return OutboundRequest{URL: d.base + "/bot" + downloadToken + "/lookup", Body: []byte(ref)}, false, nil
	}
	return OutboundRequest{Method: http.MethodGet, URL: d.base + "/file/bot" + downloadToken + "/" + string(prev)}, true, nil
}

func (d *downloadProvider) FetchBudget() (time.Duration, time.Duration) { return d.perFile, d.budget }

// downloadServer answers a lookup with the file's path and the path with its
// bytes. A ref it does not know is a 404; "slow" sleeps past any budget.
func downloadServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/lookup"):
			var ref bytes.Buffer
			_, _ = ref.ReadFrom(r.Body)
			if ref.String() == "slow" {
				time.Sleep(300 * time.Millisecond)
			}
			if _, ok := files[ref.String()]; !ok {
				http.Error(w, "no such file", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte("files/" + ref.String()))
		case strings.HasSuffix(r.URL.Path, "/files/broken"):
			http.Error(w, "the file store is down", http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/file/bot"+downloadToken+"/files/"):
			_, _ = w.Write(files[strings.TrimPrefix(r.URL.Path, "/file/bot"+downloadToken+"/files/")])
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func downloading(t *testing.T, files map[string][]byte) (*Ingest, *downloadProvider, *fakeObjects, *bytes.Buffer) {
	t.Helper()
	srv := downloadServer(t, files)
	store := newFakeObjects()
	log := &bytes.Buffer{}
	g := NewIngest(store, store, srv.Client(), slog.New(slog.NewTextHandler(log, nil)))
	return g, &downloadProvider{noFetch: noFetch{&fetchProvider{}}, base: srv.URL, perFile: time.Second, budget: 2 * time.Second}, store, log
}

// A file the provider holds is followed through its requests to its bytes,
// and kept as any other: its type read from what arrived.
func TestADownloadedFileIsKept(t *testing.T) {
	t.Parallel()

	g, p, store, _ := downloading(t, map[string][]byte{"photo": pngBytes})
	stored, missed := g.Fetch(t.Context(), p, WebhookRequest{}, nil, uuid.New(), []Attachment{claim("photo", "photo-18.jpg")})
	if len(stored) != 1 || len(missed) != 0 {
		t.Fatalf("stored %+v, missed %v", stored, missed)
	}
	if s := stored[0]; s.MediaType != "image/png" || s.Filename != "photo-18.jpg" || !bytes.Equal(store.put[s.ObjectKey], pngBytes) {
		t.Errorf("kept %+v", s)
	}
}

// A file not kept is said, a sentence naming it, so the run can tell the
// person: refused for what it is, or not fetched. The rest are kept.
func TestAFileNotKeptIsSaid(t *testing.T) {
	t.Parallel()

	g, p, _, log := downloading(t, map[string][]byte{"photo": pngBytes, "page": []byte("<html><body>hi</body></html>")})
	stored, missed := g.Fetch(t.Context(), p, WebhookRequest{}, nil, uuid.New(), []Attachment{
		claim("gone", "report.pdf"),
		claim("page", "page.html"),
		claim("photo", "photo.jpg"),
		{Ref: "huge", ClaimedFilename: "film.mov", ClaimedSize: 30 << 20},
	})
	if len(stored) != 1 || stored[0].Filename != "photo.jpg" {
		t.Errorf("stored %+v, want the photo only", stored)
	}
	want := []string{
		"report.pdf could not be fetched",
		"page.html is text/html; charset=utf-8, which is not accepted",
		"film.mov is over the 25 MB a file may be",
	}
	if strings.Join(missed, "|") != strings.Join(want, "|") {
		t.Errorf("missed %q, want %q", missed, want)
	}
	// The download URL carries the provider's token; no log line may.
	if strings.Contains(log.String(), downloadToken) {
		t.Errorf("the log holds the token:\n%s", log)
	}
}

// A provider's budget bounds its files. What does not fit is said, and so is
// every file after it that was never tried.
func TestADownloadPastItsBudgetIsSaid(t *testing.T) {
	t.Parallel()

	g, p, _, _ := downloading(t, map[string][]byte{"slow": pngBytes, "photo": pngBytes})
	// The whole budget runs out on the first file, so the second is never tried.
	p.perFile, p.budget = 200*time.Millisecond, 100*time.Millisecond
	stored, missed := g.Fetch(t.Context(), p, WebhookRequest{}, nil, uuid.New(),
		[]Attachment{claim("slow", "a.png"), claim("photo", "b.png")})
	if len(stored) != 0 {
		t.Errorf("stored %+v past the budget", stored)
	}
	if len(missed) != 2 || !strings.HasPrefix(missed[0], "a.png could not be fetched") ||
		missed[1] != "b.png could not be fetched in time" {
		t.Errorf("missed %q", missed)
	}
}

// A chain of requests that never ends is cut off after a few, not followed
// until the budget runs out.
func TestADownloadThatNeverEndsIsCutOff(t *testing.T) {
	t.Parallel()

	var lookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lookups.Add(1)
		_, _ = w.Write([]byte("files/photo"))
	}))
	t.Cleanup(srv.Close)
	store := newFakeObjects()
	g := NewIngest(store, store, srv.Client(), discardLogger())
	p := &downloadProvider{noFetch: noFetch{&fetchProvider{}}, base: srv.URL, perFile: 5 * time.Second, budget: 5 * time.Second, endless: true}

	stored, missed := g.Fetch(t.Context(), p, WebhookRequest{}, nil, uuid.New(), []Attachment{claim("photo", "a.png")})
	if len(stored) != 0 || len(missed) != 1 {
		t.Errorf("stored %+v, missed %v", stored, missed)
	}
	if n := lookups.Load(); n != downloadSteps {
		t.Errorf("made %d requests, want it cut off at %d", n, downloadSteps)
	}
}

// Where nothing downloads, as on the dashboard, a provider's file is said
// as not fetched rather than dialled with no client.
func TestAnIngestWithNoClientDownloadsNothing(t *testing.T) {
	t.Parallel()

	_, p, store, _ := downloading(t, map[string][]byte{"photo": pngBytes})
	g := NewIngest(store, store, nil, discardLogger())
	stored, missed := g.Fetch(t.Context(), p, WebhookRequest{}, nil, uuid.New(), []Attachment{claim("photo", "a.png")})
	if len(stored) != 0 || len(missed) != 1 || len(store.put) != 0 {
		t.Errorf("stored %+v, missed %v, wrote %d", stored, missed, len(store.put))
	}
}

// A provider that answers the file with an error has sent no file: its body
// is never kept as one.
func TestAnErrorAnsweredForTheFileIsNotKept(t *testing.T) {
	t.Parallel()

	g, p, store, _ := downloading(t, map[string][]byte{"broken": nil})
	stored, missed := g.Fetch(t.Context(), p, WebhookRequest{}, nil, uuid.New(), []Attachment{claim("broken", "notes.txt")})
	if len(stored) != 0 || len(store.put) != 0 {
		t.Errorf("kept %+v: an error page stored as the file", stored)
	}
	if len(missed) != 1 || missed[0] != "notes.txt could not be fetched" {
		t.Errorf("missed %q", missed)
	}
}

// A provider out of reach fails in the transport, whose error quotes the URL,
// and the URL carries the token: neither the log nor what the run is told
// may hold it.
func TestADownloadThatCannotConnectLeaksNoToken(t *testing.T) {
	t.Parallel()

	g, p, _, log := downloading(t, nil)
	closed := httptest.NewServer(http.NotFoundHandler())
	p.base = closed.URL
	closed.Close()
	_, missed := g.Fetch(t.Context(), p, WebhookRequest{}, nil, uuid.New(), []Attachment{claim("photo", "a.png")})
	if len(missed) != 1 {
		t.Fatalf("missed %q", missed)
	}
	if strings.Contains(log.String(), downloadToken) || strings.Contains(missed[0], downloadToken) {
		t.Errorf("the token leaked:\nlog %s\nmissed %q", log, missed)
	}
	if !strings.Contains(log.String(), "connect") && !strings.Contains(log.String(), "refused") {
		t.Errorf("the log does not say why: %s", log)
	}
}
