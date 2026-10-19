package agents

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID   = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	agentID  = "3f9a41c2-7e58-4b0d-9a11-8d2c0e5b71c1"
	serverID = "1c0e5b71-8d2c-4b0d-9a11-3f9a41c27e58"
	skillID  = "9a114b0d-7e58-4b0d-9a11-8d2c0e5b71c1"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`

const oneAgentPage = `{"items":[{"id":"` + agentID + `","team_id":"` + teamID + `","name":"triage","description":"Sorts issues",
  "kind":"custom","model":{"name":"anthropic/claude-sonnet-5-5","max_tokens":1024},"pattern":"react",
  "created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}]}`

const oneAgent = `{"id":"` + agentID + `","team_id":"` + teamID + `","name":"triage","description":"Sorts issues",
  "kind":"custom","model":{"name":"anthropic/claude-sonnet-5-5","max_tokens":1024},"pattern":"react",
  "instructions":"Label it.","max_steps":5,"steer_continuations":0,"parser":false,
  "mcp_servers":[{"id":"` + serverID + `","allow":["search_code"]}],"skills":["` + skillID + `"],
  "created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`

const oneServerPage = `{"items":[{"id":"` + serverID + `","team_id":"` + teamID + `","name":"github","url":"https://mcp.example.com",
  "env":{},"bare":false,"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}]}`

const oneSkillPage = `{"items":[{"id":"` + skillID + `","team_id":"` + teamID + `","name":"pdf","description":"Read PDFs","source":"upload",
  "created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}]}`

const triageYAML = `name: triage
description: Sorts issues
instructions: Label it.
model: {name: anthropic/claude-sonnet-5-5, max_tokens: 1024}
pattern: react
max_steps: 5
`

func routes(extra map[string]clitest.Reply) map[string]clitest.Reply {
	r := map[string]clitest.Reply{
		"GET /teams":                  {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/agents":      {Status: http.StatusOK, Body: oneAgentPage},
		"GET /teams/{id}/mcp-servers": {Status: http.StatusOK, Body: oneServerPage},
		"GET /teams/{id}/skills":      {Status: http.StatusOK, Body: oneSkillPage},
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func file(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sent(t *testing.T, script *clitest.Transcript, method string) map[string]any {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == method {
			var body map[string]any
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("the %s body is not json: %v\n%s", method, err, e.Body)
			}
			return body
		}
	}
	t.Fatalf("no %s was sent: %+v", method, script.All())
	return nil
}

func TestAgentsListRendersEachAgent(t *testing.T) {
	clitest.Routes(t, routes(nil))

	out, err := clitest.Execute(t, commands, "agents", "list", "platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"triage", "anthropic/claude-sonnet-5-5", "react", agentID} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestAgentsGetShowsItsGrants(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/agents/{agentID}": {Status: http.StatusOK, Body: oneAgent},
	}))

	out, err := clitest.Execute(t, commands, "agents", "get", "platform", "triage")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"search_code", serverID, skillID, "5 steps"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// An agent with no budget of its own runs on the deployment's default, and
// the table says so rather than leaving the row out.
func TestAgentsGetShowsTheTokenBudget(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"its own": {strings.Replace(oneAgent, `"max_steps":5,`, `"max_steps":5,"token_budget":250000,`, 1), "250000 tokens"},
		"none":    {oneAgent, "the default"},
	} {
		t.Run(name, func(t *testing.T) {
			clitest.Routes(t, routes(map[string]clitest.Reply{
				"GET /teams/{id}/agents/{agentID}": {Status: http.StatusOK, Body: tc.body},
			}))

			out, err := clitest.Execute(t, commands, "agents", "get", "platform", "triage")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "token budget") || !strings.Contains(out, tc.want) {
				t.Errorf("missing the budget %q:\n%s", tc.want, out)
			}
		})
	}
}

// The file is the body. Grants named on the command line are resolved in
// the team's own lists and added, so nobody writes a uuid by hand.
func TestCreateSendsTheFileAndTheGrantsByID(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/agents": {Status: http.StatusCreated, Body: oneAgent},
	}))

	out, err := clitest.Execute(t, commands, "agents", "create", "platform", "-f", file(t, triageYAML),
		"--mcp-server", "github=search_code, create_issue", "--skill", "pdf")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}

	body := sent(t, script, http.MethodPost)
	if body["name"] != "triage" || body["pattern"] != "react" || body["max_steps"] != float64(5) {
		t.Errorf("the definition did not reach the request: %v", body)
	}
	servers, _ := body["mcp_servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("mcp_servers = %v", body["mcp_servers"])
	}
	grant, _ := servers[0].(map[string]any)
	allow, _ := grant["allow"].([]any)
	if grant["id"] != serverID || len(allow) != 2 || allow[0] != "search_code" || allow[1] != "create_issue" {
		t.Errorf("grant = %v", grant)
	}
	if skills, _ := body["skills"].([]any); len(skills) != 1 || skills[0] != skillID {
		t.Errorf("skills = %v", body["skills"])
	}
}

// A server named without = is granted every tool, which the API says by
// leaving allow out.
func TestAServerNamedAloneGrantsEveryTool(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/agents": {Status: http.StatusCreated, Body: oneAgent},
	}))

	if _, err := clitest.Execute(t, commands, "agents", "create", "platform", "-f", file(t, triageYAML), "--mcp-server", "github"); err != nil {
		t.Fatal(err)
	}
	servers, _ := sent(t, script, http.MethodPost)["mcp_servers"].([]any)
	grant, _ := servers[0].(map[string]any)
	if _, has := grant["allow"]; has {
		t.Errorf("allow was sent for a server granted whole: %v", grant)
	}
}

func TestAnEmptyToolListIsRefused(t *testing.T) {
	script := clitest.Routes(t, routes(nil))

	_, err := clitest.Execute(t, commands, "agents", "create", "platform", "-f", file(t, triageYAML), "--mcp-server", "github=")
	if err == nil || !strings.Contains(err.Error(), "names no tools") {
		t.Errorf("err = %v", err)
	}
	if script.Calls("POST", "/teams/"+teamID+"/agents") != 0 {
		t.Error("the agent was created anyway")
	}
}

func TestADefinitionOnStdinIsRead(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/agents": {Status: http.StatusCreated, Body: oneAgent},
	}))

	if _, err := clitest.ExecuteWithStdin(t, commands, `{"name":"triage","model":{"name":"m","max_tokens":1},"pattern":"react","max_steps":1}`,
		"agents", "create", "platform", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	if sent(t, script, http.MethodPost)["name"] != "triage" {
		t.Error("the JSON on stdin was not the body")
	}
}

func TestADefinitionThatIsNotAnAgentIsRefusedBeforeAnyRequest(t *testing.T) {
	for name, body := range map[string]string{
		"no name":   "pattern: react\n",
		"not yaml":  "name: [unclosed\n",
		"not a map": "- a list\n",
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			if _, err := clitest.Execute(t, commands, "agents", "create", "platform", "-f", file(t, body)); err == nil {
				t.Error("accepted")
			}
			if n := len(script.All()); n != 0 {
				t.Errorf("%d requests", n)
			}
		})
	}
}

func TestCreateWithoutAFileIsAUsageError(t *testing.T) {
	clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "agents", "create", "platform"); err == nil {
		t.Error("create ran with no definition")
	}
}

func TestReplaceSendsAPutToTheResolvedAgent(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"PUT /teams/{id}/agents/{agentID}": {Status: http.StatusOK, Body: oneAgent},
	}))

	out, err := clitest.Execute(t, commands, "agents", "replace", "platform", "triage", "-f", file(t, triageYAML))
	if err != nil || !strings.Contains(out, "replaced") {
		t.Fatalf("replace: %v\n%s", err, out)
	}
	if script.Calls("PUT", "/teams/"+teamID+"/agents/"+agentID) != 1 {
		t.Errorf("requests = %+v", script.All())
	}
}

func TestEveryWriteNamesAgentWriteWhenRefused(t *testing.T) {
	forbidden := clitest.Reply{Status: http.StatusForbidden, Body: `{"error":"forbidden"}`}

	for name, tc := range map[string]struct {
		route string
		args  []string
	}{
		"create":  {"POST /teams/{id}/agents", []string{"create", "platform", "-f"}},
		"replace": {"PUT /teams/{id}/agents/{agentID}", []string{"replace", "platform", "triage", "-f"}},
		"delete":  {"DELETE /teams/{id}/agents/{agentID}", []string{"delete", "platform", "triage"}},
	} {
		t.Run(name, func(t *testing.T) {
			args := tc.args
			if args[len(args)-1] == "-f" {
				args = append(args, file(t, triageYAML))
			}
			clitest.Routes(t, routes(map[string]clitest.Reply{tc.route: forbidden}))
			_, err := clitest.Execute(t, commands, append([]string{"agents"}, args...)...)
			if err == nil || !strings.Contains(err.Error(), "agent:write") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// A grant naming a server the team does not have stops before the agent is
// written, with the server's name in the error.
func TestAGrantOfAServerTheTeamLacksIsReported(t *testing.T) {
	script := clitest.Routes(t, routes(nil))

	_, err := clitest.Execute(t, commands, "agents", "create", "platform", "-f", file(t, triageYAML), "--mcp-server", "linear")
	if err == nil || !strings.Contains(err.Error(), `"linear"`) {
		t.Errorf("err = %v", err)
	}
	if script.Calls("POST", "/teams/"+teamID+"/agents") != 0 {
		t.Error("the agent was created without the grant")
	}
}

func TestAnAgentsEscapeSequencesDoNotReachTheTerminal(t *testing.T) {
	evil := strings.NewReplacer(`"name":"triage"`, `"name":"tri\u001b[2Jage"`,
		`"name":"anthropic/claude-sonnet-5-5"`, `"name":"m\u001b]0;x\u0007"`,
		`"pattern":"react"`, `"pattern":"re\u001b[31mact"`,
		`"allow":["search_code"]`, `"allow":["search\u001b[2J"]`)
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/agents":           {Status: http.StatusOK, Body: evil.Replace(oneAgentPage)},
		"GET /teams/{id}/agents/{agentID}": {Status: http.StatusOK, Body: evil.Replace(oneAgent)},
	}))

	for _, args := range [][]string{{"agents", "list", "platform"}, {"agents", "get", "platform", agentID}} {
		out, err := clitest.Execute(t, commands, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.ContainsAny(out, "\x1b\x07") {
			t.Errorf("%v printed a raw escape:\n%q", args, out)
		}
	}
}
