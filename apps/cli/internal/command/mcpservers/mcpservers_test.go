package mcpservers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID   = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	serverID = "1c0e5b71-8d2c-4b0d-9a11-3f9a41c27e58"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`

const github = `{"id":"` + serverID + `","team_id":"` + teamID + `","name":"github","url":"https://mcp.example.com",
  "auth_secret_ref":"teams/` + teamID + `/mcp/github#token","env":{},"bare":false,"discovered_at":"2026-10-02T10:00:00Z",
  "created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`

const servers = `{"items":[` + github + `,
  {"id":"8b3d0a6e-9f4c-4f3a-9c3a-4d0a6e9f4c6f","team_id":"` + teamID + `","name":"files","command":["npx","server"],
   "env":{"TOKEN":"teams/x/mcp/files#k"},"bare":true,"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}]}`

const tools = `{"items":[{"name":"search_code","description":"Search\ncode\u001b[2J everywhere","input_schema":{"type":"object"}}]}`

func routes(extra map[string]clitest.Reply) map[string]clitest.Reply {
	r := map[string]clitest.Reply{
		"GET /teams":                  {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/mcp-servers": {Status: http.StatusOK, Body: servers},
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func sent(t *testing.T, script *clitest.Transcript, method string) map[string]any {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == method {
			var body map[string]any
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("not json: %v\n%s", err, e.Body)
			}
			return body
		}
	}
	t.Fatalf("no %s was sent", method)
	return nil
}

func TestListShowsTransportAndDiscovery(t *testing.T) {
	clitest.Routes(t, routes(nil))

	out, err := clitest.Execute(t, commands, "mcp-servers", "list", "platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"github", "url", "2026-10-02 10:00", "files", "command", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestTheShortNameWorks(t *testing.T) {
	clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "mcp", "list", "platform"); err != nil {
		t.Errorf("mcp list: %v", err)
	}
}

func TestCreatingAURLServerSendsTheURLAndTheReference(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers": {Status: http.StatusCreated, Body: github},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "create", "platform", "github",
		"--url", "https://mcp.example.com", "--auth-secret-ref", "teams/t/mcp/github#token")
	if err != nil || !strings.Contains(out, "registered") {
		t.Fatalf("create: %v\n%s", err, out)
	}
	body := sent(t, script, http.MethodPost)
	if body["url"] != "https://mcp.example.com" || body["auth_secret_ref"] != "teams/t/mcp/github#token" || body["name"] != "github" {
		t.Errorf("body = %v", body)
	}
	if _, has := body["command"]; has {
		t.Errorf("a url server was sent a command: %v", body)
	}
}

// What follows -- is the command, flags and all.
func TestCreatingACommandServerSendsTheArgvAfterTheDashes(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers": {Status: http.StatusCreated, Body: github},
	}))

	_, err := clitest.Execute(t, commands, "mcp-servers", "create", "platform", "files", "--bare",
		"--env", "TOKEN=teams/t/mcp/files#k", "--", "npx", "-y", "@mcp/server", "--root", "/data")
	if err != nil {
		t.Fatal(err)
	}
	body := sent(t, script, http.MethodPost)
	argv, _ := body["command"].([]any)
	if len(argv) != 5 || argv[0] != "npx" || argv[3] != "--root" {
		t.Errorf("command = %v", body["command"])
	}
	env, _ := body["env"].(map[string]any)
	if env["TOKEN"] != "teams/t/mcp/files#k" || body["bare"] != true {
		t.Errorf("body = %v", body)
	}
}

func TestADefinitionTheAPICannotExpressIsRefusedBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"neither":                  {"create", "platform", "x"},
		"both":                     {"create", "platform", "x", "--url", "https://a.b", "--", "npx"},
		"env on a url server":      {"create", "platform", "x", "--url", "https://a.b", "--env", "A=b"},
		"a reference on a command": {"create", "platform", "x", "--auth-secret-ref", "r#k", "--", "npx"},
		"env without =":            {"create", "platform", "x", "--env", "A", "--", "npx"},
		"a blank name":             {"create", "platform", " ", "--url", "https://a.b"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			if _, err := clitest.Execute(t, commands, append([]string{"mcp-servers"}, args...)...); err == nil {
				t.Error("accepted")
			}
			if n := len(script.All()); n != 0 {
				t.Errorf("%d requests", n)
			}
		})
	}
}

// A tool's description is the server's text. One that tries to clear the
// screen is quoted, and it stays on one row.
func TestToolsAreQuotedBecauseTheServerWroteThem(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/mcp-servers/{serverID}/tools": {Status: http.StatusOK, Body: tools},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "tools", "platform", "github")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Error("an escape sequence from the server reached the terminal")
	}
	if !strings.Contains(out, `"search_code"`) || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("out = %q", out)
	}

	// json carries what arrived.
	raw, err := clitest.Execute(t, commands, "mcp-servers", "tools", "platform", "github", "-o", "json")
	if err != nil || !strings.Contains(raw, `\u001b[2J`) {
		t.Errorf("json lost the description as sent: %v\n%s", err, raw)
	}
}

func TestDiscoverPrintsWhatItFound(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/mcp-servers/{serverID}/discover": {Status: http.StatusOK, Body: tools},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "discover", "platform", "github")
	if err != nil || !strings.Contains(out, "search_code") {
		t.Fatalf("discover: %v\n%s", err, out)
	}
	if script.Calls("POST", "/teams/"+teamID+"/mcp-servers/"+serverID+"/discover") != 1 {
		t.Errorf("requests = %+v", script.All())
	}
}

func TestNoToolsSaysWhy(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/mcp-servers/{serverID}/tools": {Status: http.StatusOK, Body: `{"items":[]}`},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "tools", "platform", "github", "-o", "json")
	if err != nil || !strings.Contains(out, `"items": []`) && !strings.Contains(out, `"items":[]`) {
		t.Errorf("an empty list must still be a list: %v\n%s", err, out)
	}
}

func TestEveryWriteNamesToolWriteWhenRefused(t *testing.T) {
	forbidden := clitest.Reply{Status: http.StatusForbidden, Body: `{"error":"forbidden"}`}

	for name, tc := range map[string]struct {
		route string
		args  []string
	}{
		"create":   {"POST /teams/{id}/mcp-servers", []string{"create", "platform", "x", "--url", "https://a.b"}},
		"replace":  {"PUT /teams/{id}/mcp-servers/{serverID}", []string{"replace", "platform", "github", "github", "--url", "https://a.b"}},
		"delete":   {"DELETE /teams/{id}/mcp-servers/{serverID}", []string{"delete", "platform", "github"}},
		"discover": {"POST /teams/{id}/mcp-servers/{serverID}/discover", []string{"discover", "platform", "github"}},
	} {
		t.Run(name, func(t *testing.T) {
			clitest.Routes(t, routes(map[string]clitest.Reply{tc.route: forbidden}))
			_, err := clitest.Execute(t, commands, append([]string{"mcp-servers"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), "tool:write") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestGetShowsTheReferencesNotSecrets(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/mcp-servers/{serverID}": {Status: http.StatusOK, Body: github},
	}))

	out, err := clitest.Execute(t, commands, "mcp-servers", "get", "platform", "github")
	if err != nil || !strings.Contains(out, "teams/"+teamID+"/mcp/github#token") {
		t.Errorf("get: %v\n%s", err, out)
	}
}

// A member with tool:write writes a server's name, URL, command and
// references, and whoever lists the servers reads them. One that carries an
// escape sequence must not reach that reader's terminal raw.
func TestATeamMembersEscapeSequencesDoNotReachTheTerminal(t *testing.T) {
	evil := `{"id":"` + serverID + `","team_id":"` + teamID + `","name":"git\u001b[2Jhub","url":"https://a.b/\u001b]0;pwned\u0007",
	  "auth_secret_ref":"teams/x/mcp/g#k\u001b[31m","command":["npx","\u001b[2J"],"env":{"A\u001b[2J":"r\u001b[2J#k"},
	  "bare":false,"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`
	clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams":                             {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/mcp-servers":            {Status: http.StatusOK, Body: `{"items":[` + evil + `]}`},
		"GET /teams/{id}/mcp-servers/{serverID}": {Status: http.StatusOK, Body: evil},
	})

	for _, args := range [][]string{
		{"mcp-servers", "list", "platform"},
		{"mcp-servers", "get", "platform", serverID},
	} {
		out, err := clitest.Execute(t, commands, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.ContainsAny(out, "\x1b\x07") {
			t.Errorf("%v printed a raw escape:\n%q", args, out)
		}
	}
}
