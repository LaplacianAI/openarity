package improvements

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID  = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	agentID = "3a9d1c5e-2b7f-4e8a-9c6d-1f0e2a3b4c5d"
	propID  = "5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7"
	runA    = "0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60"
	runB    = "1c8d4f63-9b52-4a7e-8f3c-6d2b8e4a9f71"
	userID  = "9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d"
)

const (
	oneTeam  = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`
	oneAgent = `{"items":[{"id":"` + agentID + `","name":"support-bot"}]}`
)

func proposal(status, extra string) string {
	return `{"id":"` + propID + `","kind":"instructions","status":"` + status + `",
  "agent":{"id":"` + agentID + `","name":"support-bot"},
  "why":"it kept answering in French\nto English speakers","hypothesis":"say the language back",
  "scope":"invariant","marks":["changes_behaviour","raises_cost"],
  "reaches":[{"id":"` + agentID + `","name":"support-bot"},{"id":"` + runB + `","name":"triage"}],
  "changes":[
    {"field":"instructions","type":"text","before":"Be brief.\nAnswer in French.\nStop.","after":"Be brief.\nAnswer in the user's language.\nStop."},
    {"field":"skills","type":"list","before":["old-skill","kept"],"after":["kept","new-skill"]},
    {"field":"max_steps","type":"value","before":10,"after":20},
    {"field":"files/scripts/fetch.py","type":"text","before":null,"after":"print(1)"}],
  "runs":[{"id":"` + runA + `","readable":true},{"id":"` + runB + `","readable":false}],
  "outsider":true,"created_at":"2026-10-21T09:00:00Z"` + extra + `}`
}

func routes(extra map[string]clitest.Reply) map[string]clitest.Reply {
	r := map[string]clitest.Reply{
		"GET /teams":             {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/agents": {Status: http.StatusOK, Body: oneAgent},
	}
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

func contains(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

// A verb built but never added to AddCommand passes every test of its body.
func TestTheHelpNamesEveryVerb(t *testing.T) {
	out, err := clitest.Execute(t, commands, "improvements", "--help")
	if err != nil {
		t.Fatalf("improvements --help: %v", err)
	}
	contains(t, out, "list", "show", "passes", "approve", "reject", "revert", "clear")
}

func TestListShowsAgentKindStatusScopeMarksAgeAndWhyOnOneLine(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements": {Status: http.StatusOK, Body: `{"items":[` + proposal("waiting", "") + `],"next_cursor":"abc"}`},
	}))

	out, err := clitest.Execute(t, commands, "improvements", "list", "platform",
		"--status", "waiting", "--status", "Applied", "--agent", "support-bot", "--query", " French ")
	if err != nil {
		t.Fatalf("improvements list: %v", err)
	}
	contains(t, out, "support-bot", "instructions", "waiting", "invariant", "behaviour,cost",
		"it kept answering in French to English speakers", propID,
		"--cursor abc", "--status waiting", "--status Applied", "--agent support-bot", "--query French")

	var query string
	for _, e := range script.All() {
		if e.Path == "/teams/"+teamID+"/improvements" {
			query = e.Query
		}
	}
	contains(t, query, "status=waiting", "status=applied", "agent="+agentID, "q=French")
}

func TestListDoesNotLookUpAnAgentGivenAsAnID(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements": {Status: http.StatusOK, Body: `{"items":[]}`},
	}))
	if _, err := clitest.Execute(t, commands, "improvements", "list", "platform", "--agent", agentID); err != nil {
		t.Fatalf("list: %v", err)
	}
	if n := script.Calls("GET", "/teams/"+teamID+"/agents"); n != 0 {
		t.Errorf("an id was looked up: %d agent list calls", n)
	}
}

func TestListWithAnAgentNobodyHasNamesIt(t *testing.T) {
	clitest.Routes(t, routes(nil))
	_, err := clitest.Execute(t, commands, "improvements", "list", "platform", "--agent", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %v, want it to name ghost", err)
	}
}

func TestListRefusesAStatusTheBrainDoesNotKnowAndSendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "improvements", "list", "platform", "--status", "maybe"); err == nil {
		t.Error("a bad status was accepted")
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("a refused list made %d requests", n)
	}
}

func TestAnEmptyListIsAnEmptyArrayInJSON(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements": {Status: http.StatusOK, Body: `{"items":[]}`},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "list", "platform", "-o", "json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, `"items": []`) && !strings.Contains(out, `"items":[]`) {
		t.Errorf("an empty page did not print an empty array:\n%s", out)
	}
}

func TestAListOfAHiddenStatusIsForbiddenAndNamesThePermission(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements": {Status: http.StatusForbidden, Body: "forbidden"},
	}))
	_, err := clitest.Execute(t, commands, "improvements", "list", "platform", "--status", "waiting")
	if err == nil || !strings.Contains(err.Error(), "improvement:approve") {
		t.Errorf("err = %v, want it to name improvement:approve", err)
	}
}

func TestShowPrintsTheWhyMarksReachesRunsAndEveryKindOfChange(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("waiting", "")},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	contains(t, out,
		"support-bot", "invariant (a general weakness of agents)", "behaviour,cost",
		"support-bot, triage", runA, runB+" (not yours to open)",
		"someone outside the team",
		"Why", "it kept answering in French", "to English speakers", "Hypothesis", "say the language back",
		"instructions", "  Be brief.", "- Answer in French.", "+ Answer in the user's language.", "  Stop.",
		"- old-skill", "+ new-skill",
		"10 → 20",
		"scripts/fetch.py", "+ print(1)")
	if strings.Contains(out, "files/scripts") {
		t.Errorf("a file's heading carries its prefix:\n%s", out)
	}
	if strings.Contains(out, "- kept") || strings.Contains(out, "+ kept") {
		t.Errorf("an unchanged name shows as changed:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("colour reached a non-terminal:\n%q", out)
	}
}

func TestShowQuietlyOmitsTheOutsiderWarningWhenThereIsNone(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {
			Status: http.StatusOK,
			Body: strings.Replace(proposal("applied", `,"decided_by":"`+userID+`","decided_at":"2026-10-21T09:05:00Z"`),
				`"outsider":true`, `"outsider":false`, 1),
		},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(out, "outside the team") {
		t.Errorf("a warning with no outsider:\n%s", out)
	}
	contains(t, out, "decided", userID)
}

func TestShowMakesAModelsEscapeSequencesInADiffHarmless(t *testing.T) {
	body := strings.Replace(proposal("waiting", ""), `"after":"Be brief.\nAnswer in the user's language.\nStop."`,
		`"after":"Be brief.\n\u001b[2Jwiped\nStop."`, 1)
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: body},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	contains(t, out, "+ wiped")
}

func TestShowRefusesANonIDAndSendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "improvements", "show", "platform", "nope"); err == nil {
		t.Fatal("a word was taken for an id")
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("a refused show made %d requests", n)
	}
}

func TestShowAHiddenOrUnknownProposalIsNotFound(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusNotFound, Body: "not found"},
	}))
	_, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestEachDecisionPostsItsActionAndSaysWhatHappened(t *testing.T) {
	for verb, want := range map[string]string{"approve": "approved", "reject": "rejected", "revert": "reverted", "clear": "cleared"} {
		t.Run(verb, func(t *testing.T) {
			script := clitest.Routes(t, routes(map[string]clitest.Reply{
				"POST /teams/{id}/improvements/{proposalID}/decision": {Status: http.StatusOK, Body: proposal(want, "")},
			}))
			out, err := clitest.Execute(t, commands, "improvements", verb, "platform", propID)
			if err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			if body := bodyOf(t, script, http.MethodPost); body["action"] != verb || len(body) != 1 {
				t.Errorf("body = %v, want only action %q", body, verb)
			}
			contains(t, out, want, "support-bot")
			if n := script.Calls("POST", "/teams/"+teamID+"/improvements/"+propID+"/decision"); n != 1 {
				t.Errorf("%d decisions posted", n)
			}
		})
	}
}

func TestADecisionWithANonIDSendsNothing(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	if _, err := clitest.Execute(t, commands, "improvements", "approve", "platform", "nope"); err == nil {
		t.Fatal("a word was taken for an id")
	}
	if n := len(script.All()); n != 0 {
		t.Errorf("a refused decision made %d requests", n)
	}
}

func TestAConflictPrintsTheBrainsReasonAsTheError(t *testing.T) {
	const reason = "support-bot changed since this was proposed; reject it and let the next one be proposed from the current agent"
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/improvements/{proposalID}/decision": {Status: http.StatusConflict, Body: reason + "\n", ContentType: "text/plain"},
	}))
	_, err := clitest.Execute(t, commands, "improvements", "approve", "platform", propID)
	if err == nil || err.Error() != reason {
		t.Errorf("err = %v, want exactly the brain's reason", err)
	}
}

func TestARefusedDecisionNamesWhatDecidingNeeds(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/improvements/{proposalID}/decision": {Status: http.StatusForbidden, Body: "forbidden"},
	}))
	_, err := clitest.Execute(t, commands, "improvements", "revert", "platform", propID)
	if err == nil {
		t.Fatal("a 403 succeeded")
	}
	for _, want := range []string{"improvement:approve", "agent:write", "skill:write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "log in") {
		t.Errorf("a 403 told the caller to log in: %v", err)
	}
}

func TestADecisionOnAnUnknownProposalIsNotFound(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/improvements/{proposalID}/decision": {Status: http.StatusNotFound, Body: "not found"},
	}))
	_, err := clitest.Execute(t, commands, "improvements", "reject", "platform", propID)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestEveryFormatPrintsAListAndAProposal(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements":              {Status: http.StatusOK, Body: `{"items":[` + proposal("waiting", "") + `]}`},
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("waiting", "")},
	}))
	for _, format := range []string{"table", "json", "yaml"} {
		for _, args := range [][]string{{"list", "platform"}, {"show", "platform", propID}} {
			out, err := clitest.Execute(t, commands, append([]string{"improvements"}, append(args, "-o", format)...)...)
			if err != nil {
				t.Fatalf("%v -o %s: %v", args, format, err)
			}
			switch format {
			case "json":
				var v any
				if err := json.Unmarshal([]byte(out), &v); err != nil {
					t.Errorf("%v -o json is not json (a note leaked in?): %v\n%s", args, err, out)
				}
			case "yaml":
				var v any
				if err := yaml.Unmarshal([]byte(out), &v); err != nil {
					t.Errorf("%v -o yaml is not yaml: %v\n%s", args, err, out)
				}
			}
			if strings.Contains(out, "\x1b") {
				t.Errorf("%v -o %s carries an escape sequence", args, format)
			}
		}
	}
}

func TestShowInJSONCarriesTheChangesAndMarksVerbatim(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("waiting", "")},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID, "-o", "json")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var got view
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.Marks[0] != "changes_behaviour" || len(got.Changes) != 4 || got.Changes[3].Before != nil || got.Runs[1].Readable {
		t.Errorf("view = %+v", got)
	}
}

func TestLineDiffKeepsOrderAndFindsTheUnchangedLines(t *testing.T) {
	got := lineDiff([]string{"a", "b", "c"}, []string{"a", "x", "c", "d"})
	want := []diffLine{{opSame, "a"}, {opDel, "b"}, {opAdd, "x"}, {opSame, "c"}, {opAdd, "d"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %v, want %v", i, got[i], want[i])
		}
	}
	if d := lineDiff(nil, splitLines("one\ntwo\n")); len(d) != 2 || d[0].op != opAdd {
		t.Errorf("an added file = %v", d)
	}
}

// A long file edited in one place diffs only the middle, and a middle too big
// for the table is shown as removed then added rather than built.
func TestLineDiffOfALongFileStaysCheap(t *testing.T) {
	long := make([]string, 4000)
	for i := range long {
		long[i] = fmt.Sprintf("line %d", i)
	}
	edited := slices.Clone(long)
	edited[2000] = "changed"
	d := lineDiff(long, edited)
	changes := 0
	for _, l := range d {
		if l.op != opSame {
			changes++
		}
	}
	if len(d) != 4001 || changes != 2 {
		t.Errorf("one edited line in 4000 gave %d lines, %d changed", len(d), changes)
	}

	a, b := make([]string, 1500), make([]string, 1500)
	for i := range a {
		a[i], b[i] = fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
	}
	if d := lineDiff(a, b); len(d) != 3000 || d[0].op != opDel || d[1500].op != opAdd {
		t.Errorf("a wholly rewritten long file = %d lines", len(d))
	}
}

// An added file needs no table at all, however long it is.
func TestLineDiffOfAnAddedFileBuildsNoTable(t *testing.T) {
	big := make([]string, 200_000)
	for i := range big {
		big[i] = fmt.Sprintf("l%d", i)
	}
	if d := lineDiff(nil, big); len(d) != len(big) || d[0].op != opAdd {
		t.Errorf("an added file = %d lines", len(d))
	}
}
