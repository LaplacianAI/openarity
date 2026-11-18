package mcpservers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

func mailServer() Request {
	req := commandServer(map[string]string{"EMAIL_PASS": root + "gmail#app_password"})
	req.Vars = map[string]string{"IMAP_HOST": "imap.gmail.com", "IMAP_PORT": "993"}
	req.Egress = []sandbox.HostPort{{Host: "IMAP.Gmail.com.", Port: 993}, {Host: "smtp.gmail.com", Port: 465}}
	return req
}

// A command server's plain settings and egress are stored as given, its
// hosts in lower case, beside env's references.
func TestACommandServerKeepsItsVarsAndEgress(t *testing.T) {
	t.Parallel()

	got, err := Parse(team, mailServer())
	if err != nil {
		t.Fatal(err)
	}
	var vars map[string]string
	var egress []sandbox.HostPort
	if err := json.Unmarshal(got.Vars, &vars); err != nil || vars["IMAP_HOST"] != "imap.gmail.com" || vars["IMAP_PORT"] != "993" {
		t.Errorf("vars = %s, %v", got.Vars, err)
	}
	if err := json.Unmarshal(got.Egress, &egress); err != nil || len(egress) != 2 || egress[0] != (sandbox.HostPort{Host: "imap.gmail.com", Port: 993}) {
		t.Errorf("egress = %s, %v", got.Egress, err)
	}
}

// Left out, they are stored empty, never null: the columns are NOT NULL.
func TestNoVarsOrEgressIsStoredEmpty(t *testing.T) {
	t.Parallel()

	for name, req := range map[string]Request{"a command server": commandServer(nil), "a url server": urlServer("https://mcp.example.com")} {
		got, err := Parse(team, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got.Vars) != "{}" || string(got.Egress) != "[]" {
			t.Errorf("%s: vars %s, egress %s; want {} and []", name, got.Vars, got.Egress)
		}
	}
}

func TestBadVarsAndEgressAreRefused(t *testing.T) {
	t.Parallel()

	many := make([]sandbox.HostPort, maxEgress+1)
	for i := range many {
		many[i] = sandbox.HostPort{Host: "h" + strings.Repeat("x", i) + ".example", Port: 443}
	}
	for name, tc := range map[string]struct {
		edit func(*Request)
		says string
	}{
		"vars on a url server": {func(r *Request) { *r = urlServer("https://mcp.example.com"); r.Vars = map[string]string{"A": "b"} }, "for a command server"},
		"egress on a url server": {func(r *Request) {
			*r = urlServer("https://mcp.example.com")
			r.Egress = []sandbox.HostPort{{Host: "a.example", Port: 443}}
		}, "for a command server"},
		"a var that is not a name": {func(r *Request) { r.Vars["NOT-A-NAME"] = "x" }, "not an environment variable name"},
		"a name in vars and env":   {func(r *Request) { r.Vars["EMAIL_PASS"] = "plain" }, "in both vars and env"},
		"a var with a NUL":         {func(r *Request) { r.Vars["IMAP_HOST"] = "a\x00b" }, "NUL"},
		"a var too long":           {func(r *Request) { r.Vars["IMAP_HOST"] = strings.Repeat("a", maxVarBytes+1) }, "longer than"},
		"port zero":                {func(r *Request) { r.Egress[0].Port = 0 }, "between 1 and 65535"},
		"a port too high":          {func(r *Request) { r.Egress[0].Port = 70000 }, "between 1 and 65535"},
		"a host with a path":       {func(r *Request) { r.Egress[0].Host = "imap.gmail.com/x" }, "not a host name"},
		"a host with a port in it": {func(r *Request) { r.Egress[0].Host = "imap.gmail.com:993" }, "not a host name"},
		"an empty host":            {func(r *Request) { r.Egress[0].Host = "" }, "not a host name"},
		"a private address":        {func(r *Request) { r.Egress[0].Host = "10.0.0.5" }, "not a public address"},
		"loopback":                 {func(r *Request) { r.Egress[0].Host = "127.0.0.1" }, "not a public address"},
		"a link-local address":     {func(r *Request) { r.Egress[0].Host = "169.254.169.254" }, "not a public address"},
		"an IPv6 private address":  {func(r *Request) { r.Egress[0].Host = "fd00::1" }, "not a public address"},
		"localhost":                {func(r *Request) { r.Egress[0].Host = "localhost" }, "this machine"},
		"a name under localhost":   {func(r *Request) { r.Egress[0].Host = "db.localhost" }, "this machine"},
		"the same place twice":     {func(r *Request) { r.Egress[1] = sandbox.HostPort{Host: "imap.gmail.com", Port: 993} }, "twice"},
		"too many places":          {func(r *Request) { r.Egress = many }, "at most"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := mailServer()
			tc.edit(&req)
			if _, err := Parse(team, req); err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want one saying %q", err, tc.says)
			}
		})
	}
}

// A public address is a place a room may go.
func TestAPublicAddressIsAnEgress(t *testing.T) {
	t.Parallel()

	req := mailServer()
	req.Egress = []sandbox.HostPort{{Host: "1.1.1.1", Port: 443}, {Host: "2606:4700:4700::1111", Port: 443}}
	if _, err := Parse(team, req); err != nil {
		t.Errorf("parse: %v", err)
	}
}

// What a create stores is what the API then answers with.
func TestACreatedCommandServerAnswersWithItsVarsAndEgress(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newFakeStore()
	b := commandBody(teamID, "mail")
	b["vars"] = map[string]string{"IMAP_HOST": "imap.gmail.com"}
	b["egress"] = []map[string]any{{"host": "imap.gmail.com", "port": 993}}
	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, collection(teamID), b)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}

	if got := s.created[0]; string(got.Vars) != `{"IMAP_HOST":"imap.gmail.com"}` || string(got.Egress) != `[{"host":"imap.gmail.com","port":993}]` {
		t.Errorf("stored vars %s, egress %s", got.Vars, got.Egress)
	}
	out := decodeServer(t, rec)
	if out.Vars["IMAP_HOST"] != "imap.gmail.com" || len(out.Egress) != 1 || out.Egress[0].Port != 993 {
		t.Errorf("answered vars %v, egress %v", out.Vars, out.Egress)
	}
}
