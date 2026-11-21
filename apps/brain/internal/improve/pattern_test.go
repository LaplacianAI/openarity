package improve

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func TestAKeyReadsBackWhole(t *testing.T) {
	for _, k := range []Key{
		{Part: "instructions", Cause: "skipped_step"},
		{Part: "tool", Target: "crm__close_ticket", Cause: "misused_tool"},
		{Part: "skill", Target: "odd:name", Cause: "missing_knowledge"},
	} {
		got, ok := parseKey(k.String())
		if !ok || got != k {
			t.Errorf("parseKey(%q) = %+v %v", k, got, ok)
		}
	}
	for _, bad := range []string{"", "instructions", "nope::skipped_step", "instructions::nope", "instructions:skipped_step"} {
		if _, ok := parseKey(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestPatternsAreGroupedByAgentLargestFirst(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	row := func(agent uuid.UUID, cause string, runs int32) db.ListPatternsRow {
		return db.ListPatternsRow{AgentID: agent, Part: "instructions", Cause: cause, Runs: runs}
	}
	got := patternPasses([]db.ListPatternsRow{
		row(a, "skipped_step", 9), row(a, "gave_up", 5), row(a, "misread_request", 4), row(a, "ran_out", 3),
		row(b, "other", 3),
	})
	if len(got) != 2 || got[0].AgentID != a || got[1].AgentID != b {
		t.Fatalf("passes = %+v", got)
	}
	if want := []string{"instructions::skipped_step", "instructions::gave_up", "instructions::misread_request"}; !slices.Equal(got[0].Keys, want) {
		t.Errorf("keys = %v, want the largest %d", got[0].Keys, keysPerPass)
	}
	if got[0].Trigger != triggerPattern {
		t.Errorf("trigger = %s", got[0].Trigger)
	}
	back, err := decodePlan(got[0].encode())
	if err != nil || back.AgentID != a || !slices.Equal(back.Keys, got[0].Keys) {
		t.Errorf("a plan did not read back: %+v %v", back, err)
	}
}

// A member's 👎 first, then the worst verdicts, newest first; a run once.
func TestTheClearestFailuresAndClosestSuccessesAreRead(t *testing.T) {
	r := func(rank int, at int64) toRead { return toRead{run: uuid.New(), rank: rank, at: at, failure: true} }
	down, wrong, partly, older := r(0, 1), r(1, 5), r(3, 9), r(1, 2)
	failures := []toRead{partly, older, wrong, down, r(4, 1), r(4, 2), r(4, 3), r(4, 4)}
	successes := []toRead{{run: uuid.New(), at: 1}, {run: uuid.New(), at: 2}, {run: down.run, at: 3}, {run: uuid.New(), at: 4}, {run: uuid.New(), at: 5}, {run: uuid.New(), at: 6}}
	got := chooseRuns(failures, successes)
	if len(got) != failuresToRead+successesToRead {
		t.Fatalf("%d runs, want %d", len(got), failuresToRead+successesToRead)
	}
	if got[0].run != down.run || got[1].run != wrong.run || got[2].run != older.run || got[3].run != partly.run {
		t.Errorf("failures out of order: %+v", got[:4])
	}
	seen := map[uuid.UUID]bool{}
	for _, x := range got {
		if seen[x.run] {
			t.Errorf("run %s chosen twice", x.run)
		}
		seen[x.run] = true
	}
}

func use(run uuid.UUID, kind string, payload any) db.ListRunsUseRow {
	b, _ := json.Marshal(payload)
	return db.ListRunsUseRow{RunID: run, Kind: kind, Payload: b}
}

func TestPruneCandidatesAreHeldAlwaysAndNeverUsed(t *testing.T) {
	tools := []grantedTool{
		{runName: "crm__lookup", server: "crm", tool: "lookup"},
		{runName: "crm__close", server: "crm", tool: "close"},
		{runName: "fetch", server: "web", tool: "fetch"},
	}
	skills := []string{"refunds", "returns", "rare"}
	build := func(n int, extra func(i int, run uuid.UUID) []db.ListRunsUseRow) ([]uuid.UUID, []db.ListRunsUseRow) {
		var runs []uuid.UUID
		var rows []db.ListRunsUseRow
		for i := range n {
			run := uuid.New()
			runs = append(runs, run)
			held := []string{"refunds", "returns"}
			if i > 0 {
				held = append(held, "rare") // granted after the window began
			}
			rows = append(rows, use(run, "started", startedPayload{Skills: held, Tools: []string{"crm__lookup", "crm__close", "fetch"}}))
			rows = append(rows, extra(i, run)...)
		}
		return runs, rows
	}
	runs, rows := build(pruneWindow, func(i int, run uuid.UUID) []db.ListRunsUseRow {
		switch i {
		case 3:
			return []db.ListRunsUseRow{use(run, "tool_call", toolCallPayload{Name: skillTool, Arguments: `{"name":"refunds"}`})}
		case 7:
			return []db.ListRunsUseRow{use(run, "tool_call", toolCallPayload{Name: callTool, Arguments: `{"name":"crm__lookup","arguments":{}}`})}
		case 9:
			return []db.ListRunsUseRow{use(run, "tool_call", toolCallPayload{Name: "fetch", Arguments: `not json`})}
		}
		return nil
	})
	got := pruneCandidates(runs, rows, skills, tools)
	if want := []string{"skill:returns", "tool:crm/close"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	if got := pruneCandidates(runs[:pruneWindow-1], rows, skills, tools); got != nil {
		t.Errorf("fewer runs than the window gave %v", got)
	}
}

func TestAnEditReplacesOnePassage(t *testing.T) {
	long := strings.Repeat("Be kind and brief. ", 40) + "Close the ticket when done."
	for name, tc := range map[string]struct {
		text string
		edit replaceEdit
		want string
		bad  string
	}{
		"replaced once": {long, replaceEdit{"Close the ticket when done.", "Confirm the order id, then close the ticket."}, strings.Repeat("Be kind and brief. ", 40) + "Confirm the order id, then close the ticket.", ""},
		"appended":      {"Be brief.", replaceEdit{"", "  Confirm the order id first. "}, "Be brief.\n\nConfirm the order id first.", ""},
		"removed":       {long, replaceEdit{" Close the ticket when done.", ""}, strings.TrimSuffix(strings.Repeat("Be kind and brief. ", 40), " "), ""},
		"short, whole":  {"Be brief.", replaceEdit{"Be brief.", "Be thorough."}, "Be thorough.", ""},
		"absent":        {long, replaceEdit{"Never close tickets.", "x"}, "", "not in"},
		"twice":         {long, replaceEdit{"Be kind and brief.", "x"}, "", "more than once"},
		"most at once":  {long, replaceEdit{strings.Repeat("Be kind and brief. ", 39) + "Be kind", "x"}, "", "most of"},
		"empty append":  {long, replaceEdit{"", "  "}, "", "adds some text"},
	} {
		got, err := tc.edit.apply(tc.text, "the instructions")
		switch {
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("%s: err %v, want one saying %q", name, err, tc.bad)
		case tc.bad == "" && (err != nil || got != tc.want):
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
}

func TestOnlyWhatIsAddedIsScreened(t *testing.T) {
	for _, tc := range []struct{ before, after, want string }{
		{"Be brief. Close it.", "Be brief. Confirm the id. Close it.", "Confirm the id. Close"},
		{"café ok", "café très ok", "très"},
		{"same", "same", ""},
		{"", "all new", "all new"},
	} {
		if got := added(tc.before, tc.after); got != tc.want {
			t.Errorf("added(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
		}
	}
}

func TestALongTextKeepsItsEnds(t *testing.T) {
	s := strings.Repeat("a", 10) + strings.Repeat("b", 100) + strings.Repeat("c", 10)
	got := middle(s, 20)
	if !strings.HasPrefix(got, "aaaaaaaaaa") || !strings.HasSuffix(got, "cccccccccc") || !strings.Contains(got, fmt.Sprintf("%d characters left out", 100)) {
		t.Errorf("middle = %q", got)
	}
	if middle("short", 20) != "short" {
		t.Error("a short text was cut")
	}
}
