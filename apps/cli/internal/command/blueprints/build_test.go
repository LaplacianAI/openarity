package blueprints

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const (
	runID     = "11111111-2222-3333-4444-555555555555"
	sessionID = "66666666-7777-8888-9999-000000000000"
	aProposal = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	otherRun  = "99999999-2222-3333-4444-555555555555"
)

var buildCommands = []clitest.Build{NewBuild, NewProposals}

const builderRun = `{"id":"` + runID + `","session_id":"` + sessionID + `","agent":"builder","status":"queued",
  "input":"x","input_tokens":0,"output_tokens":0,"created_at":"2026-11-11T10:00:00Z","depth":0,"hub":false,"builder":true,"delegates":[],"reply":null}`

const finishedBuilder = `{"id":"` + runID + `","session_id":"` + sessionID + `","agent":"builder","status":"succeeded",
  "input":"x","output":"I propose a triage agent.","input_tokens":1,"output_tokens":1,"created_at":"2026-11-11T10:00:00Z","depth":0,"hub":false,"builder":true,"delegates":[],"reply":null}`

const builderStream = "id: 1\nevent: step\ndata: {\"step\":1}\n\n" +
	"id: 2\nevent: finished\ndata: {\"status\":\"succeeded\"}\n\n" +
	"event: end\ndata: {\"status\":\"succeeded\"}\n\n"

const openProposal = `{"id":"` + aProposal + `","session_id":"` + sessionID + `","run_id":"` + runID + `",
  "summary":"A triage agent with the FAQ","state":"open","created_at":"2026-11-11T10:00:01Z",
  "plan":{"changes":[{"kind":"agent","name":"triage","action":"create"}],"refusals":[],"digest":"sha256:now"}}`

func buildRoutes(proposals string) map[string]clitest.Reply {
	return map[string]clitest.Reply{
		"POST /teams/{id}/runs":                         {Status: http.StatusAccepted, Body: builderRun},
		"GET /teams/{id}/runs/{runID}/events":           {Status: http.StatusOK, Body: builderStream},
		"GET /teams/{id}/runs/{runID}":                  {Status: http.StatusOK, Body: finishedBuilder},
		"GET /teams/{id}/proposals":                     {Status: http.StatusOK, Body: proposals},
		"GET /teams/{id}/proposals/{proposalID}":        {Status: http.StatusOK, Body: openProposal},
		"POST /teams/{id}/proposals/{proposalID}/apply": {Status: http.StatusOK, Body: `{"changes":[{"kind":"agent","name":"triage","action":"create","id":"` + otherRun + `"}]}`},
	}
}

func TestBuildAsksTheBuilderAndShowsItsProposal(t *testing.T) {
	script := clitest.Routes(t, buildRoutes(`{"items":[`+openProposal+`]}`))

	out, err := clitest.Execute(t, buildCommands, "build", teamID, "An agent that triages email.")
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if body := script.All()[0].Body; !strings.Contains(body, `"builder":true`) || strings.Contains(body, `"agent"`) {
		t.Errorf("start body = %s, want a builder run naming no agent", body)
	}
	for _, want := range []string{
		"I propose a triage agent.", "A triage agent with the FAQ", "triage", "create",
		"oa proposals apply " + teamID + " " + aProposal, "--session " + sessionID,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := script.Calls("POST", "/teams/"+teamID+"/proposals/"+aProposal+"/apply"); n != 0 {
		t.Errorf("build applied the proposal: %d calls", n)
	}
	if q := script.All()[3].Query; q != "session="+sessionID {
		t.Errorf("proposals asked with %q, want this session's", q)
	}
}

// A run that asked a question proposed nothing; an older open proposal in the
// session is not this run's to show.
func TestBuildWithNoProposalSaysHowToAnswer(t *testing.T) {
	older := strings.Replace(openProposal, `"run_id":"`+runID, `"run_id":"`+otherRun, 1)
	clitest.Routes(t, buildRoutes(`{"items":[`+older+`]}`))

	out, err := clitest.Execute(t, buildCommands, "build", teamID, "An agent.")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(out, "No proposal yet") || strings.Contains(out, "oa proposals apply") {
		t.Errorf("output:\n%s\nwant no proposal and how to answer", out)
	}
}

func TestBuildCarriesOnASession(t *testing.T) {
	script := clitest.Routes(t, buildRoutes(`{"items":[]}`))
	if _, err := clitest.Execute(t, buildCommands, "build", teamID, "Sonnet.", "--session", sessionID); err != nil {
		t.Fatal(err)
	}
	if body := script.All()[0].Body; !strings.Contains(body, `"session_id":"`+sessionID+`"`) {
		t.Errorf("start body = %s, want the session", body)
	}
	if _, err := clitest.Execute(t, buildCommands, "build", teamID, "x", "--session", "nope"); err == nil {
		t.Error("a session that is not an id was taken")
	}
}

func TestApplyingAProposalShowsItsPlanAndSendsItsDigest(t *testing.T) {
	script := clitest.Routes(t, buildRoutes(`{"items":[]}`))

	out, err := clitest.Execute(t, buildCommands, "proposals", "apply", teamID, aProposal, "--yes")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	applyPath := "/teams/" + teamID + "/proposals/" + aProposal + "/apply"
	var body string
	for _, e := range script.All() {
		if e.Path == applyPath {
			body = e.Body
		}
	}
	if body != `{"plan_digest":"sha256:now"}` {
		t.Errorf("apply body = %q, want the digest of the plan shown", body)
	}
	if !strings.Contains(out, "applied") || !strings.Contains(out, "1 created") {
		t.Errorf("output:\n%s\nwant what was applied", out)
	}
}

func TestAProposalNoLongerOpenIsNotApplied(t *testing.T) {
	routes := buildRoutes(`{"items":[]}`)
	routes["GET /teams/{id}/proposals/{proposalID}"] = clitest.Reply{
		Status: http.StatusOK,
		Body:   strings.Replace(openProposal, `"state":"open"`, `"state":"superseded"`, 1),
	}
	script := clitest.Routes(t, routes)

	_, err := clitest.Execute(t, buildCommands, "proposals", "apply", teamID, aProposal, "--yes")
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Errorf("err = %v, want it to say superseded", err)
	}
	if n := script.Calls("POST", "/teams/"+teamID+"/proposals/"+aProposal+"/apply"); n != 0 {
		t.Errorf("%d applies sent", n)
	}
}

func TestARefusedProposalIsShownAndNotApplied(t *testing.T) {
	routes := buildRoutes(`{"items":[]}`)
	routes["GET /teams/{id}/proposals/{proposalID}"] = clitest.Reply{Status: http.StatusOK, Body: strings.Replace(openProposal,
		`"refusals":[]`, `"refusals":[{"kind":"agent","name":"triage","message":"agent:write is needed"}]`, 1)}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, buildCommands, "proposals", "apply", teamID, aProposal, "--yes")
	if err == nil || !strings.Contains(out, "agent:write is needed") {
		t.Errorf("err = %v, out:\n%s\nwant the refusal shown and an error", err, out)
	}
	if n := script.Calls("POST", "/teams/"+teamID+"/proposals/"+aProposal+"/apply"); n != 0 {
		t.Errorf("%d applies sent", n)
	}
}

func TestAnApplyThatLostARaceSaysSo(t *testing.T) {
	routes := buildRoutes(`{"items":[]}`)
	routes["POST /teams/{id}/proposals/{proposalID}/apply"] = clitest.Reply{
		Status: http.StatusConflict,
		Body:   "the proposal was applied or superseded meanwhile: nothing was applied", ContentType: "text/plain",
	}
	clitest.Routes(t, routes)

	_, err := clitest.Execute(t, buildCommands, "proposals", "apply", teamID, aProposal, "--yes")
	if err == nil || !strings.Contains(err.Error(), "meanwhile") {
		t.Errorf("err = %v, want the brain's sentence", err)
	}
}

func TestApplyAsksBeforeItWritesUnlessToldNot(t *testing.T) {
	script := clitest.Routes(t, buildRoutes(`{"items":[]}`))
	if _, err := clitest.Execute(t, buildCommands, "-o", "json", "proposals", "apply", teamID, aProposal); err == nil {
		t.Error("apply under json without --yes went ahead")
	}
	if len(script.All()) != 0 {
		t.Errorf("%d requests sent", len(script.All()))
	}
}

func TestProposalsAreListedAndReadByID(t *testing.T) {
	clitest.Routes(t, buildRoutes(`{"items":[`+openProposal+`]}`))
	out, err := clitest.Execute(t, buildCommands, "proposals", "list", teamID)
	if err != nil || !strings.Contains(out, "A triage agent with the FAQ") || !strings.Contains(out, aProposal) {
		t.Errorf("list: %v\n%s", err, out)
	}
	out, err = clitest.Execute(t, buildCommands, "proposals", "get", teamID, aProposal)
	if err != nil || !strings.Contains(out, "open") || !strings.Contains(out, "create") {
		t.Errorf("get: %v\n%s", err, out)
	}
	if _, err := clitest.Execute(t, buildCommands, "proposals", "get", teamID, "nope"); err == nil {
		t.Error("a proposal that is not an id was taken")
	}
}
