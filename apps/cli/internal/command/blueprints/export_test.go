package blueprints

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const exportPath = "/teams/" + teamID + "/blueprint"

// Every field a blueprint server and agent carry, so one the YAML drops on
// the way back in shows up as a difference.
const exportBody = `{
  "mcp_servers": [
    {"name": "helpdesk", "url": "https://helpdesk.example/mcp", "auth_secret": "helpdesk#token", "bare": false},
    {"name": "local", "command": ["helpdesk-mcp", "--stdio"], "env": {"KEY": "helpdesk#key"}, "bare": true}
  ],
  "agents": [
    {"name": "triage", "description": "Sorts email.", "kind": "custom", "instructions": "Sort it.",
     "model": {"name": "m", "max_tokens": 4096, "temperature": 0.3}, "pattern": "react", "max_steps": 10,
     "steer_continuations": 2, "token_budget": 50000, "web": true, "delegation": "listed",
     "output_schema": {"name": "verdict", "json": {"type": "object"}, "strict": true},
     "parser": true, "parser_model": {"name": "p", "max_tokens": 512},
     "mcp_servers": [{"name": "helpdesk", "allow": ["create_ticket"]}, {"name": "local"}],
     "skills": ["support-faq"], "delegates": ["writer"]},
    {"name": "writer", "kind": "custom", "instructions": "", "model": {"name": "m", "max_tokens": 1},
     "pattern": "react", "max_steps": 1, "steer_continuations": 0, "parser": false, "web": false,
     "delegation": "none", "mcp_servers": [], "skills": [], "delegates": []}
  ]
}`

// The promise export makes: what it prints, given back to plan, is what the
// brain said, field for field, empty grants included.
func TestAnExportPlansBackAsTheBlueprintTheBrainSaid(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"GET " + exportPath: {Status: http.StatusOK, Body: exportBody},
		"POST " + planPath:  {Status: http.StatusOK, Body: `{"changes": [], "refusals": [], "digest": "d"}`},
	})
	cmds := []clitest.Build{NewExport, NewPlan}

	out, err := clitest.Execute(t, cmds, "export", teamID)
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("export printed JSON by default, want YAML:\n%s", out)
	}
	file := filepath.Join(t.TempDir(), "team.yaml")
	if err := os.WriteFile(file, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := clitest.Execute(t, cmds, "plan", teamID, "-f", file); err != nil {
		t.Fatalf("plan the export: %v", err)
	}

	var said, planned any
	if err := json.Unmarshal([]byte(exportBody), &said); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(script.All()[1].Body), &planned); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(said, planned) {
		t.Errorf("planned:\n%s\nwant what export said:\n%s", script.All()[1].Body, exportBody)
	}
}

func TestAnExportOfSomeAsksForThoseByName(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"GET " + exportPath: {Status: http.StatusOK, Body: `{"agents": []}`},
	})

	if _, err := clitest.Execute(t, []clitest.Build{NewExport}, "export", teamID,
		"--agent", "triage", "--agent", "writer", "--mcp-server", "helpdesk"); err != nil {
		t.Fatalf("export: %v", err)
	}
	if q := script.All()[0].Query; q != "agent=triage&agent=writer&mcp_server=helpdesk" {
		t.Errorf("query = %q, want each name asked for", q)
	}
}

func TestAnExportUnderJSONIsJSON(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{
		"GET " + exportPath: {Status: http.StatusOK, Body: exportBody},
	})

	out, err := clitest.Execute(t, []clitest.Build{NewExport}, "-o", "json", "export", teamID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Errorf("-o json printed something else: %v\n%s", err, out)
	}
}

func TestAnExportOfANameTheTeamLacksSaysSo(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{
		"GET " + exportPath: {Status: http.StatusNotFound, Body: `"nobody" is not an agent in this team that a blueprint describes`},
	})

	_, err := clitest.Execute(t, []clitest.Build{NewExport}, "export", teamID, "--agent", "nobody")
	if err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("err = %v, want the brain's sentence", err)
	}
}
