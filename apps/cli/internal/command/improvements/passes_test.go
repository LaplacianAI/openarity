package improvements

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const passesRoute = "GET /teams/{id}/improvements/passes"

const (
	passA = "7e1f2a3b-4c5d-4e6f-8a9b-0c1d2e3f4a5b"
	passB = "8f2a3b4c-5d6e-4f7a-9b0c-1d2e3f4a5b6c"
	passC = "9a3b4c5d-6e7f-4a8b-8c1d-2e3f4a5b6c7d"
)

func pass(id, trigger, status, extra string) string {
	return `{"id":"` + id + `","agent":{"id":"` + agentID + `","name":"support-bot"},
  "trigger":"` + trigger + `","signatures":["instructions::skipped_step","tool:crm::wrong_arg"],"pruned":[],
  "status":"` + status + `","cut_short":false,"report":"found two","report_withheld":false,"proposed":2,"screened_out":1,"model":"gpt-5",
  "input_tokens":1200,"output_tokens":340,"error":null,
  "started_at":"2026-11-21T09:00:00Z","finished_at":null` + extra + `}`
}

func TestPassesListsWhyItRanStatusCountsTokensAndStart(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		passesRoute: {Status: http.StatusOK, Body: `{"items":[` +
			pass(passA, "pattern", "done", `,"cut_short":true`) + `,` +
			pass(passB, "daily", "failed", `,"error":"it stopped answering and was taken as lost"`) + `,` +
			pass(passC, "daily", "running", `,"input_tokens":0,"output_tokens":0`) + `],"next_cursor":"abc"}`},
	}))

	out, err := clitest.Execute(t, commands, "improvements", "passes", "platform", "--agent", "support-bot", "--limit", "5")
	if err != nil {
		t.Fatalf("passes: %v", err)
	}
	contains(t, out, "support-bot", "pattern instructions::skipped_step, tool:crm::wrong_arg", "daily",
		"done", "failed", "2 proposed / 1 screened out, cut short", "1200 in / 340 out", "2026-11-2", passA,
		"taken as lost", "running", "--cursor abc", "--agent support-bot")
	if strings.Contains(out, "0 in / 0 out") {
		t.Errorf("a running pass printed its tokens as nothing spent:\n%s", out)
	}

	var query string
	for _, e := range script.All() {
		if e.Path == "/teams/"+teamID+"/improvements/passes" {
			query = e.Query
		}
	}
	contains(t, query, "agent="+agentID, "limit=5")
}

func TestPassesDoesNotLookUpAnAgentGivenAsAnID(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		passesRoute: {Status: http.StatusOK, Body: `{"items":[]}`},
	}))
	if _, err := clitest.Execute(t, commands, "improvements", "passes", "platform", "--agent", agentID); err != nil {
		t.Fatalf("passes: %v", err)
	}
	if n := script.Calls("GET", "/teams/"+teamID+"/agents"); n != 0 {
		t.Errorf("an id was looked up: %d agent list calls", n)
	}
}

func TestPassesWithAnAgentNobodyHasNamesItAndAsksForNoPasses(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	_, err := clitest.Execute(t, commands, "improvements", "passes", "platform", "--agent", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %v, want it to name ghost", err)
	}
	if n := script.Calls("GET", "/teams/"+teamID+"/improvements/passes"); n != 0 {
		t.Errorf("passes were requested anyway: %d", n)
	}
}

func TestPassesInJSONCarriesTheRawItemsAndAnEmptyListIsAnEmptyArray(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		passesRoute: {Status: http.StatusOK, Body: `{"items":[` + pass(passA, "daily", "running", "") + `]}`},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "passes", "platform", "-o", "json")
	if err != nil {
		t.Fatalf("passes: %v", err)
	}
	contains(t, out, `"report"`, "found two", `"signatures"`, `"input_tokens"`, passA)
	if strings.Contains(out, "\x1b") {
		t.Errorf("colour in json:\n%q", out)
	}

	clitest.Routes(t, routes(map[string]clitest.Reply{
		passesRoute: {Status: http.StatusOK, Body: `{"items":[]}`},
	}))
	out, err = clitest.Execute(t, commands, "improvements", "passes", "platform", "-o", "json")
	if err != nil {
		t.Fatalf("passes: %v", err)
	}
	if !strings.Contains(out, `"items": []`) && !strings.Contains(out, `"items":[]`) {
		t.Errorf("an empty page did not print an empty array:\n%s", out)
	}
}

func TestPassesSaysWhenThereAreNone(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		passesRoute: {Status: http.StatusOK, Body: `{"items":[]}`},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "passes", "platform")
	if err != nil {
		t.Fatalf("passes: %v", err)
	}
	contains(t, out, "no improver passes")
}

func TestPassesForbiddenNamesThePermissionAndDoesNotSayLogIn(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		passesRoute: {Status: http.StatusForbidden, Body: "forbidden"},
	}))
	_, err := clitest.Execute(t, commands, "improvements", "passes", "platform")
	if err == nil || !strings.Contains(err.Error(), "improvement:approve") {
		t.Fatalf("err = %v, want it to name improvement:approve", err)
	}
	if strings.Contains(err.Error(), "log in") {
		t.Errorf("a 403 told the caller to log in: %v", err)
	}
}

func TestShowPrintsThePassThatWroteTheProposal(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("waiting", `,"pass_id":"`+passA+`"`)},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	contains(t, out, "pass", passA)
}

func TestShowOmitsThePassRowWhenThereIsNone(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("waiting", `,"pass_id":null`)},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(out, "\npass ") {
		t.Errorf("a pass row with no pass:\n%s", out)
	}
	jsonOut, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID, "-o", "json")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	contains(t, jsonOut, `"pass_id": null`)
}
