package runs

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID    = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	sessionID = "c7e2a940-51f6-4c3b-8d7a-0e9b3f1a6c52"
	runID     = "0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60"
	otherRun  = "9d4b2e60-3a17-4f8c-b2e5-7c1d0a4f8b93"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`

const (
	runsPath   = "/teams/" + teamID + "/runs"
	runPath    = runsPath + "/" + runID
	eventsPath = runPath + "/events"
	cancelPath = runPath + "/cancel"
	listPath   = "/teams/" + teamID + "/sessions/" + sessionID + "/runs"
)

func runJSON(id, status, extra string) string {
	return `{"id":"` + id + `","session_id":"` + sessionID + `","agent":"triage","status":"` + status + `",
	  "input":"Sort the issues opened today.","input_tokens":120,"output_tokens":45,
	  "created_at":"2026-10-21T09:00:00Z"` + extra + `}`
}

var (
	queuedRun   = runJSON(runID, "queued", "")
	finishedRun = runJSON(runID, "succeeded", `,"output":"Labelled 4 issues.",
	  "started_at":"2026-10-21T09:00:01Z","finished_at":"2026-10-21T09:00:09Z"`)
	failedRun = runJSON(runID, "failed", `,"error":"the run has spent its token budget",
	  "started_at":"2026-10-21T09:00:01Z","finished_at":"2026-10-21T09:00:09Z"`)
)

func teamRoute() map[string]clitest.Reply {
	return map[string]clitest.Reply{"GET /teams": {Status: http.StatusOK, Body: oneTeam}}
}

func with(route string, reply clitest.Reply) map[string]clitest.Reply {
	routes := teamRoute()
	routes[route] = reply
	return routes
}

func bodyOf(t *testing.T, script *clitest.Transcript, method, path string) map[string]any {
	t.Helper()

	for _, e := range script.All() {
		if e.Method == method && e.Path == path {
			var body map[string]any
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("the body of %s %s is not json: %v\n%s", method, path, err, e.Body)
			}
			return body
		}
	}
	t.Fatalf("nothing was sent to %s %s: %+v", method, path, script.All())
	return nil
}

func queryFor(t *testing.T, script *clitest.Transcript, path string) string {
	t.Helper()

	for _, e := range script.All() {
		if e.Path == path {
			return e.Query
		}
	}
	t.Fatalf("nothing was sent to %s", path)
	return ""
}

// A subcommand missed in AddCommand gets no warning from any linter — the
// constructor is called, so nothing is unused. --help is what notices.
func TestEveryVerbIsRegistered(t *testing.T) {
	clitest.Routes(t, teamRoute())

	out, err := clitest.Execute(t, commands, "runs", "--help")
	if err != nil {
		t.Fatalf("runs --help: %v\n%s", err, out)
	}
	for _, verb := range []string{"start", "list", "get", "events", "cancel"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not on `oa runs`:\n%s", verb, out)
		}
	}
}

// --- starting one ---

// The agent goes by name in the body and the brain resolves it while queueing,
// so starting a run needs no agent lookup — and must not make one, or it
// would need the agent list to be readable as well.
func TestStartSendsTheAgentAndTheInput(t *testing.T) {
	script := clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun}))

	out, err := clitest.Execute(t, commands, "runs", "start", "platform", "triage", "Sort the issues opened today.")
	if err != nil {
		t.Fatalf("runs start: %v\n%s", err, out)
	}

	body := bodyOf(t, script, http.MethodPost, runsPath)
	if body["agent"] != "triage" || body["input"] != "Sort the issues opened today." {
		t.Errorf("body = %v, want the agent and the input", body)
	}
	if _, ok := body["session_id"]; ok {
		t.Errorf("a session_id was sent with no --session: %v", body)
	}
	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/agents"); n != 0 {
		t.Errorf("the agent was looked up %d times", n)
	}
	for _, want := range []string{"started", runID, "queued"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output is missing %q:\n%s", want, out)
		}
	}
}

func TestStartWithASessionRunsInIt(t *testing.T) {
	script := clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun}))

	out, err := clitest.Execute(t, commands, "runs", "start", "platform", "triage", "And yesterday's?",
		"--session", sessionID)
	if err != nil {
		t.Fatalf("runs start --session: %v\n%s", err, out)
	}
	if got := bodyOf(t, script, http.MethodPost, runsPath)["session_id"]; got != sessionID {
		t.Errorf("session_id = %v, want %s", got, sessionID)
	}
}

// A long task lives in a file, and argv is the wrong place for one.
func TestStartReadsTheInputFromStdin(t *testing.T) {
	script := clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun}))

	out, err := clitest.ExecuteWithStdin(t, commands, "Sort the issues\nopened today.\n",
		"runs", "start", "platform", "triage", "-")
	if err != nil {
		t.Fatalf("runs start -: %v\n%s", err, out)
	}
	if got := bodyOf(t, script, http.MethodPost, runsPath)["input"]; got != "Sort the issues\nopened today.\n" {
		t.Errorf("input = %q, want stdin exactly", got)
	}
}

// Both refusals are local, so the brain is never asked: a run queued with
// nothing to do would still cost a worker and a session.
func TestStartRefusesBadInputWithoutAskingTheBrain(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"an empty input":     {args: []string{"runs", "start", "platform", "triage", "  "}, want: "empty"},
		"an empty stdin":     {stdin: "\n", args: []string{"runs", "start", "platform", "triage", "-"}, want: "empty"},
		"a session by name":  {args: []string{"runs", "start", "platform", "triage", "go", "--session", "support"}, want: "not a session id"},
		"no input at all":    {args: []string{"runs", "start", "platform"}, want: "accepts between 2 and 3 arg"},
		"four arguments":     {args: []string{"runs", "start", "platform", "triage", "go", "now"}, want: "accepts between 2 and 3 arg"},
		"an empty hub input": {args: []string{"runs", "start", "platform", "  "}, want: "empty"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun}))

			out, err := clitest.ExecuteWithStdin(t, commands, tc.stdin, tc.args...)
			if err == nil {
				t.Fatalf("%s was accepted:\n%s", name, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			if n := script.Calls(http.MethodPost, runsPath); n != 0 {
				t.Errorf("the run was started anyway, %d times", n)
			}
		})
	}
}

func TestStartNamesThePermissionItNeeded(t *testing.T) {
	clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusForbidden, Body: "forbidden"}))

	_, err := clitest.Execute(t, commands, "runs", "start", "platform", "triage", "go")
	if err == nil {
		t.Fatal("a refused start reported success")
	}
	if !strings.Contains(err.Error(), "run:write") {
		t.Errorf("the refusal does not name run:write: %v", err)
	}
}

// The agent is resolved by the brain, so its 404 is the only place a typo in
// the agent's name is reported — the sentence has to survive.
func TestStartKeepsTheBrainsReasonForAnUnknownAgent(t *testing.T) {
	clitest.Routes(t, with("POST /teams/{id}/runs",
		clitest.Reply{Status: http.StatusNotFound, Body: "no agent in this team has that name"}))

	_, err := clitest.Execute(t, commands, "runs", "start", "platform", "triag", "go")
	if err == nil {
		t.Fatal("an unknown agent reported success")
	}
	if !strings.Contains(err.Error(), "no agent in this team has that name") {
		t.Errorf("the brain's reason was dropped: %v", err)
	}
}

// Two arguments ask the team's hub: no agent is sent, and the brain starts
// whichever agent the team chose, or the built-in orchestrator.
func TestStartWithNoAgentAsksTheHub(t *testing.T) {
	script := clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun}))

	out, err := clitest.Execute(t, commands, "runs", "start", "platform", "What is due this week?")
	if err != nil {
		t.Fatalf("runs start: %v\n%s", err, out)
	}
	body := bodyOf(t, script, http.MethodPost, runsPath)
	if _, sent := body["agent"]; sent || body["input"] != "What is due this week?" {
		t.Errorf("body = %v, want the input and no agent", body)
	}
}

func TestStartWithNoAgentReadsTheInputFromStdin(t *testing.T) {
	script := clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun}))

	if out, err := clitest.ExecuteWithStdin(t, commands, "What is due?\n", "runs", "start", "platform", "-"); err != nil {
		t.Fatalf("runs start -: %v\n%s", err, out)
	}
	if got := bodyOf(t, script, http.MethodPost, runsPath)["input"]; got != "What is due?\n" {
		t.Errorf("input = %q, want stdin exactly", got)
	}
}

// With no model and no hub chosen, the orchestrator cannot run; the brain's
// sentence says where to fix it, and has to survive.
func TestStartKeepsTheBrainsReasonWhenTheHubCannotRun(t *testing.T) {
	const why = "the team has no model set, so its orchestrator cannot run; set one in the team's settings"
	clitest.Routes(t, with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusConflict, Body: why}))

	_, err := clitest.Execute(t, commands, "runs", "start", "platform", "hi")
	if err == nil || !strings.Contains(err.Error(), why) {
		t.Errorf("err = %v, want the brain's sentence", err)
	}
}

// --- listing a conversation's runs ---

func TestListAsksForTheSessionsRuns(t *testing.T) {
	page := `{"items":[` + finishedRun + `,` + runJSON(otherRun, "running", "") + `],"next_cursor":"bmV4dA"}`
	script := clitest.Routes(t, with("GET /teams/{id}/sessions/{sessionID}/runs", clitest.Reply{Status: http.StatusOK, Body: page}))

	out, err := clitest.Execute(t, commands, "runs", "list", "platform", sessionID, "--limit", "2", "--cursor", "abc")
	if err != nil {
		t.Fatalf("runs list: %v\n%s", err, out)
	}

	query := queryFor(t, script, listPath)
	if !strings.Contains(query, "limit=2") || !strings.Contains(query, "cursor=abc") {
		t.Errorf("query = %q, want the limit and the cursor", query)
	}
	for _, want := range []string{runID, otherRun, "succeeded", "running", "triage", "Sort the issues"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "oa runs list platform "+sessionID+" --cursor bmV4dA") {
		t.Errorf("the next page is not named:\n%s", out)
	}
}

// A limit of zero is refused by the brain, so the flag's default must mean
// "not sent" rather than "send 0".
func TestListSendsNoLimitUnlessAsked(t *testing.T) {
	script := clitest.Routes(t, with("GET /teams/{id}/sessions/{sessionID}/runs",
		clitest.Reply{Status: http.StatusOK, Body: `{"items":[]}`}))

	if out, err := clitest.Execute(t, commands, "runs", "list", "platform", sessionID); err != nil {
		t.Fatalf("runs list: %v\n%s", err, out)
	}
	if query := queryFor(t, script, listPath); strings.Contains(query, "limit") || strings.Contains(query, "cursor") {
		t.Errorf("query = %q, want neither", query)
	}
}

// output and error are null until the run has them. A script reading
// `.items[0].output` must see null, not a missing key on one run and a string
// on the next — and must see the string when there is one.
func TestListPrintsTheEnvelopeWithNullsKept(t *testing.T) {
	page := `{"items":[` + finishedRun + `,` + runJSON(otherRun, "queued", "") + `],"next_cursor":"bmV4dA"}`
	clitest.Routes(t, with("GET /teams/{id}/sessions/{sessionID}/runs", clitest.Reply{Status: http.StatusOK, Body: page}))

	out, err := clitest.Execute(t, commands, "runs", "list", "platform", sessionID, "-o", "json")
	if err != nil {
		t.Fatalf("runs list -o json: %v\n%s", err, out)
	}

	var got struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.NextCursor == nil || *got.NextCursor != "bmV4dA" {
		t.Errorf("next_cursor = %v, so a caller would stop at the first page", got.NextCursor)
	}

	done, queued := got.Items[0], got.Items[1]
	if done["output"] != "Labelled 4 issues." || done["finished_at"] == nil || done["started_at"] == nil {
		t.Errorf("a finished run lost what it has: %v", done)
	}
	for _, key := range []string{"output", "error", "started_at", "finished_at"} {
		v, ok := queued[key]
		if !ok {
			t.Errorf("%s is missing from a queued run, want null", key)
		} else if v != nil {
			t.Errorf("%s = %v on a queued run, want null", key, v)
		}
	}
	if queued["session_id"] != sessionID || queued["input_tokens"] != float64(120) {
		t.Errorf("a queued run's fields = %v", queued)
	}
}

func TestListOfNoRunsIsAnEmptyArray(t *testing.T) {
	clitest.Routes(t, with("GET /teams/{id}/sessions/{sessionID}/runs", clitest.Reply{Status: http.StatusOK, Body: `{"items":[]}`}))

	out, err := clitest.Execute(t, commands, "runs", "list", "platform", sessionID, "-o", "json")
	if err != nil {
		t.Fatalf("runs list: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"items": []`) {
		t.Errorf("no runs printed %q, want an empty array", out)
	}

	out, err = clitest.Execute(t, commands, "runs", "list", "platform", sessionID)
	if err != nil {
		t.Fatalf("runs list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no runs") {
		t.Errorf("an empty table said nothing:\n%s", out)
	}
}

// A session has no name, only an id copied from a list. Sending the brain a
// name would get a 400 about a uuid nobody typed.
func TestListRefusesASessionNameWithoutAskingTheBrain(t *testing.T) {
	script := clitest.Routes(t, teamRoute())

	_, err := clitest.Execute(t, commands, "runs", "list", "platform", "support")
	if err == nil || !strings.Contains(err.Error(), "not a session id") {
		t.Fatalf("err = %v, want it to say a session id is wanted", err)
	}
	for _, e := range script.All() {
		if strings.Contains(e.Path, "/sessions/") {
			t.Errorf("the brain was asked anyway: %s %s", e.Method, e.Path)
		}
	}
}

func TestListReportsASessionItCannotSee(t *testing.T) {
	clitest.Routes(t, with("GET /teams/{id}/sessions/{sessionID}/runs", clitest.Reply{Status: http.StatusNotFound, Body: "not found"}))

	_, err := clitest.Execute(t, commands, "runs", "list", "platform", sessionID)
	if err == nil || !strings.Contains(err.Error(), "not found, or not visible to you") {
		t.Errorf("err = %v, want the 404 sentence", err)
	}
}

// --- reading one ---

func TestGetShowsTheRun(t *testing.T) {
	script := clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: finishedRun}))

	out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID)
	if err != nil {
		t.Fatalf("runs get: %v\n%s", err, out)
	}
	if n := script.Calls(http.MethodGet, runPath); n != 1 {
		t.Errorf("the run was read %d times", n)
	}
	if !strings.HasSuffix(out, "\nAnswer\n  Labelled 4 issues.\n") {
		t.Errorf("the answer is not under its label, after the table:\n%s", out)
	}
	for _, want := range []string{"succeeded", "triage", runID, sessionID, "Labelled 4 issues.", "120 in, 45 out", "finished"} {
		if !strings.Contains(out, want) {
			t.Errorf("the run is missing %q:\n%s", want, out)
		}
	}
}

func TestGetShowsWhyARunFailed(t *testing.T) {
	clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: failedRun}))

	out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID)
	if err != nil {
		t.Fatalf("runs get: %v\n%s", err, out)
	}
	if !strings.Contains(out, "token budget") {
		t.Errorf("the error is missing:\n%s", out)
	}
}

// A run that is not yours answers 404 exactly as one that does not exist, and
// the CLI must not undo that by guessing which it was.
func TestGetOfARunItCannotSeeSaysSo(t *testing.T) {
	clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusNotFound, Body: "not found"}))

	_, err := clitest.Execute(t, commands, "runs", "get", "platform", runID)
	if err == nil {
		t.Fatal("a 404 reported success")
	}
	if !strings.Contains(err.Error(), "not found, or not visible to you") {
		t.Errorf("err = %v, want the 404 sentence", err)
	}
}

func TestGetRefusesARunNameWithoutAskingTheBrain(t *testing.T) {
	script := clitest.Routes(t, teamRoute())

	_, err := clitest.Execute(t, commands, "runs", "get", "platform", "triage")
	if err == nil || !strings.Contains(err.Error(), "not a run id") {
		t.Fatalf("err = %v, want it to say a run id is wanted", err)
	}
	for _, e := range script.All() {
		if strings.Contains(e.Path, "/runs") {
			t.Errorf("the brain was asked anyway: %s %s", e.Method, e.Path)
		}
	}
}

// The output is the model's, and a model repeats whatever a tool fed it. A
// page it read can carry an escape sequence straight onto the screen of
// whoever reads the run.
func TestGetNeutralisesWhatTheModelWrote(t *testing.T) {
	hostile := runJSON(runID, "succeeded", `,"output":"done \u001b[2J\u001b[H\nsecond line"`)
	clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: hostile}))

	out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID)
	if err != nil {
		t.Fatalf("runs get: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "\nAnswer\n  done\n  second line\n") {
		t.Errorf("the answer is not its lines, indented under the label:\n%s", out)
	}

	out, err = clitest.Execute(t, commands, "runs", "get", "platform", runID, "-o", "json")
	if err != nil {
		t.Fatalf("runs get -o json: %v\n%s", err, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if got["output"] != "done \x1b[2J\x1b[H\nsecond line" {
		t.Errorf("output = %q, want it exactly as stored", got["output"])
	}
}

func TestEveryFormatPrintsTheSameRunAndNoCredential(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: finishedRun}))

			out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID, "-o", format)
			if err != nil {
				t.Fatalf("runs get -o %s: %v\n%s", format, err, out)
			}
			if !strings.Contains(out, runID) || !strings.Contains(out, "Labelled 4 issues.") {
				t.Errorf("-o %s lost the run:\n%s", format, out)
			}
			if strings.Contains(out, "a-token") {
				t.Errorf("-o %s printed the credential:\n%s", format, out)
			}

			switch format {
			case "json":
				var got runView
				if err := json.Unmarshal([]byte(out), &got); err != nil || got.ID != runID {
					t.Errorf("not the run as json: %v\n%s", err, out)
				}
			case "yaml":
				var got runView
				if err := yaml.Unmarshal([]byte(out), &got); err != nil || got.SessionID != sessionID {
					t.Errorf("not the run as yaml: %v\n%s", err, out)
				}
				if strings.Contains(out, "{") {
					t.Errorf("the yaml is json:\n%s", out)
				}
			}
		})
	}
}

// --- cancelling one ---

func TestCancelAsksTheBrainToStopTheRun(t *testing.T) {
	cancelled := runJSON(runID, "cancelled", `,"finished_at":"2026-10-21T09:00:02Z"`)
	script := clitest.Routes(t, with("POST /teams/{id}/runs/{runID}/cancel", clitest.Reply{Status: http.StatusAccepted, Body: cancelled}))

	out, err := clitest.Execute(t, commands, "runs", "cancel", "platform", runID)
	if err != nil {
		t.Fatalf("runs cancel: %v\n%s", err, out)
	}
	if n := script.Calls(http.MethodPost, cancelPath); n != 1 {
		t.Errorf("cancel was sent %d times", n)
	}
	if !strings.Contains(out, "cancelled") || strings.Contains(out, "cancelling") {
		t.Errorf("a queued run's cancel does not say it is done:\n%s", out)
	}
}

// A running run is only marked; it stops at its next heartbeat. Saying
// "cancelled" would send somebody to read output that is still being written.
func TestCancellingARunningRunSaysItIsStillStopping(t *testing.T) {
	clitest.Routes(t, with("POST /teams/{id}/runs/{runID}/cancel",
		clitest.Reply{Status: http.StatusAccepted, Body: runJSON(runID, "running", "")}))

	out, err := clitest.Execute(t, commands, "runs", "cancel", "platform", runID)
	if err != nil {
		t.Fatalf("runs cancel: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cancelling") || !strings.Contains(out, "15 seconds") {
		t.Errorf("a running run's cancel does not say it is still stopping:\n%s", out)
	}
}

// 409 is the brain saying there is nothing left to cancel — not a failure of
// the request. The sentence says what happened and where to look.
func TestCancellingAFinishedRunSaysSoPlainly(t *testing.T) {
	clitest.Routes(t, with("POST /teams/{id}/runs/{runID}/cancel",
		clitest.Reply{Status: http.StatusConflict, Body: "the run has already finished"}))

	out, err := clitest.Execute(t, commands, "runs", "cancel", "platform", runID)
	if err == nil {
		t.Fatalf("cancelling a finished run reported success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "already finished") || !strings.Contains(err.Error(), "oa runs get platform "+runID) {
		t.Errorf("err = %v, want it to say the run already finished and how to see how", err)
	}
	if strings.Contains(err.Error(), "run:write") {
		t.Errorf("a 409 was blamed on a permission: %v", err)
	}
}

func TestCancelReportsRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusNotFound, "not found", "not found, or not visible to you"},
		{http.StatusForbidden, "forbidden", "run:write"},
		{http.StatusOK, queuedRun, "200"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			clitest.Routes(t, with("POST /teams/{id}/runs/{runID}/cancel", clitest.Reply{Status: tc.status, Body: tc.body}))

			out, err := clitest.Execute(t, commands, "runs", "cancel", "platform", runID)
			if err == nil {
				t.Fatalf("a %d reported success:\n%s", tc.status, out)
			}
			if tc.status != http.StatusOK && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

const parentID = "5e55a1b2-0000-4000-8000-0000000000aa"

// A delegate's run says which run started it, in every format, so a child
// found on its own leads back to its parent.
func TestAChildRunNamesTheRunThatDelegatedIt(t *testing.T) {
	child := runJSON(runID, "succeeded", `,"parent_run_id":"`+parentID+`","depth":1`)
	for _, format := range []string{"json", "table"} {
		t.Run(format, func(t *testing.T) {
			clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: child}))

			out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID, "-o", format)
			if err != nil {
				t.Fatalf("runs get: %v\n%s", err, out)
			}
			if format == "json" {
				var got map[string]any
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("not json: %v\n%s", err, out)
				}
				if got["parent_run_id"] != parentID || got["depth"] != float64(1) {
					t.Errorf("parent %v depth %v, want %s at depth 1", got["parent_run_id"], got["depth"], parentID)
				}
				return
			}
			if !strings.Contains(out, "delegated by") || !strings.Contains(out, "oa runs get platform "+parentID) {
				t.Errorf("the table does not lead to the parent:\n%s", out)
			}
		})
	}
}

func TestARunAPersonStartedHasNoParent(t *testing.T) {
	clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: runJSON(runID, "succeeded", `,"depth":0`)}))

	out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID, "-o", "json")
	if err != nil {
		t.Fatalf("runs get: %v\n%s", err, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if v, has := got["parent_run_id"]; !has || v != nil || got["depth"] != float64(0) {
		t.Errorf("parent %v (present %v) depth %v, want null at depth 0", v, has, got["depth"])
	}
	table, _ := clitest.Execute(t, commands, "runs", "get", "platform", runID)
	if strings.Contains(table, "delegated by") {
		t.Errorf("a top-level run claims a parent:\n%s", table)
	}
}

// A run started for the team says so in every format: the view is built field
// by field, so a field the brain sends and the view lacks is silently dropped.
func TestAHubRunSaysItWasTheHubs(t *testing.T) {
	for name, tc := range map[string]struct {
		extra string
		hub   bool
	}{
		"the hub's":  {`,"hub":true,"depth":0`, true},
		"an agent's": {`,"hub":false,"depth":0`, false},
	} {
		t.Run(name, func(t *testing.T) {
			clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: runJSON(runID, "succeeded", tc.extra)}))

			out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID, "-o", "json")
			if err != nil {
				t.Fatalf("runs get: %v\n%s", err, out)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("not json: %v\n%s", err, out)
			}
			if got["hub"] != tc.hub {
				t.Errorf("hub = %v, want %v", got["hub"], tc.hub)
			}
			table, _ := clitest.Execute(t, commands, "runs", "get", "platform", runID)
			if strings.Contains(table, "the team's hub") != tc.hub {
				t.Errorf("the table says hub %v, want %v:\n%s", !tc.hub, tc.hub, table)
			}
		})
	}
}
