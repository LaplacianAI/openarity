package approvals

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const oneRule = `{"id":"` + ruleID + `","target_kind":"tool","target":"github__delete_repo","mode":"deny",
  "require_other":false,"created_at":"2026-10-21T09:00:00Z"}`

func TestRulesListShowsTargetModeAndID(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approval-rules": {Status: http.StatusOK, Body: `{"items":[` + oneRule + `]}`},
	}))
	out, err := clitest.Execute(t, commands, "approvals", "rules", "list", "platform")
	if err != nil {
		t.Fatalf("rules list: %v", err)
	}
	for _, want := range []string{"tool", "github__delete_repo", "deny", ruleID} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestRulesAddForAToolAndForAServer(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approval-rules": {Status: http.StatusCreated, Body: oneRule},
	}))
	out, err := clitest.Execute(t, commands, "approvals", "rules", "add", "platform", "--tool", "github__delete_repo", "--mode", "deny")
	if err != nil {
		t.Fatalf("rules add: %v", err)
	}
	body := bodyOf(t, script, http.MethodPost)
	if body["target_kind"] != "tool" || body["target"] != "github__delete_repo" || body["mode"] != "deny" || body["require_other"] != false {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(out, "added") {
		t.Errorf("no confirmation:\n%s", out)
	}

	script = clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approval-rules": {Status: http.StatusCreated, Body: oneRule},
	}))
	if _, err := clitest.Execute(t, commands, "approvals", "rules", "add", "platform", "--server", "github", "--mode", "ask", "--require-other"); err != nil {
		t.Fatalf("rules add --server: %v", err)
	}
	body = bodyOf(t, script, http.MethodPost)
	if body["target_kind"] != "server" || body["target"] != "github" || body["mode"] != "ask" || body["require_other"] != true {
		t.Errorf("body = %v", body)
	}
}

func TestRulesAddRefusesBadFlagsAndSendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	for _, args := range [][]string{
		{"--mode", "deny"},
		{"--tool", "a", "--server", "b", "--mode", "deny"},
		{"--tool", "a"},
		{"--tool", "a", "--mode", "maybe"},
		{"--tool", "  ", "--mode", "deny"},
	} {
		if _, err := clitest.Execute(t, commands, append([]string{"approvals", "rules", "add", "platform"}, args...)...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("refused rules made %d requests", n)
	}
}

func TestRulesAddForATargetThatHasOneSaysSo(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approval-rules": {Status: http.StatusConflict, Body: "a rule already covers it"},
	}))
	_, err := clitest.Execute(t, commands, "approvals", "rules", "add", "platform", "--tool", "x__y", "--mode", "ask")
	if err == nil || !strings.Contains(err.Error(), "already has a rule") {
		t.Errorf("err = %v", err)
	}
}

func TestRulesWritesNameThePermissionWhenRefused(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approval-rules":            {Status: http.StatusForbidden, Body: "no"},
		"DELETE /teams/{id}/approval-rules/{ruleID}": {Status: http.StatusForbidden, Body: "no"},
	}))
	for _, args := range [][]string{
		{"add", "platform", "--tool", "x__y", "--mode", "ask"},
		{"rm", "platform", ruleID},
	} {
		_, err := clitest.Execute(t, commands, append([]string{"approvals", "rules"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "team:settings") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestRulesRemoveDeletesAndConfirms(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"DELETE /teams/{id}/approval-rules/{ruleID}": {Status: http.StatusNoContent},
	}))
	out, err := clitest.Execute(t, commands, "approvals", "rules", "rm", "platform", ruleID)
	if err != nil {
		t.Fatalf("rules rm: %v", err)
	}
	if script.Calls(http.MethodDelete, "/teams/"+teamID+"/approval-rules/"+ruleID) != 1 || !strings.Contains(out, "removed") {
		t.Errorf("calls = %v, out:\n%s", script.All(), out)
	}
}

func TestRulesRemoveNeedsARuleIDAndAReal204(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"DELETE /teams/{id}/approval-rules/{ruleID}": {Status: http.StatusOK, Body: "{}"},
	}))
	if _, err := clitest.Execute(t, commands, "approvals", "rules", "rm", "platform", "github"); err == nil {
		t.Error("a name was taken for a rule id")
	}
	if n := script.Calls(http.MethodDelete, "/teams/"+teamID+"/approval-rules/github"); n != 0 {
		t.Errorf("a refused rm sent %d requests", n)
	}
	out, err := clitest.Execute(t, commands, "approvals", "rules", "rm", "platform", ruleID)
	if err == nil || strings.Contains(out, "removed") {
		t.Errorf("a 200 on a 204 route printed a confirmation: %v\n%s", err, out)
	}
}
