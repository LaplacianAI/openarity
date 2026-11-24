package improvements

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const tried = `,"replay":{"status":"done","runs":[
  {"run":{"id":"` + runA + `","readable":true},"chosen":"cited","status":"done","original":"bad","replayed":"good","outcome":"fixed",
   "request":"refund order 42","withheld":false,"diverged":true,"recorded":2,"live":0,"not_made":1,
   "before":{"tokens":18400,"steps":9,"work_ms":41000},"after":{"tokens":12000,"steps":5,"work_ms":20000}},
  {"run":{"id":"` + runB + `","readable":false},"chosen":"good","status":"done","original":"good","replayed":"bad","outcome":"broke",
   "request":null,"withheld":true,"diverged":false,"recorded":3,"live":1,"not_made":0,
   "before":{"tokens":9000,"steps":4,"work_ms":10000},"after":{"tokens":9500,"steps":4,"work_ms":11000}},
  {"run":{"id":"` + userID + `","readable":true},"chosen":"cited","status":"skipped","original":"bad",
   "skip_why":"its conversation was compacted since it began","request":"cancel \u001b[2Jit","withheld":false,
   "diverged":false,"recorded":0,"live":0,"not_made":0,"before":{"tokens":1,"steps":1,"work_ms":1}}]}`

// show says how a proposal fared on past runs: the summary, worse when a
// run that went well now goes wrong, and each run beside its try, a run
// whose conversation the caller cannot read said so.
func TestShowSaysHowAProposalFaredOnPastRuns(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("waiting", tried)},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	contains(t, out,
		"worse on past runs",
		"tried on 2 past runs: 1 that went wrong now done · 1 that went well now go wrong",
		"refund order 42 · went wrong, now done · tokens 18400 → 12000 · steps 9 → 5",
		"went a different way: 1 call not made",
		"went well, now goes wrong",
		"not tried · its conversation was compacted",
	)
	if strings.Contains(out, "\x1b[2J") {
		t.Error("a request's escape reached the terminal")
	}
}

func TestTheReplaySummaryNamesEachState(t *testing.T) {
	for status, want := range map[string]string{
		"off": "not tried: trying on past runs was off", "trying": "trying on past runs…", "none": "not tried on any past run",
	} {
		if got := (&replayView{Status: status}).summary(); !strings.HasPrefix(got, want) {
			t.Errorf("%s = %q, want %q", status, got, want)
		}
	}
}

// While some tries are still to run, the summary says how many are done.
func TestTheReplaySummaryCountsTriesWhileTrying(t *testing.T) {
	fixed := "fixed"
	v := &replayView{Status: "trying", Runs: []replayRunView{
		{Status: "done", Outcome: &fixed}, {Status: "trying"}, {Status: "queued"}, {Status: "skipped"},
	}}
	if got, want := v.summary(), "trying on past runs… 1 of 3 tried"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// A team with trying off sees no line for it under a proposal.
func TestShowSaysNothingOfTriesWithReplayOff(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("waiting", `,"replay":{"status":"off","runs":[]}`)},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(out, "past runs") {
		t.Errorf("replay off printed a line for it:\n%s", out)
	}
}

// replay prints each call with how it was served, and the answer.
func TestReplayPrintsATry(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}/replays/{runID}": {Status: http.StatusOK, Body: `{
  "run":{"id":"` + runA + `","readable":true},"answer":"Refunded order 42.",
  "calls":[{"name":"orders__lookup","how":"recorded","arguments":"{\"id\":42}","output":"order 42: paid"},
           {"name":"orders__refund","how":"not_made","arguments":"{\"id\":42}","error":"Not made: this is a replay"}]}`},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "replay", "platform", propID, runA)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	contains(t, out, "orders__lookup", "recorded", "order 42: paid", "orders__refund", "not made", "Refunded order 42.")
}

// Someone who may not read the run's conversation is told so.
func TestReplayOfAnUnreadableRunIsRefused(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}/replays/{runID}": {Status: http.StatusForbidden, Body: "the run's conversation is not one you can read"},
	}))
	if _, err := clitest.Execute(t, commands, "improvements", "replay", "platform", propID, runA); err == nil {
		t.Error("a refused trace printed nothing wrong")
	}
}
