package sandboxd_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/sandboxd"
)

// seen is one request as sandboxd received it. The client's whole job is to
// put these bytes on the wire, so the tests assert them exactly.
type seen struct {
	Method string
	Path   string
	Auth   string
	Body   string
}

type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func (s *server) last(t *testing.T) seen {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		t.Fatal("sandboxd saw no request")
	}
	return s.reqs[len(s.reqs)-1]
}

func newServer(t *testing.T, status int, reply string) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, seen{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(b)})
		s.mu.Unlock()
		if reply != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestCreateSendsTheSpecAndReturnsTheID(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusCreated, `{"id":"r1"}`)
	c := sandboxd.New(s.URL+"/", "tok", s.Client())

	id, err := c.Create(t.Context(), sandbox.Spec{
		Image:  "room:1",
		Env:    map[string]string{"K": "v"},
		Egress: []sandbox.HostPort{{Host: "imap.example.com", Port: 993}},
		Labels: map[string]string{"team": "t1"},
	})
	if err != nil || id != "r1" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	got := s.last(t)
	want := `{"image":"room:1","env":{"K":"v"},"egress":[{"host":"imap.example.com","port":993}],"labels":{"team":"t1"}}`
	if got.Method != "POST" || got.Path != "/v1/rooms" || got.Auth != "Bearer tok" || got.Body != want {
		t.Fatalf("request = %+v\nwant body %s", got, want)
	}
}

// A nil map would be sent as null, which sandboxd's strict decoder reads as
// a different thing from an empty one.
func TestCreateSendsEmptyCollectionsNotNull(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusCreated, `{"id":"r1"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	if _, err := c.Create(t.Context(), sandbox.Spec{Image: "room:1"}); err != nil {
		t.Fatal(err)
	}
	want := `{"image":"room:1","env":{},"egress":[],"labels":{}}`
	if got := s.last(t).Body; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestCreateSendsAPublicWorkspaceAsSuch(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusCreated, `{"id":"r1"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	if _, err := c.Create(t.Context(), sandbox.Spec{Image: "room:1", Network: sandbox.NetworkPublic, Workspace: true}); err != nil {
		t.Fatal(err)
	}
	want := `{"image":"room:1","env":{},"egress":[],"labels":{},"network":"public","workspace":true}`
	if got := s.last(t).Body; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestCreateMaps422ToErrRefusedWithSandboxdsReason(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusUnprocessableEntity, `{"error":"two hosts on port 25"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	_, err := c.Create(t.Context(), sandbox.Spec{Image: "x"})
	if !errors.Is(err, sandbox.ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "two hosts on port 25") {
		t.Fatalf("the reason is lost: %q", err)
	}
}

// A busy sandboxd is full, said so the agent knows nothing ran and why; any
// other 503 is neither refused nor not found, nor full.
func TestCreateReports503AsAnErrorThatIsNeitherRefusedNorNotFound(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusServiceUnavailable, `{"error":"the sandbox is busy"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	_, err := c.Create(t.Context(), sandbox.Spec{Image: "x"})
	if !errors.Is(err, sandbox.ErrFull) || !strings.Contains(err.Error(), "Nothing ran") {
		t.Fatalf("busy: err = %v, want ErrFull", err)
	}
	s = newServer(t, http.StatusServiceUnavailable, `{"error":"pulling the image failed"}`)
	_, err = sandboxd.New(s.URL, "tok", s.Client()).Create(t.Context(), sandbox.Spec{Image: "x"})
	if err == nil || errors.Is(err, sandbox.ErrRefused) || errors.Is(err, sandbox.ErrNotFound) || errors.Is(err, sandbox.ErrFull) {
		t.Fatalf("an image failure: err = %v", err)
	}
	if !strings.Contains(err.Error(), "pulling the image failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateWithoutAnIDIsAnError(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusCreated, `{}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	if _, err := c.Create(t.Context(), sandbox.Spec{Image: "x"}); err == nil {
		t.Fatal("a room with no id was accepted")
	}
}

func TestStartPostsTheArgv(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusNoContent, "")
	c := sandboxd.New(s.URL, "tok", s.Client())
	if err := c.Start(t.Context(), "r1", []string{"sandbox", "shim", "--", "npx", "-y", "p@1"}); err != nil {
		t.Fatal(err)
	}
	got := s.last(t)
	want := `{"argv":["sandbox","shim","--","npx","-y","p@1"]}`
	if got.Method != "POST" || got.Path != "/v1/rooms/r1/start" || got.Auth != "Bearer tok" || got.Body != want {
		t.Fatalf("request = %+v", got)
	}
}

func TestStartOfAnUnknownRoomIsErrNotFound(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusNotFound, `{"error":"no such room"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	if err := c.Start(t.Context(), "gone", []string{"x"}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestURLReturnsTheProxyAndItsHeaders(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusOK,
		`{"url":"http://sandboxd:9090/v1/rooms/r1/ports/8080/proxy","headers":{"X-Sandboxd-Token":"abc"}}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	ep, err := c.URL(t.Context(), "r1", sandbox.ShimPort)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "http://sandboxd:9090/v1/rooms/r1/ports/8080/proxy" || ep.Header.Get("X-Sandboxd-Token") != "abc" {
		t.Fatalf("endpoint = %+v", ep)
	}
	// The shim's own bearer goes in Authorization; the endpoint must not
	// claim it.
	if ep.Header.Get("Authorization") != "" {
		t.Fatal("the endpoint's headers carry an Authorization")
	}
	got := s.last(t)
	if got.Method != "GET" || got.Path != "/v1/rooms/r1/ports/8080" || got.Auth != "Bearer tok" || got.Body != "" {
		t.Fatalf("request = %+v", got)
	}
}

func TestURLOfAnUnknownRoomIsErrNotFound(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusNotFound, `{"error":"no such room"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	if _, err := c.URL(t.Context(), "gone", 8080); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteSendsDELETE(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusNoContent, "")
	c := sandboxd.New(s.URL, "tok", s.Client())
	if err := c.Delete(t.Context(), "r1"); err != nil {
		t.Fatal(err)
	}
	got := s.last(t)
	if got.Method != "DELETE" || got.Path != "/v1/rooms/r1" || got.Auth != "Bearer tok" {
		t.Fatalf("request = %+v", got)
	}
}

func TestDeleteOfARoomAlreadyGoneIsNil(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusNotFound, `{"error":"no such room"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	if err := c.Delete(t.Context(), "gone"); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteReportsAServerError(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusInternalServerError, `{"error":"docker is down"}`)
	c := sandboxd.New(s.URL, "tok", s.Client())
	if err := c.Delete(t.Context(), "r1"); err == nil {
		t.Fatal("a failed delete was reported as done")
	}
}

func TestARoomIDIsEscapedInThePath(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusNoContent, "")
	c := sandboxd.New(s.URL, "tok", s.Client())
	if err := c.Delete(t.Context(), "a/../b"); err != nil {
		t.Fatal(err)
	}
	if got := s.last(t).Path; got != "/v1/rooms/a%2F..%2Fb" {
		t.Fatalf("path = %s", got)
	}
}

func TestAnErrorNeverCarriesTheRequestBodyOrTheToken(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	c := sandboxd.New(s.URL, "sekrit-token", s.Client())
	_, err := c.Create(t.Context(), sandbox.Spec{Image: "x", Env: map[string]string{"PASSWORD": "hunter2"}})
	if err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "sekrit-token") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewPanicsOnANilClient(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("a nil http client did not panic")
		}
	}()
	sandboxd.New("http://x", "t", nil)
}

func TestStopAndResumePostToTheRoom(t *testing.T) {
	for _, c := range []struct {
		path string
		call func(*sandboxd.Client) error
	}{
		{"/v1/rooms/r1/stop", func(c *sandboxd.Client) error { return c.Stop(t.Context(), "r1") }},
		{"/v1/rooms/r1/resume", func(c *sandboxd.Client) error { return c.Resume(t.Context(), "r1", []string{"ignored"}) }},
	} {
		s := newServer(t, http.StatusNoContent, "")
		if err := c.call(sandboxd.New(s.URL, "tok", s.Client())); err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if got := s.last(t); got.Method != http.MethodPost || got.Path != c.path || got.Auth != "Bearer tok" || got.Body != "" {
			t.Fatalf("request = %+v", got)
		}
	}
}

func TestStopOfARoomNeverStartedIsNotAnError(t *testing.T) {
	s := newServer(t, http.StatusConflict, `{"error":"the room has not started"}`)
	if err := sandboxd.New(s.URL, "tok", s.Client()).Stop(t.Context(), "r1"); err != nil {
		t.Fatal(err)
	}
}

// A room whose container is gone has nothing to resume: ErrNotFound, so the
// keeper replaces it rather than failing every call until it expires.
func TestAResumeOfARoomNeverStartedIsErrNotFound(t *testing.T) {
	s := newServer(t, http.StatusConflict, `{"error":"the room has not started"}`)
	err := sandboxd.New(s.URL, "tok", s.Client()).Resume(t.Context(), "r1", nil)
	if !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAResumeSandboxdHasNoRoomForIsErrFull(t *testing.T) {
	s := newServer(t, http.StatusServiceUnavailable, `{"error":"the sandbox is busy"}`)
	err := sandboxd.New(s.URL, "tok", s.Client()).Resume(t.Context(), "r1", nil)
	if !errors.Is(err, sandbox.ErrFull) {
		t.Fatalf("err = %v, want ErrFull", err)
	}
	// A sandboxd that says why keeps its reason in the error.
	s = newServer(t, http.StatusServiceUnavailable, `{"error":"the sandbox is busy: the workspaces' disk is spent (8200 MiB with this one, of 8192 MiB); stopped workspaces keep theirs until their keep hours end"}`)
	err = sandboxd.New(s.URL, "tok", s.Client()).Resume(t.Context(), "r1", nil)
	if !errors.Is(err, sandbox.ErrFull) || !strings.Contains(err.Error(), "disk is spent") {
		t.Fatalf("err = %v, want ErrFull with the reason", err)
	}
	// An image that cannot be pulled is a 503 too, and is not full.
	s = newServer(t, http.StatusServiceUnavailable, `{"error":"pulling the image failed"}`)
	if err := sandboxd.New(s.URL, "tok", s.Client()).Resume(t.Context(), "r1", nil); errors.Is(err, sandbox.ErrFull) {
		t.Fatalf("an image failure was read as full: %v", err)
	}
}

func TestAResumeOverItsDiskIsRefused(t *testing.T) {
	s := newServer(t, http.StatusUnprocessableEntity, `{"error":"the workspace holds more than it may: 600 MiB of 512"}`)
	err := sandboxd.New(s.URL, "tok", s.Client()).Resume(t.Context(), "r1", nil)
	if !errors.Is(err, sandbox.ErrRefused) || !strings.Contains(err.Error(), "600 MiB of 512") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateSendsAWorkspacesSize(t *testing.T) {
	t.Parallel()
	s := newServer(t, http.StatusCreated, `{"id":"r1"}`)
	_, err := sandboxd.New(s.URL, "tok", s.Client()).Create(t.Context(), sandbox.Spec{
		Image: "x", Workspace: true, Size: &sandbox.Size{MemoryMB: 4096, CPUs: 2, DiskMB: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.last(t).Body; !strings.Contains(got, `"size":{"memory_mb":4096,"cpus":2,"disk_mb":1024}`) {
		t.Fatalf("body = %s", got)
	}
}
