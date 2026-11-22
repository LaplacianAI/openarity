package improve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A daily pass has no pattern of its own; what it was shown going wrong is
// what its proposals may answer, so a fix for it is stored.
func TestADailyPassStoresAFixForWhatItWasShown(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var runs []uuid.UUID
	for range 2 { // under the pattern bar: only a daily pass sees it
		runs = append(runs, w.digested(t, skipped, "wrong", false))
	}
	pass := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily})
	if len(pass.Signatures) != 0 {
		t.Fatalf("a daily pass was given patterns: %v", pass.Signatures)
	}
	out, _ := w.proposeWith(t, im, pass, instructionsEdit(runs[0]), reply{text: screenOKText})
	if !strings.HasPrefix(out, "Stored") {
		t.Fatalf("propose = %q, want the fix stored", out)
	}
	// One it was not shown is still refused.
	other := instructionsEdit(runs[0])
	other["signature"] = "tool:crm:misused_tool"
	if out, _ := w.proposeWith(t, im, pass, other, reply{text: screenOKText}); !strings.Contains(out, "not one of this pass's patterns") {
		t.Errorf("a signature the pass was not shown = %q", out)
	}
}

// Once a pass may propose no more, it says plainly that nothing was stored.
func TestDoneProposingSaysNotStored(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	runs := []uuid.UUID{w.digested(t, skipped, "wrong", false)}
	pass := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily})
	m, err := im.material(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	tools := &passTools{im: im, pass: pass, material: m, tokens: 1 << 30}
	bad := instructionsEdit(runs[0])
	bad["signature"] = "tool:crm:misused_tool"
	b, _ := json.Marshal(bad)
	var out string
	for range refusalsPerPass + 1 {
		if out, err = tools.propose(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(out, "Not stored:") || !strings.Contains(out, "done proposing") {
		t.Errorf("after %d refusals: %q, want it to say not stored", refusalsPerPass, out)
	}
}

func (w *world) improving(t *testing.T) map[uuid.UUID]db.ListImprovingAgentsRow {
	t.Helper()
	rows, err := w.store.ListImprovingAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[uuid.UUID]db.ListImprovingAgentsRow{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// A daily pass waits for the team's daily runs judged since the agent's
// last pass: four of five is not enough, five is, and a pass resets the
// count. The team may change the number.
func TestADailyPassWaitsForEnoughRuns(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	for range 4 {
		w.digested(t, skipped, "wrong", false)
	}
	if _, ok := w.improving(t)[w.agent]; ok {
		t.Fatal("an agent with 4 judged runs was offered a daily pass")
	}
	w.judged(t, w.agent, "30 minutes", verdictDone, 100)
	got, ok := w.improving(t)[w.agent]
	if !ok || got.RunsSince != 5 || !got.HasNew {
		t.Fatalf("with 5 judged runs: %+v, %v", got, ok)
	}
	// Past the bar the count is exact, not stopped at it.
	w.judged(t, w.agent, "20 minutes", verdictDone, 100)
	w.judged(t, w.agent, "10 minutes", verdictDone, 100)
	if got = w.improving(t)[w.agent]; got.RunsSince != 7 {
		t.Fatalf("with 7 judged runs: %+v", got)
	}

	seen := got.RunsSince
	pass := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily, RunsSeen: &seen})
	if pass.RunsSeen == nil || *pass.RunsSeen != 7 {
		t.Errorf("the pass kept runs seen %v, want 7", pass.RunsSeen)
	}
	if err := im.done(t.Context(), pass.ID, outcome{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.improving(t)[w.agent]; ok {
		t.Error("runs from before the last pass were counted again")
	}

	w.exec(t, `UPDATE team_settings SET improvement_daily_runs = 1 WHERE team_id = $1`, w.team)
	w.judged(t, w.agent, "0 seconds", verdictDone, 100)
	if got, ok := w.improving(t)[w.agent]; !ok || got.RunsSince != 1 {
		t.Errorf("with the bar at 1 and one new run: %+v, %v", got, ok)
	}
}

// A pass whose team turned improvement off says so, not that its proposals
// were refused.
func TestAPassStoppedByTheOffSwitchSaysWhy(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	runs := []uuid.UUID{w.digested(t, skipped, "wrong", false)}
	pass := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily})
	m, err := im.material(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	tools := &passTools{im: im, pass: pass, material: m, tokens: 1 << 30}
	w.exec(t, `UPDATE team_settings SET improvement = false WHERE team_id = $1`, w.team)
	b, _ := json.Marshal(instructionsEdit(runs[0]))
	if _, err := tools.propose(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	out, err := tools.propose(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Not stored: improvement was turned off") || strings.Contains(out, "refused") {
		t.Errorf("after the off switch: %q", out)
	}
}
