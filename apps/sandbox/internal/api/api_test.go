package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/api"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/guard"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/rooms"
)

type fakeRooms struct {
	mu      sync.Mutex
	created []rooms.Spec
	started map[string][]string
	target  string
	err     error
}

func (f *fakeRooms) Create(_ context.Context, sp rooms.Spec) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, sp)
	return "r1", nil
}

func (f *fakeRooms) Start(_ context.Context, id string, argv []string) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started == nil {
		f.started = map[string][]string{}
	}
	f.started[id] = argv
	return nil
}

func (f *fakeRooms) Delete(context.Context, string) error { return f.err }
func (f *fakeRooms) Exists(context.Context, string) error { return f.err }
func (f *fakeRooms) Target(context.Context, string, int) (string, error) {
	return f.target, f.err
}
func (f *fakeRooms) Token(id string) string { return "tok-" + id }

const (
	token  = "the-token"
	public = "http://sandboxd.internal:9090/"
)

func setup(t *testing.T, f *fakeRooms) *httptest.Server {
	t.Helper()
	h := api.New(f, token, public, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func call(t *testing.T, method, url, auth, body string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestEveryRoomsRouteNeedsTheBearerToken(t *testing.T) {
	t.Parallel()
	f := &fakeRooms{}
	s := setup(t, f)
	routes := []struct{ method, path string }{
		{"POST", "/v1/rooms"},
		{"POST", "/v1/rooms/r1/start"},
		{"GET", "/v1/rooms/r1/ports/8080"},
		{"DELETE", "/v1/rooms/r1"},
	}
	for _, r := range routes {
		for _, auth := range []string{"", "Bearer wrong", "Bearer ", token, "Basic " + token, "bearer " + token} {
			resp, _ := call(t, r.method, s.URL+r.path, auth, `{"image":"x"}`)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with %q = %d", r.method, r.path, auth, resp.StatusCode)
			}
		}
	}
	if len(f.created) != 0 || len(f.started) != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
}

func TestHealthzNeedsNoToken(t *testing.T) {
	t.Parallel()
	s := setup(t, &fakeRooms{})
	if resp, _ := call(t, "GET", s.URL+"/healthz", "", ""); resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestCreateReturns201AndPassesTheSpecOn(t *testing.T) {
	t.Parallel()
	f := &fakeRooms{}
	s := setup(t, f)
	resp, body := call(t, "POST", s.URL+"/v1/rooms", "Bearer "+token,
		`{"image":"room:1","env":{"A":"b"},"egress":[{"host":"imap.example.com","port":993}],"labels":{"t":"1"}}`)
	if resp.StatusCode != 201 || strings.TrimSpace(body) != `{"id":"r1"}` {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	got := f.created[0]
	if got.Image != "room:1" || got.Env["A"] != "b" || got.Egress[0] != (guard.Host{Name: "imap.example.com", Port: 993}) || got.Labels["t"] != "1" {
		t.Fatalf("spec = %+v", got)
	}
}

func TestCreateRefusesARequestThatNamesAMountOrAnythingElseItDoesNotKnow(t *testing.T) {
	t.Parallel()
	f := &fakeRooms{}
	s := setup(t, f)
	for _, body := range []string{
		`{"image":"x","mounts":["/:/host"]}`,
		`{"image":"x","privileged":true}`,
		`{"image":"x","user":"0"}`,
		`{"image":"x","cap_add":["ALL"]}`,
		`{"image":"x","network":"host"}`,
		`not json`,
		`{"image":"x"} {"image":"y"}`,
	} {
		resp, b := call(t, "POST", s.URL+"/v1/rooms", "Bearer "+token, body)
		if resp.StatusCode != 422 || !strings.Contains(b, `"error"`) {
			t.Errorf("%s -> %d %s", body, resp.StatusCode, b)
		}
	}
	if len(f.created) != 0 {
		t.Fatal("a refused body reached the service")
	}
}

func TestServiceErrorsMapToStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    error
		status int
		body   string
	}{
		{rooms.ErrBusy, 503, `{"error":"the sandbox is busy"}`},
		{fmt.Errorf("%w: two hosts", rooms.ErrRefused), 422, "two hosts"},
		{fmt.Errorf("%w: pulling the image \"ghcr.io/o/r:1\" failed: docker answered 404: not found", rooms.ErrUnavailable), 503, "ghcr.io/o/r:1"},
		{rooms.ErrNotFound, 404, "no such room"},
		{rooms.ErrStarted, 409, ""},
		{errors.New("docker: secret detail"), 500, "the sandbox failed"},
	}
	for _, c := range cases {
		s := setup(t, &fakeRooms{err: c.err})
		resp, body := call(t, "POST", s.URL+"/v1/rooms", "Bearer "+token, `{"image":"x"}`)
		if resp.StatusCode != c.status || !strings.Contains(body, c.body) {
			t.Errorf("%v -> %d %s", c.err, resp.StatusCode, body)
		}
		if strings.Contains(body, "secret detail") {
			t.Errorf("an internal error leaked: %s", body)
		}
	}
}

func TestUnknownRoomIs404OnStartPortsAndDelete(t *testing.T) {
	t.Parallel()
	s := setup(t, &fakeRooms{err: rooms.ErrNotFound})
	for _, r := range []struct{ m, p string }{
		{"POST", "/v1/rooms/zz/start"}, {"GET", "/v1/rooms/zz/ports/8080"}, {"DELETE", "/v1/rooms/zz"},
	} {
		if resp, _ := call(t, r.m, s.URL+r.p, "Bearer "+token, `{"argv":["x"]}`); resp.StatusCode != 404 {
			t.Errorf("%s %s = %d", r.m, r.p, resp.StatusCode)
		}
	}
}

func TestStartReturns204AndPassesTheArgv(t *testing.T) {
	t.Parallel()
	f := &fakeRooms{}
	s := setup(t, f)
	resp, _ := call(t, "POST", s.URL+"/v1/rooms/r1/start", "Bearer "+token, `{"argv":["sandbox","shim"]}`)
	if resp.StatusCode != 204 || strings.Join(f.started["r1"], " ") != "sandbox shim" {
		t.Fatalf("%d %v", resp.StatusCode, f.started)
	}
}

func TestDeleteReturns204(t *testing.T) {
	t.Parallel()
	s := setup(t, &fakeRooms{})
	if resp, _ := call(t, "DELETE", s.URL+"/v1/rooms/r1", "Bearer "+token, ""); resp.StatusCode != 204 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestPortsReturnsTheProxyURLAndItsToken(t *testing.T) {
	t.Parallel()
	s := setup(t, &fakeRooms{})
	resp, body := call(t, "GET", s.URL+"/v1/rooms/r1/ports/8080", "Bearer "+token, "")
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var out struct {
		URL     string
		Headers map[string]string
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != "http://sandboxd.internal:9090/v1/rooms/r1/ports/8080/proxy" || out.Headers["X-Sandboxd-Token"] != "tok-r1" {
		t.Fatalf("%+v", out)
	}
	if strings.Contains(body, token) {
		t.Fatal("the main token is in the answer")
	}
}

// room is a stand-in for the shim in a room, and records what it is sent.
func room(t *testing.T) (addr string, seen chan *http.Request) {
	t.Helper()
	seen = make(chan *http.Request, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		_, _ = io.WriteString(w, "from the room")
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), seen
}

func TestTheProxyForwardsAuthorizationUntouchedAndDropsOurToken(t *testing.T) {
	t.Parallel()
	addr, seen := room(t)
	s := setup(t, &fakeRooms{target: addr})
	resp, body := call(t, "POST", s.URL+"/v1/rooms/r1/ports/8080/proxy/mcp?x=1", "Bearer shim-secret", `{}`,
		"X-Sandboxd-Token", "tok-r1")
	if resp.StatusCode != 200 || body != "from the room" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	got := <-seen
	if got.Header.Get("Authorization") != "Bearer shim-secret" {
		t.Errorf("Authorization = %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("X-Sandboxd-Token") != "" {
		t.Error("the proxy token reached the room")
	}
	if got.URL.Path != "/mcp" || got.URL.RawQuery != "x=1" || got.Method != "POST" {
		t.Errorf("request = %s %s?%s", got.Method, got.URL.Path, got.URL.RawQuery)
	}
}

func TestTheProxyRefusesTheWrongTokenAndTheMainBearer(t *testing.T) {
	t.Parallel()
	addr, seen := room(t)
	s := setup(t, &fakeRooms{target: addr})
	for _, hdr := range [][]string{
		nil,
		{"X-Sandboxd-Token", "wrong"},
		{"X-Sandboxd-Token", "tok-other"},
		{"Authorization", "Bearer " + token}, // the API's token is no key to a room
	} {
		resp, _ := call(t, "GET", s.URL+"/v1/rooms/r1/ports/8080/proxy/healthz", "", "", hdr...)
		if resp.StatusCode != 401 {
			t.Errorf("%v = %d", hdr, resp.StatusCode)
		}
	}
	if len(seen) != 0 {
		t.Fatal("an unauthenticated request reached the room")
	}
}

func TestTheProxyOfARoomThatHasNotStartedIs502(t *testing.T) {
	t.Parallel()
	s := setup(t, &fakeRooms{err: rooms.ErrNotStarted})
	resp, _ := call(t, "GET", s.URL+"/v1/rooms/r1/ports/8080/proxy/mcp", "", "", "X-Sandboxd-Token", "tok-r1")
	if resp.StatusCode != 502 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestTheProxyOfAnUnknownRoomIs404(t *testing.T) {
	t.Parallel()
	s := setup(t, &fakeRooms{err: rooms.ErrNotFound})
	resp, _ := call(t, "GET", s.URL+"/v1/rooms/r1/ports/8080/proxy/mcp", "", "", "X-Sandboxd-Token", "tok-r1")
	if resp.StatusCode != 404 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestTheProxyReportsARoomThatDoesNotAnswerAs502(t *testing.T) {
	t.Parallel()
	l := httptest.NewServer(nil)
	addr := strings.TrimPrefix(l.URL, "http://")
	l.Close()
	s := setup(t, &fakeRooms{target: addr})
	resp, _ := call(t, "GET", s.URL+"/v1/rooms/r1/ports/8080/proxy/", "", "", "X-Sandboxd-Token", "tok-r1")
	if resp.StatusCode != 502 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestNewPanicsOnANilLogger(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	api.New(&fakeRooms{}, "t", "http://x", nil)
}
