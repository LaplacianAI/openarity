package improve

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ev is one event of a run's tree, for runCostOf.
func ev(run uuid.UUID, kind string, f func(*db.ListEfficiencyEventsRow)) db.ListEfficiencyEventsRow {
	e := db.ListEfficiencyEventsRow{RunID: run, Kind: kind}
	if f != nil {
		f(&e)
	}
	return e
}

func callEv(run uuid.UUID, id, name, args string) db.ListEfficiencyEventsRow {
	return ev(run, "tool_call", func(e *db.ListEfficiencyEventsRow) { e.CallID, e.Name, e.ArgsHash = id, name, args })
}

func resultEv(run uuid.UUID, id, name string, failed bool, ms int64) db.ListEfficiencyEventsRow {
	return ev(run, "tool_result", func(e *db.ListEfficiencyEventsRow) {
		e.CallID, e.Name, e.Failed, e.DurationMs = id, name, failed, ms
	})
}

func started(run uuid.UUID, tools int32) db.ListEfficiencyEventsRow {
	return ev(run, "started", func(e *db.ListEfficiencyEventsRow) { e.Tools = tools })
}

func steps(run uuid.UUID, n int) []db.ListEfficiencyEventsRow {
	var out []db.ListEfficiencyEventsRow
	for range n {
		out = append(out, ev(run, "step", nil))
	}
	return out
}

func markKeys(c runCost) []string {
	var out []string
	for _, m := range c.marks {
		out = append(out, m.key())
	}
	return out
}

// Each waste mark is found in a run built to show it, and not in a clean
// run that comes close to each.
func TestEachWasteMarkIsFoundOnlyWhereItShows(t *testing.T) {
	root, child := uuid.New(), uuid.New()
	clean := slices.Concat(
		[]db.ListEfficiencyEventsRow{started(root, 19)},
		steps(root, 3), // of 5: under 80%, though the tree's are over
		steps(child, 2),
		[]db.ListEfficiencyEventsRow{
			callEv(root, "1", "crm__lookup", "a"), resultEv(root, "1", "crm__lookup", true, 30_000),
			callEv(root, "2", "crm__lookup", "b"), resultEv(root, "2", "crm__lookup", false, 10),
			// The same call in another run of the tree is not a repeat.
			callEv(child, "1", "crm__lookup", "b"), resultEv(child, "1", "crm__lookup", false, 10),
		},
	)
	if got := runCostOf(root, clean, 5); len(got.marks) != 0 {
		t.Fatalf("a clean run was marked %v", markKeys(got))
	}

	cases := []struct {
		name   string
		events []db.ListEfficiencyEventsRow
		want   string
	}{
		{"repeated", []db.ListEfficiencyEventsRow{
			callEv(root, "1", "crm__lookup", "a"), resultEv(root, "1", "crm__lookup", false, 10),
			callEv(root, "2", "crm__lookup", "a"), resultEv(root, "2", "crm__lookup", false, 10),
		}, "efficiency:repeated_call:crm__lookup"},
		{"retried", []db.ListEfficiencyEventsRow{
			callEv(root, "1", "crm__lookup", "a"), resultEv(root, "1", "crm__lookup", true, 10),
			callEv(root, "2", "crm__lookup", "a"),
		}, "efficiency:retried_error:crm__lookup"},
		{"unused", append([]db.ListEfficiencyEventsRow{started(root, 20)},
			callEv(root, "1", "a", "x"), callEv(root, "2", "b", "x")), "efficiency:unused_tools"},
		{"near cap", steps(root, 4), "efficiency:near_step_cap"},
		{"long wait", []db.ListEfficiencyEventsRow{
			callEv(root, "1", "slow__report", "a"), resultEv(root, "1", "slow__report", false, 30_001),
		}, "efficiency:long_tool_wait:slow__report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := markKeys(runCostOf(root, tc.events, 5)); !slices.Contains(got, tc.want) {
				t.Errorf("marks = %v, want %s", got, tc.want)
			}
		})
	}
}

// What a run's tree did is counted: its steps, tool time, the tools it was
// offered and the ones it called.
func TestARunsCostIsReadFromItsEvents(t *testing.T) {
	root, child := uuid.New(), uuid.New()
	c := runCostOf(root, slices.Concat(
		[]db.ListEfficiencyEventsRow{started(root, 7), started(child, 40)},
		steps(root, 2), steps(child, 1),
		[]db.ListEfficiencyEventsRow{
			callEv(root, "1", "a", "x"), resultEv(root, "1", "a", false, 1500),
			callEv(child, "1", "b", "x"), resultEv(child, "1", "b", false, 500),
		},
	), 10)
	if c.steps != 3 || c.toolMS != 2000 || c.offered != 7 || c.called != 1 || !slices.Equal(c.tools, []string{"a", "b"}) {
		t.Errorf("cost = %+v", c)
	}
}

func costs(tokens ...int64) []runCost {
	out := make([]runCost, len(tokens))
	for i, n := range tokens {
		out[i] = runCost{run: uuid.New(), tokens: n, workMS: 1000}
	}
	return out
}

// A pass is called for by a spread or by a mark in enough runs, each alone;
// neither calls for none. The team's numbers decide.
func TestReasonsComeFromTheSpreadOrAMark(t *testing.T) {
	even := costs(slices.Repeat([]int64{100}, 20)...)
	if got := reasons(even, efficiencyBar{spread: 200, markRuns: 3}); len(got) != 0 {
		t.Fatalf("even runs with no marks gave %v", got)
	}

	spread := costs(append(slices.Repeat([]int64{100}, 17), 300, 300, 300)...)
	if got := reasons(spread, efficiencyBar{spread: 200, markRuns: 3}); !slices.Equal(got, []string{"efficiency:spread"}) {
		t.Errorf("a 3× spread gave %v", got)
	}
	if got := reasons(spread, efficiencyBar{spread: 400, markRuns: 3}); len(got) != 0 {
		t.Errorf("a 3× spread passed a team asking for 4×: %v", got)
	}

	marked := costs(slices.Repeat([]int64{100}, 20)...)
	for i := range 3 {
		marked[i].marks = []wasteMark{{markRepeated, "crm__lookup"}}
	}
	if got := reasons(marked, efficiencyBar{spread: 200, markRuns: 3}); !slices.Equal(got, []string{"efficiency:repeated_call:crm__lookup"}) {
		t.Errorf("a mark in 3 runs gave %v", got)
	}
	if got := reasons(marked, efficiencyBar{spread: 200, markRuns: 4}); len(got) != 0 {
		t.Errorf("a mark in 3 runs passed a team asking for 4: %v", got)
	}
}

// Each costly run is paired with the cheap run most like it: a shared digest
// part first, then tool use.
func TestCheapRunsArePairedByLikeness(t *testing.T) {
	costly := runCost{run: uuid.New(), tokens: 900, tools: []string{"crm__lookup", "mail__send"}}
	byTools := runCost{run: uuid.New(), tokens: 100, tools: []string{"crm__lookup", "mail__send"}}
	byDigest := runCost{run: uuid.New(), tokens: 120, tools: []string{"other"}}
	cheaper := runCost{run: uuid.New(), tokens: 50}
	parts := map[uuid.UUID][]string{costly.run: {"tool:crm"}, byDigest.run: {"tool:crm"}}
	got := pairRuns([]runCost{costly}, []runCost{cheaper, byTools, byDigest}, parts)
	if len(got) != 1 || got[0].run != byDigest.run {
		t.Errorf("paired %v, want the run sharing its digest part", got)
	}
	got = pairRuns([]runCost{costly}, []runCost{cheaper, byTools}, nil)
	if len(got) != 1 || got[0].run != byTools.run {
		t.Errorf("paired %v, want the run with the same tools", got)
	}
}

// goodRun is a judged good run of the agent that spent tokens, with steps
// written a second apart.
func (w *world) goodRun(t *testing.T, ago string, tokens int64, stepCount int) uuid.UUID {
	t.Helper()
	id := w.judged(t, w.agent, ago, verdictDone, tokens)
	w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload, created_at)
		VALUES ($1, 1, 'started', '{"tools":[]}', now() - $2::interval)`, id, ago)
	for i := range stepCount {
		w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload, created_at)
			VALUES ($1, $2, 'step', '{}', now() - $3::interval + make_interval(secs => $4))`, id, i+2, ago, float64(i+2))
	}
	return id
}

// Work time leaves out the time a run waited for a person's approval: a
// gap that ends in resumed.
func TestWorkTimeLeavesOutAnApprovalWait(t *testing.T) {
	w := newWorld(t)
	id := w.judged(t, w.agent, "1 hour", verdictDone, 100)
	for i, e := range []struct {
		kind string
		at   string
	}{
		{"started", "0 seconds"},
		{"step", "2 seconds"},
		{"waiting", "3 seconds"},
		{"resumed", "1 hour"},
		{"step", "1 hour 4 seconds"},
		{"finished", "1 hour 5 seconds"},
	} {
		w.exec(t, `INSERT INTO run_events (run_id, seq, kind, payload, created_at)
			VALUES ($1, $2, $3, '{}', '2026-11-01T00:00:00Z'::timestamptz + $4::interval)`, id, i+1, e.kind, e.at)
	}
	var ms int64
	w.scan(t, &ms, `SELECT run_work_ms($1)`, id)
	if ms != 8000 {
		t.Errorf("work time = %d ms, want 8000: the hour waiting for a person left out", ms)
	}
}

func (w *world) plans(t *testing.T, im *Improver) []plannedPass {
	t.Helper()
	j := NewImproverJob(im, w.store)
	plans, err := j.efficiencyPlans(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return plans
}

// spreadRuns makes n good runs, the last three costing three times the rest.
func (w *world) spreadRuns(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		tokens := int64(1000)
		if i >= n-3 {
			tokens = 3000
		}
		w.goodRun(t, fmt.Sprintf("%d minutes", 10+i), tokens, 1)
	}
}

// The sweep plans a pass only for an agent with enough good runs and a
// reason, in a team with the switch on, reading the team's own numbers.
func TestTheEfficiencySweepWaitsForEnoughGoodRuns(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	w.spreadRuns(t, 19)
	if got := w.plans(t, im); len(got) != 0 {
		t.Fatalf("with the switch off: %+v", got)
	}
	w.exec(t, `UPDATE team_settings SET improvement_efficiency = true WHERE team_id = $1`, w.team)
	if got := w.plans(t, im); len(got) != 0 {
		t.Fatalf("with 19 good runs: %+v", got)
	}
	// A bad run is no good run.
	w.judged(t, w.agent, "5 minutes", verdictNotDone, 100)
	if got := w.plans(t, im); len(got) != 0 {
		t.Fatalf("with 19 good runs and a bad one: %+v", got)
	}
	w.exec(t, `UPDATE team_settings SET improvement_efficiency_runs = 15 WHERE team_id = $1`, w.team)
	got := w.plans(t, im)
	if len(got) != 1 || got[0].Trigger != triggerEfficiency || !slices.Equal(got[0].Keys, []string{"efficiency:spread"}) {
		t.Fatalf("with 19 good runs and the team asking 15: %+v", got)
	}
	if *got[0].RunsSeen != 19 || len(got[0].RunIDs) != 2*costlyToRead {
		t.Errorf("runs seen %d, runs to read %d", *got[0].RunsSeen, len(got[0].RunIDs))
	}
	w.exec(t, `UPDATE team_settings SET improvement_efficiency = false WHERE team_id = $1`, w.team)
	if got := w.plans(t, im); len(got) != 0 {
		t.Fatalf("with the switch turned off again: %+v", got)
	}
	w.exec(t, `UPDATE team_settings SET improvement_efficiency = true WHERE team_id = $1`, w.team)
	// The window is the team's: runs older than it are not counted.
	w.exec(t, `UPDATE team_settings SET improvement_efficiency_days = 7 WHERE team_id = $1`, w.team)
	w.exec(t, `UPDATE runs SET created_at = now() - interval '8 days' WHERE agent_id = $1 AND created_at < now() - interval '20 minutes'`, w.agent)
	if got := w.plans(t, im); len(got) != 0 {
		t.Errorf("with most runs outside the team's 7 days: %+v", got)
	}
}

// No spread and no mark is no pass.
func TestEvenGoodRunsGetNoEfficiencyPass(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	w.exec(t, `UPDATE team_settings SET improvement_efficiency = true WHERE team_id = $1`, w.team)
	for i := range 20 {
		w.goodRun(t, fmt.Sprintf("%d minutes", 10+i), 1000, 1)
	}
	if got := w.plans(t, im); len(got) != 0 {
		t.Errorf("even runs: %+v", got)
	}
}

// Quality comes first: a failure pattern over the bar, a change waiting for
// a person, or one watched or flagged each holds the agent back.
func TestQualityComesBeforeAnEfficiencyPass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block func(t *testing.T, w *world)
	}{
		{"a pattern", func(t *testing.T, w *world) {
			for range patternBar {
				w.digested(t, skipped, "wrong", false)
			}
		}},
		{"a waiting change", func(t *testing.T, w *world) {
			w.exec(t, `INSERT INTO improvement_proposals (team_id, agent_id, kind, why, hypothesis, scope, before, after)
				VALUES ($1, $2, 'instructions', 'w', 'h', 'domain', '{}', '{}')`, w.team, w.agent)
		}},
		{"a watched change", func(t *testing.T, w *world) { w.watchedChange(t, []uuid.UUID{w.agent}, time.Hour) }},
		{"a flagged change", func(t *testing.T, w *world) {
			id := w.watchedChange(t, []uuid.UUID{w.agent}, time.Hour)
			w.exec(t, `UPDATE improvement_watches SET status = 'flagged', flag = 'worse', finished_at = now() WHERE proposal_id = $1`, id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			im := w.improver(t)
			w.exec(t, `UPDATE team_settings SET improvement_efficiency = true WHERE team_id = $1`, w.team)
			w.spreadRuns(t, 20)
			if got := w.plans(t, im); len(got) != 1 {
				t.Fatalf("before %s: %+v", tc.name, got)
			}
			tc.block(t, w)
			if got := w.plans(t, im); len(got) != 0 {
				t.Errorf("with %s: %+v", tc.name, got)
			}
		})
	}
}

// efficiencyPass begins an efficiency pass on the agent's runs.
func (w *world) efficiencyPass(t *testing.T, im *Improver) (db.ImproverPass, []uuid.UUID) {
	t.Helper()
	runs := []uuid.UUID{w.goodRun(t, "30 minutes", 9000, 4), w.goodRun(t, "20 minutes", 1000, 1)}
	seen := int32(2)
	pass := w.begin(t, im, plannedPass{
		TeamID: w.team, AgentID: w.agent, Trigger: triggerEfficiency,
		Keys: []string{"efficiency:spread"}, RunIDs: runs, RunsSeen: &seen,
	})
	return pass, runs
}

func efficiencyEdit(runs []uuid.UUID, kind string, edit any, saves ...string) map[string]any {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.String()
	}
	m := map[string]any{
		"kind": kind, "signature": "efficiency:spread", "runs": ids, "scope": "invariant",
		"why":        "The costly run looked the order up four times; the cheap one once.",
		"hypothesis": "Repeated lookups stop; tokens a run fall by about a fifth.",
		"edit":       edit,
	}
	if len(saves) > 0 {
		m["saves"] = saves
	}
	return m
}

// An efficiency pass reads each run with what it cost, costliest first.
func TestAnEfficiencyPassReadsWhatEachRunCost(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	pass, runs := w.efficiencyPass(t, im)
	m, err := im.material(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	costly := strings.Index(m.text, runs[0].String()+": 9000 tokens, 4 steps")
	cheap := strings.Index(m.text, runs[1].String()+": 1000 tokens, 1 steps")
	if costly < 0 || cheap < 0 || costly > cheap {
		t.Errorf("material does not list the runs costliest first with their cost:\n%s", m.text)
	}
	for _, want := range []string{"## Why this pass", "From 2 good runs", "efficiency:spread: the costliest good runs", "## Its tools in use"} {
		if !strings.Contains(m.text, want) {
			t.Errorf("material lacks %q", want)
		}
	}
	if !strings.Contains(systemFor(triggerEfficiency), "Correctness first") || strings.Contains(systemFor(triggerDaily), "Correctness first") {
		t.Error("the efficiency rules are not in its system prompt alone")
	}
}

// An efficiency pass proposes only what makes a run do less, says what it
// saves, and is stored with its goal.
func TestAnEfficiencyPassProposesOnlyWhatMakesARunDoLess(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	pass, runs := w.efficiencyPass(t, im)
	replace := map[string]string{"replace": "Look the order up before you answer.", "with": "Look the order up once, before you answer."}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"a server grant", efficiencyEdit(runs, "server_grant", map[string]any{"server": "crm"}, "cost"), "an efficiency change is only"},
		{"a description", efficiencyEdit(runs, "description", map[string]any{"text": "x"}, "cost"), "an efficiency change is only"},
		{"a raised limit", efficiencyEdit(runs, "limits", map[string]any{"max_steps": 9}, "cost"), "raises cost"},
		{"a widened delegation", efficiencyEdit(runs, "delegation", map[string]any{"mode": "team"}, "time"), "widens reach"},
		{"no saving named", efficiencyEdit(runs, "instructions", replace), "says what it saves"},
		{"a costlier loop", efficiencyEdit(runs, "pattern", map[string]any{"pattern": "plan"}, "cost"), "only to the react loop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := w.proposeWith(t, im, pass, tc.args, reply{text: screenOKText})
			if !strings.HasPrefix(out, "Not stored") || !strings.Contains(out, tc.want) {
				t.Errorf("propose = %q, want refused: %s", out, tc.want)
			}
		})
	}
	if got := w.proposals(t, "waiting"); len(got) != 0 {
		t.Fatalf("a refusal stored %d proposals", len(got))
	}

	out, _ := w.proposeWith(t, im, pass, efficiencyEdit(runs, "limits", map[string]any{"max_steps": 3}, "cost", "time"), reply{text: screenOKText})
	if !strings.HasPrefix(out, "Stored") {
		t.Fatalf("a lowered limit = %q", out)
	}
	got := w.proposals(t, "waiting")
	if len(got) != 1 || got[0].Goal != proposals.GoalEfficiency || !slices.Equal(got[0].Saves, []string{"cost", "time"}) {
		t.Errorf("stored %+v", got)
	}
}

// An efficiency pass counts against the team's passes a day: one past the
// cap is not begun, without an error.
func TestAnEfficiencyPassCountsAgainstTheDailyCap(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	w.exec(t, `UPDATE team_settings SET improvement_daily_passes = 1 WHERE team_id = $1`, w.team)
	pass, runs := w.efficiencyPass(t, im)
	if err := im.done(t.Context(), pass.ID, outcome{}); err != nil {
		t.Fatal(err)
	}
	seen := int32(2)
	_, err := im.Begin(t.Context(), plannedPass{
		TeamID: w.team, AgentID: w.agent, Trigger: triggerEfficiency,
		Keys: []string{"efficiency:spread"}, RunIDs: runs, RunsSeen: &seen,
	}, uuid.NewString())
	if !errors.Is(err, errNoPass) {
		t.Errorf("a pass past the day's cap: %v, want none begun", err)
	}
}

// A pass for what went wrong names no saving.
func TestAQualityPassNamesNoSaving(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	runs := []uuid.UUID{w.digested(t, skipped, "wrong", false)}
	pass := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily})
	args := instructionsEdit(runs[0])
	args["saves"] = []string{"cost"}
	if out, _ := w.proposeWith(t, im, pass, args, reply{text: screenOKText}); !strings.Contains(out, "saves is for an efficiency pass") {
		t.Errorf("propose = %q", out)
	}
	delete(args, "saves")
	if out, _ := w.proposeWith(t, im, pass, args, reply{text: screenOKText}); !strings.HasPrefix(out, "Stored") {
		t.Fatalf("propose = %q", out)
	}
	if got := w.proposals(t, "waiting"); len(got) != 1 || got[0].Goal != proposals.GoalQuality || len(got[0].Saves) != 0 {
		t.Errorf("stored %+v", got)
	}
}

// runsOf is a window of n counted runs, the first bad of them bad, each good
// one spending tokens and working ms.
func runsOf(n, bad int, tokens, ms int64) window {
	var rs []counted
	for i := range n {
		rs = append(rs, counted{bad: i < bad, spent: tokens, workMS: ms})
	}
	return windowOf(rs)
}

// An efficiency change is flagged when the saving it named does not show in
// its good runs, judged on tokens for cost and work time for time; a
// quality change never is.
func TestAnEfficiencyChangeThatSavedNothingIsFlagged(t *testing.T) {
	baseline := runsOf(50, 5, 1000, 4000)
	before := runsOf(10, 1, 1000, 4000)
	cost := efficiencyRule{on: true, margin: 1, cost: true}
	quicker := efficiencyRule{on: true, margin: 1, time: true}

	if v := judgeWatch(baseline, before, runsOf(10, 1, 800, 4000), cost); v.status != watchClear {
		t.Errorf("cheaper good runs: %+v", v)
	}
	if v := judgeWatch(baseline, before, runsOf(10, 1, 1000, 3000), cost); v.flag != flagNoSaving {
		t.Errorf("a cost change whose good runs cost the same: %+v", v)
	}
	if v := judgeWatch(baseline, before, runsOf(10, 1, 1000, 3000), quicker); v.status != watchClear {
		t.Errorf("a time change whose good runs got quicker: %+v", v)
	}
	if v := judgeWatch(baseline, before, runsOf(10, 1, 800, 4000), quicker); v.flag != flagNoSaving {
		t.Errorf("a time change judged on tokens: %+v", v)
	}
	if v := judgeWatch(baseline, before, runsOf(10, 1, 1000, 4000), efficiencyRule{}); v.status != watchClear {
		t.Errorf("a quality change was held to a saving: %+v", v)
	}
	// Runs that failed early and cheap are no saving: only good runs count.
	failedCheap := windowOf(append(slices.Repeat([]counted{{spent: 1000, workMS: 4000}}, 5),
		slices.Repeat([]counted{{bad: true}}, 5)...))
	if cost.saved(before, failedCheap) || quicker.saved(before, failedCheap) {
		t.Error("failures that spent nothing counted as a saving")
	}
}

// An efficiency change is flagged worse at the team's margin below the bar,
// never below one bad run; margin 0 is the usual bar.
func TestAnEfficiencyChangeIsHeldToAStricterBar(t *testing.T) {
	baseline := runsOf(50, 5, 1000, 4000)
	before := runsOf(10, 1, 1000, 4000)
	k, _ := bar(5, 50)
	saved := func(margin int) efficiencyRule { return efficiencyRule{on: true, margin: margin, cost: true} }
	for _, tc := range []struct {
		rule efficiencyRule
		at   int
	}{
		{efficiencyRule{}, k}, {saved(0), k}, {saved(1), k - 1}, {saved(3), max(k-3, 1)},
	} {
		if v := judgeWatch(baseline, before, runsOf(10, tc.at, 500, 4000), tc.rule); v.flag != flagWorse {
			t.Errorf("margin %d at %d bad: %+v, want worse", tc.rule.margin, tc.at, v)
		}
		if v := judgeWatch(baseline, before, runsOf(10, tc.at-1, 500, 4000), tc.rule); v.flag == flagWorse {
			t.Errorf("margin %d at %d bad: flagged worse", tc.rule.margin, tc.at-1)
		}
	}
	if got := saved(3).flagAt(2); got != 1 {
		t.Errorf("flagAt(2) with margin 3 = %d, want 1", got)
	}
}

// A failed call made again is a retry, and not also a repeat; made a third
// time after succeeding, it is a repeat.
func TestARetryIsNotARepeat(t *testing.T) {
	root := uuid.New()
	retry := []db.ListEfficiencyEventsRow{
		callEv(root, "1", "crm__lookup", "a"), resultEv(root, "1", "crm__lookup", true, 10),
		callEv(root, "2", "crm__lookup", "a"), resultEv(root, "2", "crm__lookup", false, 10),
	}
	if got := markKeys(runCostOf(root, retry, 0)); !slices.Equal(got, []string{"efficiency:retried_error:crm__lookup"}) {
		t.Errorf("a retry = %v", got)
	}
	again := append(slices.Clone(retry), callEv(root, "3", "crm__lookup", "a"))
	if got := markKeys(runCostOf(root, again, 0)); !slices.Contains(got, "efficiency:repeated_call:crm__lookup") {
		t.Errorf("the same call after it succeeded = %v", got)
	}
}

// Each reason starts at its bar and not one below: a spread of exactly the
// team's multiple, a mark in exactly its runs.
func TestReasonsStartAtTheirBar(t *testing.T) {
	// 20 runs: the 90th percentile is the 18th; the median the 10th.
	at := costs(append(slices.Repeat([]int64{100}, 17), 200, 200, 200)...)
	below := costs(append(slices.Repeat([]int64{100}, 17), 199, 199, 199)...)
	if got := reasons(at, efficiencyBar{spread: 200, markRuns: 3}); !slices.Equal(got, []string{"efficiency:spread"}) {
		t.Errorf("exactly 2× = %v", got)
	}
	if got := reasons(below, efficiencyBar{spread: 200, markRuns: 3}); len(got) != 0 {
		t.Errorf("1.99× = %v", got)
	}
	marked := costs(slices.Repeat([]int64{100}, 20)...)
	for i := range 2 {
		marked[i].marks = []wasteMark{{name: markUnused}}
	}
	if got := reasons(marked, efficiencyBar{spread: 200, markRuns: 3}); len(got) != 0 {
		t.Errorf("a mark in 2 of 3 runs = %v", got)
	}
	marked[2].marks = []wasteMark{{name: markUnused}}
	if got := reasons(marked, efficiencyBar{spread: 200, markRuns: 3}); !slices.Equal(got, []string{"efficiency:unused_tools"}) {
		t.Errorf("a mark in 3 of 3 runs = %v", got)
	}
}

// A saving must show: equal medians are none, and a change naming cost and
// time needs both to fall.
func TestASavingMustShowInFull(t *testing.T) {
	before := runsOf(10, 0, 1000, 4000)
	both := efficiencyRule{on: true, cost: true, time: true}
	if both.saved(before, runsOf(10, 0, 1000, 3000)) {
		t.Error("equal tokens counted as a cost saving")
	}
	if both.saved(before, runsOf(10, 0, 900, 4000)) {
		t.Error("a change naming cost and time saved on cost alone")
	}
	if !both.saved(before, runsOf(10, 0, 900, 3000)) {
		t.Error("cheaper and quicker runs were not a saving")
	}
}
