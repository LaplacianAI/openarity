package approvals

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID     = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	approvalID = "5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7"
	runID      = "0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60"
	ruleID     = "7a1d2c3b-4e5f-4a6b-8c9d-0e1f2a3b4c5d"
	userID     = "9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`

func approvalJSON(status, extra string) string {
	return `{"id":"` + approvalID + `","run_id":"` + runID + `","root_run_id":"` + runID + `","call_id":"c1",
  "tool":"github__delete_repo","class":"destructive","arguments":{"repo":"acme/site","force":true},
  "summary":"repo: acme/site\nforce: true","code":"k7qm","status":"` + status + `","require_other":false,
  "expires_at":"2099-01-01T00:00:00Z","created_at":"2026-10-21T09:00:00Z"` + extra + `}`
}

var pending = approvalJSON("pending", "")

const decidedExtra = `,"decided_by":"` + userID + `","decided_via":"cli","scope":"once","decided_at":"2026-10-21T09:05:00Z"`

func routes(extra map[string]clitest.Reply) map[string]clitest.Reply {
	r := map[string]clitest.Reply{"GET /teams": {Status: http.StatusOK, Body: oneTeam}}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func bodyOf(t *testing.T, script *clitest.Transcript, method string) map[string]any {
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
	t.Fatalf("no %s request was made", method)
	return nil
}

// A verb built but never added to AddCommand passes every test of its body.
func TestTheHelpNamesEveryVerb(t *testing.T) {
	out, err := clitest.Execute(t, commands, "approvals", "--help")
	if err != nil {
		t.Fatalf("approvals --help: %v", err)
	}
	for _, verb := range []string{"list", "show", "approve", "decline", "settings", "rules"} {
		if !strings.Contains(out, verb) {
			t.Errorf("the help does not name %q:\n%s", verb, out)
		}
	}
	out, _ = clitest.Execute(t, commands, "approvals", "rules", "--help")
	for _, verb := range []string{"list", "add", "rm"} {
		if !strings.Contains(out, verb) {
			t.Errorf("the rules help does not name %q:\n%s", verb, out)
		}
	}
}

func TestListShowsTheCodeToolSummaryAndStatusOnOneLine(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approvals": {Status: http.StatusOK, Body: `{"items":[` + pending + `],"next_cursor":"abc"}`},
	}))

	out, err := clitest.Execute(t, commands, "approvals", "list", "platform", "--status", "pending", "--status", "expired", "--run", runID)
	if err != nil {
		t.Fatalf("approvals list: %v", err)
	}
	for _, want := range []string{
		"k7qm", "github__delete_repo", "repo: acme/site force: true", "pending", "expires in", runID,
		"--cursor abc", "--status pending", "--status expired", "--run " + runID,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the list is missing %q:\n%s", want, out)
		}
	}

	var query string
	for _, e := range script.All() {
		if e.Path == "/teams/"+teamID+"/approvals" {
			query = e.Query
		}
	}
	for _, want := range []string{"status=pending", "status=expired", "run=" + runID} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
}

func TestListRefusesAStatusTheBrainDoesNotKnowAndSendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))

	for _, args := range [][]string{
		{"approvals", "list", "platform", "--status", "maybe"},
		{"approvals", "list", "platform", "--run", "not-a-run"},
	} {
		if _, err := clitest.Execute(t, commands, args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("a refused list made %d requests", n)
	}
}

func TestAnEmptyListIsAnEmptyArrayInJSON(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approvals": {Status: http.StatusOK, Body: `{"items":[]}`},
	}))
	out, err := clitest.Execute(t, commands, "approvals", "list", "platform", "-o", "json")
	if err != nil {
		t.Fatalf("approvals list: %v", err)
	}
	if !strings.Contains(out, `"items": []`) && !strings.Contains(out, `"items":[]`) {
		t.Errorf("an empty page did not print an empty array:\n%s", out)
	}
}

func TestAListWithoutReadPermissionNamesIt(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approvals": {Status: http.StatusForbidden, Body: "forbidden"},
	}))
	_, err := clitest.Execute(t, commands, "approvals", "list", "platform")
	if err == nil || !strings.Contains(err.Error(), "approval:read") {
		t.Errorf("err = %v, want it to name approval:read", err)
	}
}

func TestShowPrintsTheArgumentsPrettyAndTheAnswer(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approvals/{approvalID}": {Status: http.StatusOK, Body: approvalJSON("approved", decidedExtra)},
	}))
	out, err := clitest.Execute(t, commands, "approvals", "show", "platform", approvalID)
	if err != nil {
		t.Fatalf("approvals show: %v", err)
	}
	for _, want := range []string{"k7qm", "destructive", "approved", "by", userID, "cli", "Arguments", `"repo": "acme/site"`, `"force": true`} {
		if !strings.Contains(out, want) {
			t.Errorf("show is missing %q:\n%s", want, out)
		}
	}
}

func TestShowRefusesANonIDAndSendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "approvals", "show", "platform", "k7qm"); err == nil {
		t.Fatal("a code was taken for an id")
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("a refused show made %d requests", n)
	}
}

func TestShowAnUnknownRequestIsNotFound(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approvals/{approvalID}": {Status: http.StatusNotFound, Body: "not found"},
	}))
	_, err := clitest.Execute(t, commands, "approvals", "show", "platform", approvalID)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestApproveSendsYesOnceViaTheCLI(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approvals/{approvalID}/decision": {Status: http.StatusOK, Body: approvalJSON("approved", decidedExtra)},
	}))
	out, err := clitest.Execute(t, commands, "approvals", "approve", "platform", approvalID, "--reason", " fine ")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	body := bodyOf(t, script, http.MethodPost)
	if body["approve"] != true || body["via"] != "cli" || body["reason"] != "fine" {
		t.Errorf("body = %v", body)
	}
	if _, has := body["scope"]; has {
		t.Errorf("a plain approve sent a scope: %v", body)
	}
	if !strings.Contains(out, "approved") {
		t.Errorf("no confirmation:\n%s", out)
	}
}

func TestApproveForRunSendsTheRunScope(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approvals/{approvalID}/decision": {Status: http.StatusOK, Body: approvalJSON("approved", decidedExtra)},
	}))
	if _, err := clitest.Execute(t, commands, "approvals", "approve", "platform", approvalID, "--for-run"); err != nil {
		t.Fatalf("approve --for-run: %v", err)
	}
	if body := bodyOf(t, script, http.MethodPost); body["scope"] != "run" {
		t.Errorf("body = %v", body)
	}
}

// Only a yes may be for the run, so the decline command has no such flag.
func TestDeclineSendsNoAndCannotBeForTheRun(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approvals/{approvalID}/decision": {Status: http.StatusOK, Body: approvalJSON("denied", decidedExtra)},
	}))
	if _, err := clitest.Execute(t, commands, "approvals", "decline", "platform", approvalID, "--for-run"); err == nil {
		t.Error("decline took --for-run")
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("a refused decline made %d requests", n)
	}

	out, err := clitest.Execute(t, commands, "approvals", "decline", "platform", approvalID, "--reason", "not on a Friday")
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	body := bodyOf(t, script, http.MethodPost)
	if body["approve"] != false || body["via"] != "cli" || body["reason"] != "not on a Friday" {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(out, "declined") {
		t.Errorf("no confirmation:\n%s", out)
	}
}

func TestADecisionRefusedLocallySendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	for _, args := range [][]string{
		{"approvals", "approve", "platform", "nope"},
		{"approvals", "approve", "platform", approvalID, "--reason", strings.Repeat("x", 1001)},
		{"approvals", "decline", "platform", "nope"},
	} {
		if _, err := clitest.Execute(t, commands, args...); err == nil {
			t.Errorf("%v was accepted", args[:3])
		}
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("refused decisions made %d requests", n)
	}
}

func TestAnAnswerTooLateSaysWhatTheRequestAlreadyIs(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"approved": {approvalJSON("approved", decidedExtra), "already approved by " + userID + " via cli"},
		"denied":   {approvalJSON("denied", decidedExtra), "already denied"},
		"expired":  {approvalJSON("expired", ""), "expired"},
		"cancel":   {approvalJSON("cancelled", ""), "cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			clitest.Routes(t, routes(map[string]clitest.Reply{
				"POST /teams/{id}/approvals/{approvalID}/decision": {Status: http.StatusConflict, Body: tc.body},
			}))
			out, err := clitest.Execute(t, commands, "approvals", "approve", "platform", approvalID)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(out, "approved") && name != "approved" {
				t.Errorf("a conflict printed a confirmation:\n%s", out)
			}
		})
	}
}

func TestAForbiddenAnswerPrintsTheBrainsSentence(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approvals/{approvalID}/decision": {
			Status: http.StatusForbidden, Body: "you may not answer this request\n", ContentType: "text/plain",
		},
	}))
	_, err := clitest.Execute(t, commands, "approvals", "approve", "platform", approvalID)
	if err == nil || err.Error() != "you may not answer this request" {
		t.Errorf("err = %v", err)
	}
}

func TestAnUnknownRequestIsNotFoundOnDecide(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/approvals/{approvalID}/decision": {Status: http.StatusNotFound, Body: "not found"},
	}))
	if _, err := clitest.Execute(t, commands, "approvals", "decline", "platform", approvalID); err == nil {
		t.Error("a 404 was a success")
	}
}

func TestEveryFormatWorksAndNoneLeaksACredential(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approvals":              {Status: http.StatusOK, Body: `{"items":[` + pending + `]}`},
		"GET /teams/{id}/approvals/{approvalID}": {Status: http.StatusOK, Body: pending},
	}))
	for _, format := range []string{"table", "json", "yaml"} {
		for _, args := range [][]string{
			{"approvals", "list", "platform"},
			{"approvals", "show", "platform", approvalID},
		} {
			out, err := clitest.Execute(t, commands, append(args, "-o", format)...)
			if err != nil {
				t.Fatalf("%v -o %s: %v", args, format, err)
			}
			if strings.Contains(out, "a-token") {
				t.Errorf("%v -o %s printed the token:\n%s", args, format, out)
			}
			switch format {
			case "json":
				var v any
				if err := json.Unmarshal([]byte(out), &v); err != nil {
					t.Errorf("%v: not json: %v\n%s", args, err, out)
				}
				if strings.Contains(out, "\u001b") || strings.Contains(out, "Arguments") {
					t.Errorf("json carries table prose:\n%s", out)
				}
			case "yaml":
				var v any
				if err := yaml.Unmarshal([]byte(out), &v); err != nil {
					t.Errorf("%v: not yaml: %v\n%s", args, err, out)
				}
				if !strings.Contains(out, "decided_by: null") && strings.Contains(out, "decided_by: 0000") {
					t.Errorf("an absent person printed as a nil uuid:\n%s", out)
				}
			}
		}
	}
}

// A summary is the model's words: an escape sequence in one must not reach a
// terminal, and a newline must not make one request two rows.
func TestASummaryCannotMoveTheCursor(t *testing.T) {
	hostile := strings.Replace(pending, `repo: acme/site\nforce: true`, `x\u001b[2Jy\nz`, 1)
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/approvals": {Status: http.StatusOK, Body: `{"items":[` + hostile + `]}`},
	}))
	out, err := clitest.Execute(t, commands, "approvals", "list", "platform")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "\u001b") {
		t.Errorf("an escape sequence reached the table: %q", out)
	}
}

func TestWhenSaysHowLongAPendingRequestHasLeft(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		v    view
		want string
	}{
		{view{Status: "pending", ExpiresAt: now.Add(12 * time.Minute)}, "expires in 12m"},
		{view{Status: "pending", ExpiresAt: now.Add(-time.Minute)}, "expiring"},
		{view{Status: "approved", CreatedAt: now.Add(-3 * time.Hour)}, "3h ago"},
		{view{Status: "expired", CreatedAt: now.Add(-72 * time.Hour)}, "3d ago"},
		{view{Status: "denied", CreatedAt: now.Add(-10 * time.Second)}, "under 1m ago"},
	} {
		if got := when(now, tc.v); got != tc.want {
			t.Errorf("when(%s) = %q, want %q", tc.v.Status, got, tc.want)
		}
	}
}
