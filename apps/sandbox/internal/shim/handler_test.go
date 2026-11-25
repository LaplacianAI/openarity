package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "fake-mcp-server" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const token = "the-shim-token"

type shimmed struct {
	child   *Child
	handler *Handler
	url     string
}

// startShim runs the fake server behind a shim, initialized, and served
// over HTTP for the length of the test.
func startShim(t *testing.T) shimmed {
	t.Helper()
	child, err := Start(t.Context(), []string{os.Args[0], "fake-mcp-server"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(child, token, t.TempDir(), slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return shimmed{child: child, handler: h, url: srv.URL}
}

type bearer struct{ next http.RoundTripper }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return b.next.RoundTrip(r)
}

// connect opens a session the way the brain does: the SDK's Streamable HTTP
// client.
func connect(t *testing.T, s shimmed) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "brain", Version: "test"}, nil)
	transport := &sdk.StreamableClientTransport{
		Endpoint:   s.url + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{http.DefaultTransport}},
	}
	cs, err := client.Connect(quick(t), transport, nil)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// quick bounds one call: an answer that went to the wrong caller never
// comes, and the test fails rather than hangs.
func quick(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// callText calls a tool and returns its text. An answer that never comes
// back, as when it went to the wrong caller, fails here rather than hangs.
func callText(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(quick(t), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("calling %s: %v", name, err)
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("%s answered %T, want text", name, res.Content[0])
	}
	return text.Text
}

func seenBy(t *testing.T, s shimmed) map[string]string {
	t.Helper()
	return parseSeen(callText(t, connect(t, s), "seen", nil))
}

func TestTheBrainsClientListsAndCallsTools(t *testing.T) {
	s := startShim(t)
	cs := connect(t, s)

	tools, err := cs.ListTools(quick(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); got != "echo,slow,die,ask,seen" {
		t.Errorf("tools = %s", got)
	}
	if got := callText(t, cs, "echo", map[string]any{"text": "hello"}); got != "hello" {
		t.Errorf("echo = %q", got)
	}
}

func TestEverySessionSharesTheOneInitialize(t *testing.T) {
	s := startShim(t)
	connect(t, s)
	connect(t, s)
	if got := seenBy(t, s)["initializes"]; got != "1" {
		t.Errorf("the server was initialized %s times, want 1", got)
	}
}

// Both clients number their requests from the same start, and the slow
// answer comes back last: each must still get its own.
func TestConcurrentCallsAreAnsweredToWhoeverAsked(t *testing.T) {
	s := startShim(t)
	a, b := connect(t, s), connect(t, s)

	var wg sync.WaitGroup
	got := make([]string, 2)
	wg.Go(func() { got[0] = callText(t, a, "slow", map[string]any{"text": "from a", "ms": 300}) })
	time.Sleep(50 * time.Millisecond)
	wg.Go(func() { got[1] = callText(t, b, "echo", map[string]any{"text": "from b"}) })
	wg.Wait()

	if got[0] != "from a" || got[1] != "from b" {
		t.Errorf("a got %q, b got %q", got[0], got[1])
	}
}

func TestARequestFromTheServerIsRefusedAndTheCallGoesOn(t *testing.T) {
	s := startShim(t)
	got := callText(t, connect(t, s), "ask", nil)
	if !strings.Contains(got, "-32601") {
		t.Errorf("the server was answered %s, want -32601", got)
	}
}

func TestAServerThatDiesFailsTheCallAndTheHealthCheck(t *testing.T) {
	s := startShim(t)
	cs := connect(t, s)

	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "die"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a call to a server that died succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a call to a server that died is still waiting")
	}

	<-s.child.Done()
	if code := status(t, s, http.MethodGet, "/healthz", "", nil, ""); code != http.StatusServiceUnavailable {
		t.Errorf("healthz = %d after the server died", code)
	}
	if _, err := s.child.Call(t.Context(), "tools/list", nil, nil); err == nil {
		t.Error("a call after the server died was sent")
	}
}

// raw sends one message to /mcp and returns the response.
func raw(t *testing.T, s shimmed, method, auth, session string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, s.url+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if session != "" {
		req.Header.Set(sessionHeader, session)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func status(t *testing.T, s shimmed, method, path, auth string, body []byte, session string) int {
	t.Helper()
	if path != "/mcp" {
		req, _ := http.NewRequestWithContext(t.Context(), method, s.url+path, nil)
		req.Header.Set("Authorization", auth)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	return raw(t, s, method, auth, session, body).StatusCode
}

func open(t *testing.T, s shimmed) string {
	t.Helper()
	res := raw(t, s, http.MethodPost, "Bearer "+token, "",
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("initialize = %d", res.StatusCode)
	}
	return res.Header.Get(sessionHeader)
}

var echo = []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"}}}`)

// Every refusal is made before anything reaches the server.
func TestRefusedRequestsNeverReachTheServer(t *testing.T) {
	s := startShim(t)
	session := open(t, s)

	cases := []struct {
		name         string
		method, auth string
		session      string
		body         []byte
		want         int
	}{
		{"no token", http.MethodPost, "", session, echo, http.StatusUnauthorized},
		{"a wrong token", http.MethodPost, "Bearer not-it", session, echo, http.StatusUnauthorized},
		{"a token without Bearer", http.MethodPost, token, session, echo, http.StatusUnauthorized},
		{"no session", http.MethodPost, "Bearer " + token, "", echo, http.StatusBadRequest},
		{"an unknown session", http.MethodPost, "Bearer " + token, "made-up", echo, http.StatusNotFound},
		{"not JSON", http.MethodPost, "Bearer " + token, session, []byte(`tools/call`), http.StatusBadRequest},
		{
			"too large", http.MethodPost, "Bearer " + token, session,
			append(append([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"`),
				bytes.Repeat([]byte("a"), maxBody)...), `"}}}`...), http.StatusRequestEntityTooLarge,
		},
		{"a stream asked for", http.MethodGet, "Bearer " + token, session, nil, http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := raw(t, s, c.method, c.auth, c.session, c.body).StatusCode; got != c.want {
				t.Errorf("status = %d, want %d", got, c.want)
			}
		})
	}
	if calls := seenBy(t, s)["calls"]; calls != "0" {
		t.Errorf("%s refused calls reached the server", calls)
	}
}

func TestAnEndedSessionIsGone(t *testing.T) {
	s := startShim(t)
	session := open(t, s)
	auth := "Bearer " + token

	if got := raw(t, s, http.MethodDelete, auth, session, nil).StatusCode; got != http.StatusNoContent {
		t.Fatalf("DELETE = %d", got)
	}
	if got := raw(t, s, http.MethodPost, auth, session, echo).StatusCode; got != http.StatusNotFound {
		t.Errorf("a call on an ended session = %d", got)
	}
	if got := raw(t, s, http.MethodDelete, auth, session, nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("ending it again = %d", got)
	}
}

func reply200(t *testing.T, res *http.Response) map[string]json.RawMessage {
	t.Helper()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var m map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The server sees the shim's own ids; each caller gets its answer under the
// id it sent, whatever its type.
func TestEachAnswerCarriesTheCallersOwnID(t *testing.T) {
	s := startShim(t)
	session := open(t, s)
	for _, id := range []string{`"call-a"`, `41`, `"41"`} {
		body := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"}}}`)
		got := reply200(t, raw(t, s, http.MethodPost, "Bearer "+token, session, body))
		if string(got["id"]) != id {
			t.Errorf("sent id %s, answered under %s", id, got["id"])
		}
	}
}

func TestTheShimAnswersPingAndDiscoverItself(t *testing.T) {
	s := startShim(t)
	auth := "Bearer " + token
	session := open(t, s)

	pong := reply200(t, raw(t, s, http.MethodPost, auth, session, []byte(`{"jsonrpc":"2.0","id":"p","method":"ping"}`)))
	if string(pong["id"]) != `"p"` || string(pong["result"]) != `{}` {
		t.Errorf("ping = %s %s", pong["id"], pong["result"])
	}
	discover := reply200(t, raw(t, s, http.MethodPost, auth, "", []byte(`{"jsonrpc":"2.0","id":4,"method":"server/discover"}`)))
	if !strings.Contains(string(discover["error"]), "-32601") {
		t.Errorf("server/discover = %s, want method not found", discover["error"])
	}
	if got := raw(t, s, http.MethodPost, auth, session, []byte(`{"jsonrpc":"2.0","id":9,"result":{}}`)).StatusCode; got != http.StatusAccepted {
		t.Errorf("an answer from the client = %d", got)
	}
	if got := status(t, s, http.MethodGet, "/healthz", "", nil, ""); got != http.StatusOK {
		t.Errorf("healthz = %d", got)
	}
	if got := status(t, s, http.MethodPost, "/elsewhere", auth, nil, ""); got != http.StatusNotFound {
		t.Errorf("another path = %d", got)
	}
}

// A cancellation names the caller's id; the server must hear its own.
func TestACancellationReachesTheServerUnderItsOwnID(t *testing.T) {
	s := startShim(t)
	auth := "Bearer " + token
	session := open(t, s)

	slow := make(chan struct{})
	go func() {
		defer close(slow)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+"/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":77,"method":"tools/call","params":{"name":"slow","arguments":{"ms":400}}}`))
		req.Header.Set("Authorization", auth)
		req.Header.Set(sessionHeader, session)
		if res, err := http.DefaultClient.Do(req); err == nil {
			_ = res.Body.Close()
		}
	}()
	defer func() { <-slow }()
	waitFor(t, func() bool { _, ok := s.handler.sessions.server(session, "77"); return ok })

	if got := raw(t, s, http.MethodPost, auth, session,
		[]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":77}}`)).StatusCode; got != http.StatusAccepted {
		t.Fatalf("cancelled = %d", got)
	}
	waitFor(t, func() bool { return seenBy(t, s)["cancelled"] != "" })
	seen := seenBy(t, s)
	if seen["cancelled"] != seen["slow"] || seen["slow"] == "77" {
		t.Errorf("the server's slow call was %s and it heard %q cancelled", seen["slow"], seen["cancelled"])
	}
}

func TestACallerThatGivesUpCancelsTheCall(t *testing.T) {
	s := startShim(t)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := s.child.Call(ctx, "tools/call", json.RawMessage(`{"name":"slow","arguments":{"ms":400}}`), nil)
	if err == nil {
		t.Fatal("a cancelled call returned no error")
	}
	waitFor(t, func() bool { return seenBy(t, s)["cancelled"] != "" })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A file the server saved in its room is handed over at /file, from where
// the server runs, and only with the shim's token.
func TestAFileTheServerSavedIsDownloadedWithTheToken(t *testing.T) {
	s := startShim(t)
	if err := os.WriteFile(filepath.Join(s.handler.root, "invoice.pdf"), []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		auth   string
		status int
	}{
		"with the token": {"Bearer " + token, http.StatusOK},
		"without one":    {"", http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+"/file?path=invoice.pdf", nil)
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != c.status || (c.status == http.StatusOK && string(got) != "%PDF-1.7\n") {
				t.Errorf("%d %q", resp.StatusCode, got)
			}
		})
	}
}
