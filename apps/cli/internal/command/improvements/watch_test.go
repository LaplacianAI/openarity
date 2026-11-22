package improvements

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const flaggedWatch = `,"watch":{"status":"flagged","flag":"worse","needed":10,"baseline_runs":20,"baseline_bad":2,"bar":5,
  "baseline_p90":1100,"before":{"runs":10,"bad":2,"up":1,"down":0,"tokens":1000,"steps":4,"signature":3},
  "after":{"runs":10,"bad":7,"up":0,"down":2,"tokens":1200,"steps":5,"signature":0},
  "overlapping":["` + runA + `"],"started_at":"2026-10-22T09:00:00Z","finished_at":"2026-10-25T09:00:00Z"}`

// A watched change shows how it went: why it was flagged with the counts,
// the agent's usual rate and the bar, cost, the targeted pattern, votes and
// the changes beside it.
func TestShowPrintsTheWatch(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements/{proposalID}": {Status: http.StatusOK, Body: proposal("applied", flaggedWatch)},
	}))
	out, err := clitest.Execute(t, commands, "improvements", "show", "platform", propID)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out,
		"flagged, its runs went worse: 7 of 10 went wrong after, 2 of 10 before",
		"2 of 20 went wrong before; flagged at 5 of 10",
		"tokens a run 1000 → 1200, steps 4 → 5",
		"in 3 of 10 runs before, 0 of 10 after",
		"before 👍 1 👎 0, after 👍 0 👎 2",
		runA)
}

func TestWatchLineSaysEachState(t *testing.T) {
	bar := 5
	reverted, tooFew := "reverted", "too_few_runs"
	for _, tc := range []struct {
		w    watchView
		want string
	}{
		{watchView{Status: "watching", Needed: 10, After: sideView{Runs: 4}}, "watching: 4 of 10 runs so far"},
		{watchView{Status: "clear", After: sideView{Runs: 10, Bad: 1}, Before: sideView{Runs: 10, Bad: 3}}, "clear: 1 of 10 went wrong after, 3 of 10 before"},
		{watchView{Status: "ended", EndedWhy: &reverted, Needed: 10, After: sideView{Runs: 3}}, "ended when reverted: 3 of 10 runs counted"},
		{watchView{Status: "ended", EndedWhy: &tooFew, Needed: 10, After: sideView{Runs: 2}}, "ended with too few runs: 2 of 10 in 30 days"},
	} {
		if got := watchLine(&tc.w); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.w.Status, got, tc.want)
		}
	}
	if got := usual(&watchView{BaselineRuns: 4}); !strings.Contains(got, "too little history") {
		t.Errorf("usual without a bar = %q", got)
	}
	if got := usual(&watchView{Bar: &bar, BaselineBad: 3, BaselineRuns: 50, Needed: 10}); got != "3 of 50 went wrong before; flagged at 5 of 10" {
		t.Errorf("usual = %q", got)
	}
}

// --status flagged is passed on as the brain's filter.
func TestListAsksForTheFlagged(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/improvements": {Status: http.StatusOK, Body: `{"items":[` + proposal("applied", flaggedWatch) + `]}`},
	}))
	if _, err := clitest.Execute(t, commands, "improvements", "list", "platform", "--status", "flagged"); err != nil {
		t.Fatal(err)
	}
	asked := false
	for _, e := range script.All() {
		if e.Path == "/teams/"+teamID+"/improvements" && strings.Contains(e.Query, "status=flagged") {
			asked = true
		}
	}
	if !asked {
		t.Error("the list did not ask for status=flagged")
	}
}
