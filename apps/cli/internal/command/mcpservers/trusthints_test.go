package mcpservers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const trustPath = "PUT /teams/{id}/mcp-servers/{serverID}/trust-hints"

func trusted(on bool) string {
	v := "false"
	if on {
		v = "true"
	}
	return strings.Replace(github, `"bare":false`, `"bare":false,"trust_hints":`+v, 1)
}

func TestTrustHintsOnSendsTrustTrueAndWarns(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		trustPath: {Status: http.StatusOK, Body: trusted(true)},
	}))
	out, err := clitest.Execute(t, commands, "mcp-servers", "trust-hints", "platform", "github", "on")
	if err != nil {
		t.Fatalf("trust-hints on: %v", err)
	}
	if body := sent(t, script, http.MethodPut); body["trust"] != true {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(out, "read-only hints") || !strings.Contains(out, "past approvals") {
		t.Errorf("turning trust on did not warn:\n%s", out)
	}
	if script.Calls(http.MethodPut, "/teams/"+teamID+"/mcp-servers/"+serverID+"/trust-hints") != 1 {
		t.Errorf("the server was not resolved by name: %v", script.All())
	}
}

func TestTrustHintsOffSendsFalseAndDoesNotWarn(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		trustPath: {Status: http.StatusOK, Body: trusted(false)},
	}))
	out, err := clitest.Execute(t, commands, "mcp-servers", "trust-hints", "platform", "github", "off")
	if err != nil {
		t.Fatalf("trust-hints off: %v", err)
	}
	if body := sent(t, script, http.MethodPut); body["trust"] != false {
		t.Errorf("body = %v", body)
	}
	if strings.Contains(out, "read-only hints") {
		t.Errorf("turning trust off warned:\n%s", out)
	}
}

func TestTrustHintsRefusesAnythingButOnOrOffAndSendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "mcp-servers", "trust-hints", "platform", "github", "yes"); err == nil {
		t.Fatal("yes was accepted")
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("a refused command made %d requests", n)
	}
}

func TestTrustHintsRefusedNamesThePermissionAndANameNobodyHasIsReported(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{trustPath: {Status: http.StatusForbidden, Body: "no"}}))
	_, err := clitest.Execute(t, commands, "mcp-servers", "trust-hints", "platform", "github", "on")
	if err == nil || !strings.Contains(err.Error(), "team:settings") {
		t.Errorf("err = %v", err)
	}
	_, err = clitest.Execute(t, commands, "mcp-servers", "trust-hints", "platform", "nobody", "on")
	if err == nil || !strings.Contains(err.Error(), `"nobody"`) {
		t.Errorf("err = %v", err)
	}
}

func TestAServerTrustingHintsSaysSoWhenShown(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/mcp-servers/{serverID}": {Status: http.StatusOK, Body: trusted(true)},
	}))
	out, err := clitest.Execute(t, commands, "mcp-servers", "get", "platform", "github")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(out, "trust hints") {
		t.Errorf("get does not show trust hints:\n%s", out)
	}
}
