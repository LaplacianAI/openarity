package daytona

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

const testKey = "dtn_key_0123456789"

// sub is a JSON object inside a decoded body, empty when it is not one.
func sub(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// call is one request the fake saw.
type call struct {
	Method, Path string
	Header       http.Header
	Body         map[string]any
}

// fake is Daytona's API and toolbox on one server, answering in the shapes of
// its OpenAPI spec (apps/api and libs/toolbox-api-client-go, v0.190.0).
type fake struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []call
	states []string // GET /sandbox/{id} answers these in turn, then the last
	create func(w http.ResponseWriter)
	// failExec makes the toolbox refuse to run a command, and failStop
	// Daytona refuse a stop.
	failExec, failStop bool
	// shown is the state the last GET answered; a start is refused while
	// it is on its way somewhere else, as Daytona refuses one.
	shown string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{t: t, states: []string{"started"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) client() *Client {
	c := New(f.srv.URL+"/api", testKey, f.srv.Client())
	c.PollEvery = time.Millisecond
	c.StartTimeout = 2 * time.Second
	c.DomainFirewall = true // these tests are of what Daytona cloud takes
	return c
}

func (f *fake) last(method string) call {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i]
		}
	}
	f.t.Fatalf("no %s call", method)
	return call{}
}

func (f *fake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	c := call{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/sandbox":
		if f.create != nil {
			f.create(w)
			return
		}
		_, _ = w.Write([]byte(`{"id":"sb-1","state":"started"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/sandbox/sb-1":
		f.mu.Lock()
		st := f.states[0]
		if len(f.states) > 1 {
			f.states = f.states[1:]
		}
		f.shown = st
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"sb-1","state":"` + st + `","errorReason":"disk full","toolboxProxyUrl":"` + f.srv.URL + `/toolbox/"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/sandbox/sb-1/ports/8080/preview-url":
		_, _ = w.Write([]byte(`{"sandboxId":"sb-1","url":"https://8080-sb-1.proxy.example","token":"prev-tok"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/toolbox/sb-1/process/session":
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && r.URL.Path == "/toolbox/sb-1/process/session/openarity/exec":
		if f.failExec {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"toolbox unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"cmdId":"c1"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/sandbox/sb-1/stop":
		if f.failStop {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"runner unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"sb-1","state":"stopping"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/sandbox/sb-1/start":
		f.mu.Lock()
		busy := f.shown == "stopping"
		f.mu.Unlock()
		if busy {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"statusCode":409,"message":"Sandbox is stopping"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"sb-1","state":"starting"}`))
	case r.Method == http.MethodDelete && r.URL.Path == "/api/sandbox/sb-1":
		_, _ = w.Write([]byte(`{"id":"sb-1","state":"destroying"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"message":"Sandbox with ID or name x not found","error":"Not Found"}`))
	}
}

func TestCreateSendsTheSpecAsDaytonaWantsIt(t *testing.T) {
	f := newFake(t)
	id, err := f.client().Create(t.Context(), sandbox.Spec{
		Image:  "mail-snapshot",
		Env:    map[string]string{"MAIL_PASSWORD": "hunter2hunter2"},
		Labels: map[string]string{"openarity/team": "t1"},
		Egress: []sandbox.HostPort{{Host: "Api.Example.com", Port: 443}, {Host: "api.example.com", Port: 443}, {Host: "*.cdn.example.com", Port: 443}},
	})
	if err != nil || id != "sb-1" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	c := f.last(http.MethodPost)
	if c.Path != "/api/sandbox" {
		t.Errorf("path = %s", c.Path)
	}
	if got := c.Header.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}
	if c.Body["snapshot"] != "mail-snapshot" {
		t.Errorf("snapshot = %v", c.Body["snapshot"])
	}
	if sub(c.Body["env"])["MAIL_PASSWORD"] != "hunter2hunter2" {
		t.Errorf("env = %v", c.Body["env"])
	}
	if sub(c.Body["labels"])["openarity/team"] != "t1" {
		t.Errorf("labels = %v", c.Body["labels"])
	}
	if c.Body["domainAllowList"] != "*.cdn.example.com,api.example.com" {
		t.Errorf("domainAllowList = %v", c.Body["domainAllowList"])
	}
	if _, ok := c.Body["networkAllowList"]; ok {
		t.Errorf("networkAllowList sent with a domain list: %v", c.Body)
	}
	if _, ok := c.Body["networkBlockAll"]; ok {
		t.Errorf("networkBlockAll sent with a domain list")
	}
	if v, ok := c.Body["autoStopInterval"]; !ok || v != 0.0 {
		t.Errorf("autoStopInterval = %v, want 0 so the room never stops under the shim", v)
	}
}

func TestCreateWithNoEgressBlocksAllNetwork(t *testing.T) {
	f := newFake(t)
	if _, err := f.client().Create(t.Context(), sandbox.Spec{}); err != nil {
		t.Fatal(err)
	}
	c := f.last(http.MethodPost)
	if c.Body["networkBlockAll"] != true {
		t.Errorf("networkBlockAll = %v, want true", c.Body["networkBlockAll"])
	}
	if _, ok := c.Body["snapshot"]; ok {
		t.Errorf("snapshot sent for an empty image: %v", c.Body["snapshot"])
	}
}

func TestCreateWithIPsSendsCIDRs(t *testing.T) {
	f := newFake(t)
	_, err := f.client().Create(t.Context(), sandbox.Spec{Egress: []sandbox.HostPort{{Host: "203.0.113.7", Port: 993}}})
	if err != nil {
		t.Fatal(err)
	}
	c := f.last(http.MethodPost)
	if c.Body["networkAllowList"] != "203.0.113.7/32" {
		t.Errorf("networkAllowList = %v", c.Body["networkAllowList"])
	}
	if _, ok := c.Body["domainAllowList"]; ok {
		t.Errorf("domainAllowList sent with a CIDR list")
	}
}

func TestCreateFromAnImageBuildsFromADockerfile(t *testing.T) {
	f := newFake(t)
	if _, err := f.client().Create(t.Context(), sandbox.Spec{Image: "image:node:22-slim"}); err != nil {
		t.Fatal(err)
	}
	c := f.last(http.MethodPost)
	if _, ok := c.Body["snapshot"]; ok {
		t.Errorf("snapshot sent with buildInfo (Daytona answers 400): %v", c.Body)
	}
	if got := sub(c.Body["buildInfo"])["dockerfileContent"]; got != "FROM node:22-slim\n" {
		t.Errorf("dockerfileContent = %q", got)
	}
}

func TestEgressThatDaytonaCannotHonourIsRefusedBeforeAnyCall(t *testing.T) {
	cases := map[string][]sandbox.HostPort{
		"a wildcard beside another port": {{Host: "*.example.com", Port: 443}, {Host: "imap.gmail.com", Port: 993}},
		"ipv6":                           {{Host: "2001:db8::1", Port: 443}},
		"one label":                      {{Host: "localhost", Port: 443}},
		"bad port":                       {{Host: "api.example.com", Port: 0}},
		"bad image":                      nil,
		"too many":                       manyHosts(101),
		"many cidrs":                     manyIPs(11),
	}
	for name, egress := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			spec := sandbox.Spec{Egress: egress}
			if name == "bad image" {
				spec.Image = "image:"
			}
			_, err := f.client().Create(t.Context(), spec)
			if !errors.Is(err, sandbox.ErrRefused) {
				t.Fatalf("err = %v, want ErrRefused", err)
			}
			if n := len(f.paths()); n != 0 {
				t.Errorf("%d calls made for a refused spec: %v", n, f.paths())
			}
		})
	}
}

func manyHosts(n int) []sandbox.HostPort {
	var out []sandbox.HostPort
	for i := range n {
		out = append(out, sandbox.HostPort{Host: "h" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".example.com", Port: 443})
	}
	return out
}

func manyIPs(n int) []sandbox.HostPort {
	var out []sandbox.HostPort
	for i := range n {
		out = append(out, sandbox.HostPort{Host: "203.0.113." + string(rune('0'+i/10)) + string(rune('0'+i%10)), Port: 443})
	}
	return out
}

func TestCreatePollsUntilStarted(t *testing.T) {
	f := newFake(t)
	f.states = []string{"pending_build", "building_snapshot", "started"}
	f.create = func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"id":"sb-1","state":"pending_build"}`)) }
	id, err := f.client().Create(t.Context(), sandbox.Spec{Image: "image:alpine"})
	if err != nil || id != "sb-1" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	gets := 0
	for _, p := range f.paths() {
		if p == "GET /api/sandbox/sb-1" {
			gets++
		}
	}
	if gets != 3 {
		t.Errorf("polled %d times, want 3: %v", gets, f.paths())
	}
}

func TestCreateThatFailsToStartDeletesTheRoom(t *testing.T) {
	f := newFake(t)
	f.states = []string{"build_failed"}
	f.create = func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"id":"sb-1","state":"pending_build"}`)) }
	_, err := f.client().Create(t.Context(), sandbox.Spec{Image: "image:alpine"})
	if err == nil || !strings.Contains(err.Error(), "build_failed") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want build_failed with Daytona's reason", err)
	}
	if got := f.paths(); got[len(got)-1] != "DELETE /api/sandbox/sb-1" {
		t.Errorf("calls = %v, want the half-made room deleted", got)
	}
}

func TestCreateThatNeverStartsGivesUpAndDeletes(t *testing.T) {
	f := newFake(t)
	f.states = []string{"starting"}
	f.create = func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"id":"sb-1","state":"creating"}`)) }
	c := f.client()
	c.StartTimeout = 30 * time.Millisecond
	_, err := c.Create(t.Context(), sandbox.Spec{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	// The poll the deadline cancelled can reach the fake after the DELETE,
	// so the DELETE is looked for, not taken to be the last call.
	if got := f.paths(); !slices.Contains(got, "DELETE /api/sandbox/sb-1") {
		t.Errorf("calls = %v, want the room deleted", got)
	}
}

func TestCreateBadRequestIsRefusedWithDaytonasMessage(t *testing.T) {
	f := newFake(t)
	f.create = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"statusCode":400,"message":"Domain allow list cannot contain more than 10 domains","error":"Bad Request"}`))
	}
	_, err := f.client().Create(t.Context(), sandbox.Spec{})
	if !errors.Is(err, sandbox.ErrRefused) || !strings.Contains(err.Error(), "more than 10 domains") {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsNeverCarryTheKeyOrEnvValues(t *testing.T) {
	f := newFake(t)
	f.create = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		// A server that echoes what it was sent, as a proxy in front might.
		_, _ = w.Write([]byte(`{"message":"boom for ` + testKey + ` with hunter2hunter2 ` + strings.Repeat("x", 600) + `"}`))
	}
	_, err := f.client().Create(t.Context(), sandbox.Spec{Env: map[string]string{"P": "hunter2hunter2"}})
	if err == nil {
		t.Fatal("no error")
	}
	for _, secret := range []string{testKey, "hunter2hunter2"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error carries %q: %v", secret, err)
		}
	}
	if len(err.Error()) > maxErrorText+120 {
		t.Errorf("error is %d bytes: not cut", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "500") {
		t.Errorf("error lost Daytona's message: %v", err)
	}
}

func TestTransportErrorsNeverCarryTheKey(t *testing.T) {
	f := newFake(t)
	c := f.client()
	f.srv.Close()
	_, err := c.URL(t.Context(), "sb-1", 8080)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartRunsTheCommandInTheBackgroundThroughTheToolbox(t *testing.T) {
	f := newFake(t)
	argv := []string{"sandbox", "shim", "--listen", ":8080", "--", "npx", "-y", "mcp-mail-server@2.1.3", "it's $HOME; rm"}
	if err := f.client().Start(t.Context(), "sb-1", argv); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/sandbox/sb-1",
		"POST /toolbox/sb-1/process/session",
		"POST /toolbox/sb-1/process/session/openarity/exec",
	}
	if got := f.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	f.mu.Lock()
	sess, exec := f.calls[1], f.calls[2]
	f.mu.Unlock()
	if sess.Body["sessionId"] != "openarity" || sess.Header.Get("Authorization") != "Bearer "+testKey {
		t.Errorf("session = %v %v", sess.Body, sess.Header)
	}
	if exec.Body["runAsync"] != true {
		t.Errorf("runAsync = %v, Start would wait for the shim to exit", exec.Body["runAsync"])
	}
	wantCmd := `'sandbox' 'shim' '--listen' ':8080' '--' 'npx' '-y' 'mcp-mail-server@2.1.3' 'it'\''s $HOME; rm'`
	if exec.Body["command"] != wantCmd {
		t.Errorf("command = %v\nwant      %s", exec.Body["command"], wantCmd)
	}
}

func TestStartToleratesASessionThatExists(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/toolbox/sb-1/process/session" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"session already exists"}`))
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL+"/api", testKey, srv.Client())
	// The fake advertises its own toolbox URL; point the wrapper's at itself.
	f.srv = srv
	if err := c.Start(t.Context(), "sb-1", []string{"true"}); err != nil {
		t.Fatalf("Start = %v", err)
	}
}

func TestStartOfAMissingRoomIsNotFound(t *testing.T) {
	f := newFake(t)
	err := f.client().Start(t.Context(), "gone", []string{"true"})
	if !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestURLCarriesThePreviewTokenHeader(t *testing.T) {
	f := newFake(t)
	ep, err := f.client().URL(t.Context(), "sb-1", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "https://8080-sb-1.proxy.example" {
		t.Errorf("URL = %q", ep.URL)
	}
	if got := ep.Header.Get("x-daytona-preview-token"); got != "prev-tok" {
		t.Errorf("token header = %q", got)
	}
	if got := f.last(http.MethodGet); got.Path != "/api/sandbox/sb-1/ports/8080/preview-url" {
		t.Errorf("path = %s", got.Path)
	}
}

func TestURLOfAMissingRoomIsNotFound(t *testing.T) {
	f := newFake(t)
	if _, err := f.client().URL(t.Context(), "gone", 8080); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	f := newFake(t)
	c := f.client()
	if err := c.Delete(t.Context(), "sb-1"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(http.MethodDelete); got.Path != "/api/sandbox/sb-1" {
		t.Errorf("path = %s", got.Path)
	}
	if err := c.Delete(t.Context(), "gone"); err != nil {
		t.Errorf("Delete of a missing room = %v, want nil", err)
	}
}

func TestDeleteOtherFailuresAreErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"no"}`))
	}))
	t.Cleanup(srv.Close)
	err := New(srv.URL, testKey, srv.Client()).Delete(t.Context(), "sb-1")
	if err == nil || errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewDefaultsToDaytonaCloud(t *testing.T) {
	if c := New("", "k", nil); c.apiURL != "https://app.daytona.io/api" {
		t.Errorf("apiURL = %q", c.apiURL)
	}
	if c := New("https://daytona.internal/api/", "k", nil); c.apiURL != "https://daytona.internal/api" {
		t.Errorf("apiURL = %q", c.apiURL)
	}
}

func TestSelfHostedDaytonaGetsResolvedAddressesNotDomains(t *testing.T) {
	f := newFake(t)
	c := f.client()
	c.DomainFirewall = false
	c.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		return map[string][]netip.Addr{
			"imap.gmail.com": {netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("192.0.2.8"), netip.MustParseAddr("2001:db8::1")},
			"smtp.gmail.com": {netip.MustParseAddr("192.0.2.8")},
		}[host], nil
	}
	_, err := c.Create(t.Context(), sandbox.Spec{Egress: []sandbox.HostPort{
		{Host: "imap.gmail.com", Port: 993}, {Host: "smtp.gmail.com", Port: 465}, {Host: "198.51.100.1", Port: 443},
	}})
	if err != nil {
		t.Fatal(err)
	}
	b := f.last(http.MethodPost).Body
	if b["networkAllowList"] != "192.0.2.8/32,192.0.2.9/32,198.51.100.1/32" {
		t.Errorf("networkAllowList = %v", b["networkAllowList"])
	}
	if _, ok := b["domainAllowList"]; ok {
		t.Errorf("domainAllowList sent to a Daytona that ignores it: %v", b["domainAllowList"])
	}
}

func TestSelfHostedEgressThatCannotBeResolvedIsRefusedOrFails(t *testing.T) {
	c := New("http://daytona.invalid/api", testKey, nil)
	if c.DomainFirewall {
		t.Fatal("a self-hosted URL was taken for Daytona cloud")
	}
	if !New("", testKey, nil).DomainFirewall {
		t.Fatal("Daytona cloud was taken for self-hosted")
	}
	// The resolver would answer for a wildcard, so only the guard stops it.
	c.Resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
	}
	_, err := c.Create(t.Context(), sandbox.Spec{Egress: []sandbox.HostPort{{Host: "*.example.com", Port: 443}}})
	if !errors.Is(err, sandbox.ErrRefused) {
		t.Errorf("wildcard: err = %v, want ErrRefused", err)
	}
	c.Resolve = func(context.Context, string) ([]netip.Addr, error) { return nil, nil }
	_, err = c.Create(t.Context(), sandbox.Spec{Egress: []sandbox.HostPort{{Host: "v6only.example.com", Port: 443}}})
	if !errors.Is(err, sandbox.ErrRefused) {
		t.Errorf("no IPv4: err = %v, want ErrRefused", err)
	}
	c.Resolve = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
	_, err = c.Create(t.Context(), sandbox.Spec{Egress: []sandbox.HostPort{{Host: "gone.example.com", Port: 443}}})
	if err == nil || errors.Is(err, sandbox.ErrRefused) {
		t.Errorf("lookup failure: err = %v, want a plain error", err)
	}
}

// On Daytona cloud a list its domain firewall can say whole goes there; one
// entry it cannot say moves every host to resolved addresses.
func TestDaytonaCloudResolvesHostsWhenTheDomainListCannotSayItAll(t *testing.T) {
	resolve := func(_ context.Context, host string) ([]netip.Addr, error) {
		return map[string][]netip.Addr{
			"imap.gmail.com":     {netip.MustParseAddr("192.0.2.9")},
			"registry.npmjs.org": {netip.MustParseAddr("192.0.2.20")},
		}[host], nil
	}
	cases := map[string]struct {
		egress  []sandbox.HostPort
		domains any
		cidrs   any
	}{
		"https names only": {
			egress:  []sandbox.HostPort{{Host: "registry.npmjs.org", Port: 443}},
			domains: "registry.npmjs.org",
		},
		"imaps beside https": {
			egress: []sandbox.HostPort{{Host: "imap.gmail.com", Port: 993}, {Host: "registry.npmjs.org", Port: 443}},
			cidrs:  "192.0.2.20/32,192.0.2.9/32",
		},
		"a name beside an address": {
			egress: []sandbox.HostPort{{Host: "registry.npmjs.org", Port: 443}, {Host: "203.0.113.7", Port: 443}},
			cidrs:  "192.0.2.20/32,203.0.113.7/32",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			c := f.client()
			c.Resolve = resolve
			if _, err := c.Create(t.Context(), sandbox.Spec{Egress: tc.egress}); err != nil {
				t.Fatal(err)
			}
			b := f.last(http.MethodPost).Body
			if b["domainAllowList"] != tc.domains || b["networkAllowList"] != tc.cidrs {
				t.Errorf("domainAllowList = %v, networkAllowList = %v; want %v and %v",
					b["domainAllowList"], b["networkAllowList"], tc.domains, tc.cidrs)
			}
		})
	}
}

// On Daytona cloud a public room is sent with no lists and is not blocked.
func TestAPublicRoomOnDaytonaCloudSendsNoLists(t *testing.T) {
	f := newFake(t)
	if _, err := f.client().Create(t.Context(), sandbox.Spec{Network: sandbox.NetworkPublic, Workspace: true}); err != nil {
		t.Fatal(err)
	}
	b := f.last(http.MethodPost).Body
	for _, k := range []string{"networkBlockAll", "networkAllowList", "domainAllowList"} {
		if v, ok := b[k]; ok && v != "" {
			t.Errorf("%s = %v on a public room", k, v)
		}
	}
}

// A self-hosted Daytona can only allow, never deny, so it cannot keep a room
// off the network it runs on: a public room there is refused before any call.
func TestAPublicRoomOnASelfHostedDaytonaIsRefused(t *testing.T) {
	f := newFake(t)
	c := f.client()
	c.DomainFirewall = false
	_, err := c.Create(t.Context(), sandbox.Spec{Network: sandbox.NetworkPublic})
	if !errors.Is(err, sandbox.ErrRefused) || !strings.Contains(err.Error(), "private addresses") {
		t.Fatalf("err = %v, want ErrRefused saying why", err)
	}
	f.mu.Lock()
	reached := len(f.calls)
	f.mu.Unlock()
	if reached != 0 {
		t.Error("a refused room still reached Daytona")
	}
	for _, s := range []sandbox.Spec{
		{Network: sandbox.NetworkPublic, Egress: []sandbox.HostPort{{Host: "a.example.com", Port: 443}}},
		{Network: "host"},
	} {
		if _, err := f.client().Create(t.Context(), s); !errors.Is(err, sandbox.ErrRefused) {
			t.Errorf("%+v: %v, want ErrRefused", s, err)
		}
	}
}

func TestStopStopsTheSandbox(t *testing.T) {
	f := newFake(t)
	if err := f.client().Stop(t.Context(), "sb-1"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.paths(), ","); got != "POST /api/sandbox/sb-1/stop" {
		t.Fatalf("calls = %s", got)
	}
}

// A stopped sandbox has lost its process: a resume starts it, waits for it,
// and runs the command again.
func TestResumeStartsAStoppedSandboxAndRunsItsCommandAgain(t *testing.T) {
	f := newFake(t)
	f.states = []string{"stopped", "starting", "started"}
	if err := f.client().Resume(t.Context(), "sb-1", []string{"sandbox", "workspace"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.paths(), ",")
	if !strings.Contains(got, "POST /api/sandbox/sb-1/start") || !strings.HasSuffix(got, "POST /toolbox/sb-1/process/session/openarity/exec") {
		t.Fatalf("calls = %s", got)
	}
	if cmd := f.last(http.MethodPost).Body["command"]; cmd != "'sandbox' 'workspace'" {
		t.Fatalf("command = %v", cmd)
	}
}

// One that runs is left alone, so its command is never run twice.
func TestResumeOfARunningSandboxRunsNothing(t *testing.T) {
	f := newFake(t)
	if err := f.client().Resume(t.Context(), "sb-1", []string{"sandbox", "workspace"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.paths(), ","); got != "GET /api/sandbox/sb-1" {
		t.Fatalf("calls = %s", got)
	}
}

func TestStopAndResumeOfAGoneSandboxAreErrNotFound(t *testing.T) {
	f := newFake(t)
	if err := f.client().Stop(t.Context(), "gone"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("stop: %v", err)
	}
	if err := f.client().Resume(t.Context(), "gone", []string{"x"}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("resume: %v", err)
	}
}

func TestCreateSendsTheTeamsSizeInWholeGB(t *testing.T) {
	f := newFake(t)
	_, err := f.client().Create(t.Context(), sandbox.Spec{
		Image: "image:room:1", Workspace: true, Size: &sandbox.Size{MemoryMB: 1536, CPUs: 2, DiskMB: 8192},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := f.last(http.MethodPost).Body
	if b["cpu"] != float64(2) || b["memory"] != float64(2) || b["disk"] != float64(8) {
		t.Fatalf("size sent = cpu %v memory %v disk %v", b["cpu"], b["memory"], b["disk"])
	}
}

func TestCreateWithNoSizeLeavesItToDaytona(t *testing.T) {
	f := newFake(t)
	if _, err := f.client().Create(t.Context(), sandbox.Spec{Image: "image:room:1"}); err != nil {
		t.Fatal(err)
	}
	b := f.last(http.MethodPost).Body
	for _, k := range []string{"cpu", "memory", "disk"} {
		if _, ok := b[k]; ok {
			t.Errorf("%s sent with no size", k)
		}
	}
}

// A sandbox still stopping is waited out, then started; an archived one is
// started as a stopped one is, Daytona restoring its files first.
func TestResumeWaitsOutAStopAndStartsAnArchivedSandbox(t *testing.T) {
	for name, states := range map[string][]string{
		"stopping": {"stopping", "stopping", "stopped", "starting", "started"},
		"archived": {"archived", "starting", "started"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.states = states
			if err := f.client().Resume(t.Context(), "sb-1", []string{"sandbox", "workspace"}); err != nil {
				t.Fatal(err)
			}
			starts := 0
			for _, p := range f.paths() {
				if p == "POST /api/sandbox/sb-1/start" {
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("calls = %s, want one start", strings.Join(f.paths(), ","))
			}
		})
	}
}

// One Daytona cannot bring back is gone, so the keeper replaces it.
func TestResumeOfASandboxInErrorIsErrNotFound(t *testing.T) {
	for _, st := range []string{"error", "build_failed", "destroyed", "deleted"} {
		f := newFake(t)
		f.states = []string{st}
		err := f.client().Resume(t.Context(), "sb-1", []string{"x"})
		if !errors.Is(err, sandbox.ErrNotFound) {
			t.Fatalf("%s: err = %v, want ErrNotFound", st, err)
		}
		for _, p := range f.paths() {
			if p == "POST /api/sandbox/sb-1/start" {
				t.Fatalf("%s: a start was sent", st)
			}
		}
	}
}

// Started, but its command not: it is stopped again, so the next Resume
// starts it and runs the command rather than finding it started and running
// nothing.
func TestAResumeWhoseCommandFailsStopsTheSandboxAgain(t *testing.T) {
	f := newFake(t)
	f.states = []string{"stopped", "starting", "started"}
	f.failExec = true
	if err := f.client().Resume(t.Context(), "sb-1", []string{"sandbox", "workspace"}); err == nil {
		t.Fatal("Resume succeeded with its command refused")
	}
	if p := f.paths(); p[len(p)-1] != "POST /api/sandbox/sb-1/stop" {
		t.Fatalf("calls = %s, want a stop last", strings.Join(p, ","))
	}
}

// A sandbox that fails while it starts is gone too, not a plain error: the
// keeper replaces it on this call rather than the next.
func TestAResumeThatFailsWhileStartingIsErrNotFound(t *testing.T) {
	f := newFake(t)
	f.states = []string{"stopped", "starting", "error"}
	if err := f.client().Resume(t.Context(), "sb-1", []string{"x"}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A cleanup stop that fails is said, so the room left started without its
// command is not a mystery when the keeper replaces it.
func TestAFailedCleanupStopIsSaid(t *testing.T) {
	f := newFake(t)
	f.states = []string{"stopped", "starting", "started"}
	f.failExec, f.failStop = true, true
	err := f.client().Resume(t.Context(), "sb-1", []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "stopping it again failed") {
		t.Fatalf("err = %v", err)
	}
}
