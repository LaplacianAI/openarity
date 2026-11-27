package e2b

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

const (
	apiKey      = "e2b_key_SECRET1"
	envdToken   = "envd-token-SECRET2"
	trafficTok  = "traffic-token-SECRET3"
	envSecret   = "hunter2-SECRET4"
	sandboxID   = "isbx123abc"
	templateID  = "tmpl_mail"
	connectJSON = "application/connect+json"
)

// call is one request the fake saw.
type call struct {
	Method, Host, Path string
	Header             http.Header
	Body               []byte
}

// fake is E2B: a handler per host, reached through a transport, so the
// hostnames the client builds are the ones under test.
type fake struct {
	mu    sync.Mutex
	calls []call
	api   func(w http.ResponseWriter, r *http.Request)
	envd  func(w http.ResponseWriter, r *http.Request)
}

func (f *fake) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Host, r.URL.Path, r.Header.Clone(), body})
	f.mu.Unlock()
	rec := httptest.NewRecorder()
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(strings.NewReader(string(body)))
	r2.Host = r.URL.Host
	if strings.HasPrefix(r.URL.Host, "49983-") {
		f.envd(rec, r2)
	} else {
		f.api(rec, r2)
	}
	return rec.Result(), nil
}

func (f *fake) last(t *testing.T) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no request reached E2B")
	}
	return f.calls[len(f.calls)-1]
}

func newClient(f *fake) *Client {
	c := New(apiKey, &http.Client{Transport: f})
	c.Resolve = func(_ context.Context, h string) ([]string, error) {
		if h == "imap.example.com" {
			return []string{"192.0.2.10", "192.0.2.11"}, nil
		}
		if h == "nowhere.example.com" {
			return nil, nil
		}
		return nil, errors.New("unexpected lookup " + h)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func okCreate(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 201, map[string]any{
		"sandboxID": sandboxID, "templateID": templateID, "clientID": "x", "envdVersion": "0.5.0",
		"envdAccessToken": envdToken, "trafficAccessToken": trafficTok, "domain": nil,
	})
}

func envelope(flags byte, v any) []byte {
	b, _ := json.Marshal(v)
	out := make([]byte, 5, 5+len(b))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:], uint32(len(b)))
	return append(out, b...)
}

func connectStream(w http.ResponseWriter, envs ...[]byte) {
	w.Header().Set("Content-Type", connectJSON)
	w.WriteHeader(200)
	for _, e := range envs {
		_, _ = w.Write(e)
	}
}

func startEvent(pid int) []byte {
	return envelope(0, map[string]any{"event": map[string]any{"start": map[string]any{"pid": pid}}})
}

func TestCreateSendsTheTemplateEnvLabelsAndAPrivateNetwork(t *testing.T) {
	f := &fake{api: okCreate}
	c := newClient(f)
	room, err := c.Create(t.Context(), sandbox.Spec{
		Image:  templateID,
		Env:    map[string]string{"IMAP_PASSWORD": envSecret},
		Labels: map[string]string{"team": "t1"},
		Egress: []sandbox.HostPort{
			{Host: "Registry.NPMJS.org", Port: 443},
			{Host: "imap.example.com", Port: 993},
			{Host: "203.0.113.5", Port: 465},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.last(t)
	if got.Method != "POST" || got.Host != "api.e2b.app" || got.Path != "/v2/sandboxes" {
		t.Fatalf("call = %s %s%s", got.Method, got.Host, got.Path)
	}
	if got.Header.Get("X-API-Key") != apiKey || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", got.Header)
	}
	var body struct {
		TemplateID string            `json:"templateID"`
		Timeout    int               `json:"timeout"`
		EnvVars    map[string]string `json:"envVars"`
		Metadata   map[string]string `json:"metadata"`
		Secure     *bool             `json:"secure"`
		Network    struct {
			AllowPublicTraffic *bool    `json:"allowPublicTraffic"`
			AllowOut           []string `json:"allowOut"`
			DenyOut            []string `json:"denyOut"`
		} `json:"network"`
	}
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.TemplateID != templateID || body.Timeout != 3600 {
		t.Errorf("template/timeout = %q/%d", body.TemplateID, body.Timeout)
	}
	if body.EnvVars["IMAP_PASSWORD"] != envSecret || body.Metadata["team"] != "t1" {
		t.Errorf("env/metadata = %v / %v", body.EnvVars, body.Metadata)
	}
	if body.Network.AllowPublicTraffic == nil || *body.Network.AllowPublicTraffic {
		t.Error("allowPublicTraffic must be sent, and false")
	}
	if body.Secure == nil || !*body.Secure {
		t.Error("secure must be sent, and true: envd then wants its token from inside the room too")
	}
	if strings.Join(body.Network.DenyOut, ",") != "0.0.0.0/0" {
		t.Errorf("denyOut = %v", body.Network.DenyOut)
	}
	want := "192.0.2.10,192.0.2.11,203.0.113.5,registry.npmjs.org"
	if strings.Join(body.Network.AllowOut, ",") != want {
		t.Errorf("allowOut = %v, want %s", body.Network.AllowOut, want)
	}
	if strings.Contains(room, trafficTok) {
		// base64 hides it from a plain match; the room is still a credential,
		// which the URL test checks by round trip.
		t.Error("room shows the traffic token in the clear")
	}
}

func TestCreateWithNoEgressDeniesEverything(t *testing.T) {
	f := &fake{api: okCreate}
	if _, err := newClient(f).Create(t.Context(), sandbox.Spec{Image: templateID}); err != nil {
		t.Fatal(err)
	}
	var body map[string]map[string]any
	_ = json.Unmarshal(f.last(t).Body, &body)
	if _, ok := body["network"]["allowOut"]; ok {
		t.Error("an empty egress sent allowOut")
	}
	if d, _ := body["network"]["denyOut"].([]any); len(d) != 1 || d[0] != "0.0.0.0/0" {
		t.Errorf("denyOut = %v", d)
	}
}

func TestCreateRefusesWhatItCannotHonour(t *testing.T) {
	for name, s := range map[string]sandbox.Spec{
		"no template":        {},
		"a wildcard on 993":  {Image: "t", Egress: []sandbox.HostPort{{Host: "*.example.com", Port: 993}}},
		"a host with no IP":  {Image: "t", Egress: []sandbox.HostPort{{Host: "nowhere.example.com", Port: 993}}},
		"a bad port":         {Image: "t", Egress: []sandbox.HostPort{{Host: "a.example.com", Port: 0}}},
		"a host with a path": {Image: "t", Egress: []sandbox.HostPort{{Host: "a.example.com/x", Port: 443}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{api: okCreate}
			_, err := newClient(f).Create(t.Context(), s)
			if !errors.Is(err, sandbox.ErrRefused) {
				t.Fatalf("err = %v, want ErrRefused", err)
			}
			if len(f.calls) != 0 {
				t.Error("a refused spec still reached E2B")
			}
		})
	}
}

func TestCreateAWildcardOnPort443IsADomainRule(t *testing.T) {
	f := &fake{api: okCreate}
	_, err := newClient(f).Create(t.Context(), sandbox.Spec{Image: "t", Egress: []sandbox.HostPort{{Host: "*.example.com", Port: 443}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.last(t).Body), `"allowOut":["*.example.com"]`) {
		t.Errorf("body = %s", f.last(t).Body)
	}
}

func TestCreateAResolverFailureIsNotARefusal(t *testing.T) {
	c := newClient(&fake{api: okCreate})
	_, err := c.Create(t.Context(), sandbox.Spec{Image: "t", Egress: []sandbox.HostPort{{Host: "other.example.com", Port: 993}}})
	if err == nil || errors.Is(err, sandbox.ErrRefused) {
		t.Fatalf("err = %v, want a plain error", err)
	}
}

func TestCreateMapsE2BsAnswers(t *testing.T) {
	for _, tc := range []struct {
		status  int
		refused bool
	}{{400, true}, {404, true}, {403, true}, {401, false}, {500, false}, {503, false}} {
		f := &fake{api: func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, tc.status, map[string]any{"code": tc.status, "message": "no thanks"})
		}}
		_, err := newClient(f).Create(t.Context(), sandbox.Spec{Image: "t"})
		if err == nil || errors.Is(err, sandbox.ErrRefused) != tc.refused {
			t.Errorf("status %d: err = %v, refused want %v", tc.status, err, tc.refused)
		}
		if errors.Is(err, sandbox.ErrNotFound) {
			t.Errorf("status %d: a template that is missing is not a room that is missing", tc.status)
		}
		if err != nil && !strings.Contains(err.Error(), "no thanks") {
			t.Errorf("status %d: E2B's message is missing: %v", tc.status, err)
		}
	}
}

func TestErrorsCarryNeitherSecretsNorALongMessage(t *testing.T) {
	long := strings.Repeat("x", 500)
	f := &fake{api: func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 400, map[string]any{"code": 400, "message": "bad value " + envSecret + " and key " + apiKey + " " + long})
	}}
	_, err := newClient(f).Create(t.Context(), sandbox.Spec{Image: "t", Env: map[string]string{"P": envSecret}})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, s := range []string{envSecret, apiKey} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %q: %v", s, err)
		}
	}
	if len(err.Error()) > 400 {
		t.Errorf("error is %d bytes: not cut", len(err.Error()))
	}
}

func TestATransportErrorDoesNotLeakTheKey(t *testing.T) {
	c := New(apiKey, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed for " + apiKey)
	})})
	_, err := c.Create(t.Context(), sandbox.Spec{Image: "t"})
	if err == nil || strings.Contains(err.Error(), apiKey) {
		t.Fatalf("err = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func createRoom(t *testing.T, f *fake) (*Client, string) {
	t.Helper()
	c := newClient(f)
	room, err := c.Create(t.Context(), sandbox.Spec{Image: templateID})
	if err != nil {
		t.Fatal(err)
	}
	return c, room
}

func TestURLIsThePortAndTheSandboxOnTheDomainWithTheTrafficToken(t *testing.T) {
	f := &fake{api: okCreate}
	c, room := createRoom(t, f)
	before := len(f.calls)
	ep, err := c.URL(t.Context(), room, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "https://8080-"+sandboxID+".e2b.app" {
		t.Errorf("URL = %s", ep.URL)
	}
	if ep.Header.Get("e2b-traffic-access-token") != trafficTok {
		t.Errorf("header = %v", ep.Header)
	}
	if len(f.calls) != before {
		t.Error("URL called E2B")
	}
}

func TestURLUsesTheDomainE2BReturns(t *testing.T) {
	f := &fake{api: func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"sandboxID": "s1", "templateID": "t", "clientID": "c", "envdVersion": "1", "domain": "e2b.internal.example"})
	}}
	c, room := createRoom(t, f)
	ep, _ := c.URL(t.Context(), room, 8080)
	if ep.URL != "https://8080-s1.e2b.internal.example" {
		t.Errorf("URL = %s", ep.URL)
	}
	if len(ep.Header) != 0 {
		t.Errorf("a sandbox with no traffic token got headers %v", ep.Header)
	}
}

func TestURLRefusesWhatIsNotARoom(t *testing.T) {
	c := newClient(&fake{})
	for _, r := range []string{"", "isbx123", "e2b1.!!!", "e2b1." + base64.RawURLEncoding.EncodeToString([]byte(`{}`))} {
		if _, err := c.URL(t.Context(), r, 8080); err == nil {
			t.Errorf("room %q accepted", r)
		}
	}
	_, room := createRoom(t, &fake{api: okCreate})
	if _, err := c.URL(t.Context(), room, 0); err == nil {
		t.Error("port 0 accepted")
	}
}

func TestStartAsksForTheTokenThenSpeaksConnectToEnvd(t *testing.T) {
	f := &fake{
		api: func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				writeJSON(w, 200, map[string]any{"sandboxID": sandboxID, "envdAccessToken": envdToken})
				return
			}
			okCreate(w, r)
		},
		envd: func(w http.ResponseWriter, _ *http.Request) {
			connectStream(w, envelope(0, map[string]any{"event": map[string]any{"keepalive": map[string]any{}}}), startEvent(42))
		},
	}
	c, room := createRoom(t, f)
	argv := []string{"sandbox", "shim", "--listen", ":8080", "--", "npx", "-y", "it's@2.1.3"}
	if err := c.Start(t.Context(), room, argv); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	get, post := f.calls[1], f.calls[2]
	f.mu.Unlock()
	if get.Method != "GET" || get.Path != "/sandboxes/"+sandboxID || get.Header.Get("X-API-Key") != apiKey {
		t.Errorf("token call = %+v", get)
	}
	if post.Host != "49983-"+sandboxID+".e2b.app" || post.Path != "/process.Process/Start" {
		t.Fatalf("start call = %s%s", post.Host, post.Path)
	}
	h := post.Header
	if h.Get("Content-Type") != connectJSON || h.Get("Connect-Protocol-Version") != "1" {
		t.Errorf("connect headers = %v", h)
	}
	if h.Get("X-Access-Token") != envdToken {
		t.Errorf("X-Access-Token = %q", h.Get("X-Access-Token"))
	}
	if h.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:")) {
		t.Errorf("Authorization = %q", h.Get("Authorization"))
	}
	if h.Get("X-API-Key") != "" || h.Get("E2b-Traffic-Access-Token") != "" {
		t.Error("envd was sent the API key or the traffic token")
	}
	// One envelope: flag 0, length, JSON.
	if post.Body[0] != 0 || int(binary.BigEndian.Uint32(post.Body[1:5])) != len(post.Body)-5 {
		t.Fatalf("framing = % x", post.Body[:5])
	}
	var msg struct {
		Process struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		} `json:"process"`
		Stdin *bool `json:"stdin"`
	}
	if err := json.Unmarshal(post.Body[5:], &msg); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-l", "-c", `exec 'sandbox' 'shim' '--listen' ':8080' '--' 'npx' '-y' 'it'\''s@2.1.3'`}
	if msg.Process.Cmd != "/bin/bash" || strings.Join(msg.Process.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("process = %+v", msg.Process)
	}
	if msg.Stdin == nil || *msg.Stdin {
		t.Error("stdin must be sent, and false")
	}
}

func startWith(t *testing.T, envd, apiGet http.HandlerFunc) error {
	t.Helper()
	f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			apiGet(w, r)
			return
		}
		okCreate(w, r)
	}, envd: envd}
	c, room := createRoom(t, f)
	return c.Start(t.Context(), room, []string{"x"})
}

func getOK(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"envdAccessToken": envdToken})
}

func TestStartMapsFailures(t *testing.T) {
	t.Run("the sandbox is not secure", func(t *testing.T) {
		started := false
		err := startWith(t, func(w http.ResponseWriter, _ *http.Request) { started = true }, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"envdAccessToken": ""})
		})
		if err == nil || !strings.Contains(err.Error(), "not made secure") || started {
			t.Fatalf("err = %v, started %v: a sandbox with no envd token must not be started in", err, started)
		}
	})
	t.Run("the sandbox is gone", func(t *testing.T) {
		err := startWith(t, nil, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 404, map[string]any{"code": 404, "message": "sandbox not found"})
		})
		if !errors.Is(err, sandbox.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the proxy says 404 for envd", func(t *testing.T) {
		err := startWith(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 404, map[string]any{"code": "not_found", "message": "gone"})
		}, getOK)
		if !errors.Is(err, sandbox.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("envd rejects the token", func(t *testing.T) {
		err := startWith(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 401, map[string]any{"code": "unauthenticated", "message": "bad token " + envdToken})
		}, getOK)
		if err == nil || errors.Is(err, sandbox.ErrNotFound) || strings.Contains(err.Error(), envdToken) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "unauthenticated") {
			t.Errorf("envd's code is missing: %v", err)
		}
	})
	t.Run("the stream ends with an error", func(t *testing.T) {
		err := startWith(t, func(w http.ResponseWriter, _ *http.Request) {
			connectStream(w, envelope(flagEndStream, map[string]any{"error": map[string]any{"code": "invalid_argument", "message": "no such file"}}))
		}, getOK)
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the process ends before it starts", func(t *testing.T) {
		err := startWith(t, func(w http.ResponseWriter, _ *http.Request) {
			connectStream(w, envelope(0, map[string]any{"event": map[string]any{"end": map[string]any{"exitCode": 127, "status": "exit status 127", "error": "command not found"}}}))
		}, getOK)
		if err == nil || !strings.Contains(err.Error(), "command not found") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the stream is empty", func(t *testing.T) {
		if err := startWith(t, func(w http.ResponseWriter, _ *http.Request) { connectStream(w) }, getOK); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("nothing to run", func(t *testing.T) {
		f := &fake{api: okCreate}
		c, room := createRoom(t, f)
		if err := c.Start(t.Context(), room, nil); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestStartDoesNotWaitForTheCommandToEnd(t *testing.T) {
	// The body carries the start and then data and an end; Start must report
	// success on the start, not read on to the exit status.
	err := startWith(t, func(w http.ResponseWriter, _ *http.Request) {
		connectStream(w, startEvent(7),
			envelope(0, map[string]any{"event": map[string]any{"end": map[string]any{"exitCode": 1, "status": "exit status 1"}}}))
	}, getOK)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteKillsTheSandboxAndAGoneOneIsNotAnError(t *testing.T) {
	for status, wantErr := range map[int]bool{204: false, 404: false, 500: true, 401: true} {
		f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				if status == 204 {
					w.WriteHeader(204)
					return
				}
				writeJSON(w, status, map[string]any{"code": status, "message": "m"})
				return
			}
			okCreate(w, r)
		}}
		c, room := createRoom(t, f)
		err := c.Delete(t.Context(), room)
		if (err != nil) != wantErr {
			t.Errorf("status %d: err = %v", status, err)
		}
		got := f.last(t)
		if got.Method != "DELETE" || got.Path != "/sandboxes/"+sandboxID || got.Header.Get("X-API-Key") != apiKey {
			t.Errorf("status %d: call = %+v", status, got)
		}
	}
}

func TestTheRoomHoldsNoEnvValueAndNoEnvdToken(t *testing.T) {
	f := &fake{api: okCreate}
	c := newClient(f)
	room, err := c.Create(t.Context(), sandbox.Spec{Image: templateID, Env: map[string]string{"P": envSecret}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(room, roomPrefix))
	for _, s := range []string{envSecret, envdToken, apiKey} {
		if strings.Contains(string(raw), s) {
			t.Errorf("room carries %q", s)
		}
	}
}

// The real thing, when someone has a key: make a sandbox from E2B's base
// template, run a command, reach the port and delete it.
func TestAgainstE2B(t *testing.T) {
	key := os.Getenv("E2B_TEST_API_KEY")
	if key == "" {
		t.Skip("E2B_TEST_API_KEY is not set")
	}
	tmpl := os.Getenv("E2B_TEST_TEMPLATE")
	if tmpl == "" {
		tmpl = "base"
	}
	c := New(key, nil)
	room, err := c.Create(t.Context(), sandbox.Spec{Image: tmpl, Labels: map[string]string{"test": "openarity"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(context.WithoutCancel(t.Context()), room) })
	if err := c.Start(t.Context(), room, []string{"python3", "-m", "http.server", "8080"}); err != nil {
		t.Fatal(err)
	}
	ep, err := c.URL(t.Context(), room, 8080)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ep.URL, nil)
	req.Header = ep.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("with the token: %d", resp.StatusCode)
	}
	bare, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ep.URL, nil)
	resp2, err := http.DefaultClient.Do(bare)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Fatalf("without the token: %d, want 403", resp2.StatusCode)
	}
}

// A start request too large for its frame is refused before envd is called.
func TestStartRefusesACommandTooLargeToFrame(t *testing.T) {
	f := &fake{
		api: func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				writeJSON(w, 200, map[string]any{"sandboxID": sandboxID, "envdAccessToken": envdToken})
				return
			}
			okCreate(w, r)
		},
		envd: func(w http.ResponseWriter, _ *http.Request) {
			connectStream(w, startEvent(42))
		},
	}
	c, room := createRoom(t, f)
	err := c.Start(t.Context(), room, []string{"echo", strings.Repeat("x", maxStartRequest)})
	if !errors.Is(err, sandbox.ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Path == "/process.Process/Start" {
			t.Fatal("envd was sent a start request over the bound")
		}
	}
}

// A public room reaches every address but a private one: E2B lets through
// what denyOut does not name when nothing is allowed.
func TestAPublicRoomDeniesOnlyPrivateAddresses(t *testing.T) {
	f := &fake{api: okCreate}
	if _, err := newClient(f).Create(t.Context(), sandbox.Spec{Image: templateID, Network: sandbox.NetworkPublic, Workspace: true}); err != nil {
		t.Fatal(err)
	}
	var body map[string]map[string]any
	_ = json.Unmarshal(f.last(t).Body, &body)
	if _, ok := body["network"]["allowOut"]; ok {
		t.Error("a public room sent allowOut")
	}
	deny, _ := body["network"]["denyOut"].([]any)
	got := map[string]bool{}
	for _, d := range deny {
		if s, ok := d.(string); ok {
			got[s] = true
		}
	}
	for _, want := range []string{"10.0.0.0/8", "169.254.0.0/16", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fe80::/10"} {
		if !got[want] {
			t.Errorf("denyOut %v lacks %s", deny, want)
		}
	}
	if got["0.0.0.0/0"] {
		t.Error("a public room denied everything")
	}
}

func TestAPublicRoomThatListsHostsIsRefused(t *testing.T) {
	for name, s := range map[string]sandbox.Spec{
		"hosts":              {Image: "t", Network: sandbox.NetworkPublic, Egress: []sandbox.HostPort{{Host: "a.example.com", Port: 443}}},
		"an unknown network": {Image: "t", Network: "host"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{api: okCreate}
			if _, err := newClient(f).Create(t.Context(), s); !errors.Is(err, sandbox.ErrRefused) {
				t.Fatalf("err = %v, want ErrRefused", err)
			}
			if len(f.calls) != 0 {
				t.Error("a refused spec still reached E2B")
			}
		})
	}
}

func TestStopPausesAndResumeConnects(t *testing.T) {
	f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/sandboxes":
			okCreate(w, r)
		case "/sandboxes/" + sandboxID + "/pause":
			w.WriteHeader(204)
		case "/v2/sandboxes/" + sandboxID + "/connect":
			writeJSON(w, 200, map[string]any{"sandboxID": sandboxID})
		default:
			http.NotFound(w, r)
		}
	}}
	c, room := createRoom(t, f)
	if err := c.Stop(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t); got.Method != http.MethodPost || got.Path != "/sandboxes/"+sandboxID+"/pause" {
		t.Fatalf("stop = %s %s", got.Method, got.Path)
	}
	if err := c.Resume(t.Context(), room, []string{"ignored"}); err != nil {
		t.Fatal(err)
	}
	got := f.last(t)
	var body map[string]any
	_ = json.Unmarshal(got.Body, &body)
	if got.Path != "/v2/sandboxes/"+sandboxID+"/connect" || body["timeout"] != float64(3600) {
		t.Fatalf("resume = %s %s", got.Path, got.Body)
	}
}

func TestStopOfAPausedSandboxIsNotAnError(t *testing.T) {
	f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/sandboxes" {
			okCreate(w, r)
			return
		}
		writeJSON(w, 409, map[string]any{"message": "already paused"})
	}}
	c, room := createRoom(t, f)
	if err := c.Stop(t.Context(), room); err != nil {
		t.Fatal(err)
	}
}

func TestAResumeE2BLimitsIsErrFullAndAGoneOneErrNotFound(t *testing.T) {
	for status, want := range map[int]error{429: sandbox.ErrFull, 404: sandbox.ErrNotFound} {
		f := &fake{api: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/sandboxes" {
				okCreate(w, r)
				return
			}
			writeJSON(w, status, map[string]any{"message": "no " + apiKey})
		}}
		c, room := createRoom(t, f)
		err := c.Resume(t.Context(), room, nil)
		if !errors.Is(err, want) || strings.Contains(err.Error(), apiKey) {
			t.Errorf("%d: %v, want %v without the key", status, err, want)
		}
	}
}

// E2B's control plane down behind its gateway is unavailable.
func TestAnE2BThatIsDownIsUnavailable(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		f := &fake{api: func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, status, map[string]any{"message": "upstream"})
		}}
		_, err := newClient(f).Create(t.Context(), sandbox.Spec{Image: templateID})
		if !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("%d: err = %v, want ErrUnavailable", status, err)
		}
	}
	f := &fake{api: func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 500, map[string]any{"message": "bug"})
	}}
	if _, err := newClient(f).Create(t.Context(), sandbox.Spec{Image: templateID}); sandbox.Waitable(err) {
		t.Errorf("a 500 is waited for: %v", err)
	}
}
