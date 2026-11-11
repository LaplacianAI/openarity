package mcpregistry

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const page = `{"servers":[
 {"server":{"name":"io.example/billing","description":"Billing","version":"1.2.0",
   "repository":{"url":"https://github.com/example/billing"},
   "remotes":[{"type":"streamable-http","url":"https://billing.example/mcp",
     "headers":[{"name":"Authorization","value":"Bearer {token}","isRequired":true,"isSecret":true}]}]},
  "_meta":{"io.modelcontextprotocol.registry/official":{"status":"active","isLatest":true}}},
 {"server":{"name":"io.example/old","description":"Gone"},
  "_meta":{"io.modelcontextprotocol.registry/official":{"status":"deprecated"}}},
 {"server":{"name":"io.example/files","description":"Files",
   "packages":[{"registryType":"npm","identifier":"files-mcp","version":"0.1.5","runtimeHint":"npx",
     "transport":{"type":"stdio"},
     "environmentVariables":[{"name":"ROOT","isRequired":true},{"name":"KEY","isSecret":true}]}]}}
]}`

func serve(t *testing.T, status int, body string) (*Client, *http.Request) {
	t.Helper()
	var got http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", srv.Client()), &got
}

func TestASearchReadsTheLatestActiveServers(t *testing.T) {
	t.Parallel()
	c, req := serve(t, http.StatusOK, page)

	got, err := c.Search(t.Context(), "billing & refunds", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if req.URL.Path != "/v0.1/servers" {
		t.Errorf("path = %q", req.URL.Path)
	}
	q := req.URL.Query()
	if q.Get("search") != "billing & refunds" || q.Get("limit") != "5" || q.Get("version") != "latest" {
		t.Errorf("query = %v", q)
	}

	if len(got) != 2 || got[0].Name != "io.example/billing" || got[1].Name != "io.example/files" {
		t.Fatalf("servers = %+v; want the deprecated one left out", got)
	}
	r := got[0].Remotes[0]
	if r.URL != "https://billing.example/mcp" || r.Type != "streamable-http" || got[0].Repository == "" {
		t.Errorf("remote = %+v", r)
	}
	if h := r.Headers[0]; h.Name != "Authorization" || !h.Secret || !h.Required || h.Format != "Bearer {token}" {
		t.Errorf("header = %+v", h)
	}
	p := got[1].Packages[0]
	if p.Identifier != "files-mcp" || p.Transport != "stdio" || len(p.Env) != 2 || !p.Env[1].Secret {
		t.Errorf("package = %+v", p)
	}
}

func TestASearchAsksForNoMoreThanItReturns(t *testing.T) {
	t.Parallel()
	c, req := serve(t, http.StatusOK, page)

	if _, err := c.Search(t.Context(), "x", 500); err != nil {
		t.Fatal(err)
	}
	if l := req.URL.Query().Get("limit"); l != "10" {
		t.Errorf("limit = %s, want the cap", l)
	}
	got, err := c.Search(t.Context(), "x", 1)
	if err != nil || len(got) != 1 {
		t.Errorf("limit 1: %d servers, %v", len(got), err)
	}
}

func TestARegistryThatFailsIsAnError(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"status":   {http.StatusServiceUnavailable, page},
		"not json": {http.StatusOK, "<html>"},
		"too big":  {http.StatusOK, `{"servers":[` + strings.Repeat(" ", maxBody) + `]}`},
	} {
		c, _ := serve(t, tc.status, tc.body)
		if _, err := c.Search(t.Context(), "x", 3); !errors.Is(err, ErrRegistry) {
			t.Errorf("%s: %v, want ErrRegistry", name, err)
		}
	}
}

func TestAnUnreachableRegistryIsAnError(t *testing.T) {
	t.Parallel()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	c := New("http://"+addr, http.DefaultClient)
	if _, err := c.Search(t.Context(), "x", 3); !errors.Is(err, ErrRegistry) {
		t.Errorf("%v, want ErrRegistry", err)
	}
}
