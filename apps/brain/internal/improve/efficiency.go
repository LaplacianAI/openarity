package improve

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The efficiency pass: the improver looking at an agent whose runs go well
// but whose costliest spend far more than the rest. How many runs, over how
// many days, how wide a spread and how many runs a waste mark needs are each
// team's settings; what a waste mark is stays fixed, as it says what a run
// shows rather than how much a team wants. The dashboard's Team settings
// say these thresholds in words (apps/dashboard/src/lib/efficiency-settings.ts):
// change both together.
const (
	triggerEfficiency = "efficiency"
	efficiencyPrefix  = "efficiency:"

	// costlyToRead and cheapToRead are how many of the costliest good runs
	// a pass reads, and how many cheap ones on requests like them.
	costlyToRead = 5
	cheapToRead  = 5
	// cheapPool is how many of the cheapest good runs the cheap ones are
	// chosen from.
	cheapPool = 50
	// efficiencyRunsRead is the most judged runs a sweep reads for one
	// agent: a few hundred a week is usual.
	efficiencyRunsRead = 1000

	// What makes a waste mark.
	unusedOffered = 20
	unusedCalled  = 2
	nearCapShare  = 80 // percent of the agent's max_steps
	longToolWait  = 30 * time.Second
)

// The waste marks, and the reason that is a spread alone.
const (
	markRepeated = "repeated_call"
	markRetried  = "retried_error"
	markUnused   = "unused_tools"
	markNearCap  = "near_step_cap"
	markLongWait = "long_tool_wait"
	reasonSpread = "spread"
)

// What each reason means, as a pass is told it.
var reasonMeaning = map[string]string{
	markRepeated: "the same tool called with the same arguments twice or more in one run",
	markRetried:  "a tool call that failed, made again the same way",
	markUnused:   fmt.Sprintf("%d or more tools offered and at most %d called", unusedOffered, unusedCalled),
	markNearCap:  fmt.Sprintf("a good run that used %d%% or more of its steps", nearCapShare),
	markLongWait: fmt.Sprintf("one tool result that took over %d seconds", int(longToolWait.Seconds())),
	reasonSpread: "the costliest good runs cost far more than the usual one",
}

// wasteMark is a mark a run's events show, on a tool when it names one.
type wasteMark struct{ name, target string }

// key is the mark as a pass's signature: efficiency:<mark>[:<target>].
func (m wasteMark) key() string {
	if m.target == "" {
		return efficiencyPrefix + m.name
	}
	return efficiencyPrefix + m.name + ":" + m.target
}

func (m wasteMark) String() string {
	if m.target == "" {
		return m.name
	}
	return m.name + " " + m.target
}

// costRow is a judged run and what its tree spent, as a sweep or a pass
// reads it.
type costRow struct {
	id       uuid.UUID
	at       time.Time
	verdict  string
	up, down int32
	spent    int64
	workMS   int64
}

func (r costRow) good() bool { return Effective(r.verdict, "", r.up, r.down) == Good }

// runCost is what one good run cost and did, from its events.
type runCost struct {
	run            uuid.UUID
	at             time.Time
	tokens, workMS int64
	toolMS         int64
	steps          int
	// offered and called are the tools the run itself started with and
	// called; tools is every tool its tree called, for likeness.
	offered, called int
	tools           []string
	marks           []wasteMark
}

// measure reads each good run's events into what it cost and did. maxSteps
// is the agent's, for near_step_cap.
func measure(rows []costRow, events []db.ListEfficiencyEventsRow, maxSteps int32) []runCost {
	byRoot := map[uuid.UUID][]db.ListEfficiencyEventsRow{}
	for _, e := range events {
		byRoot[e.RootID] = append(byRoot[e.RootID], e)
	}
	out := make([]runCost, 0, len(rows))
	for _, r := range rows {
		c := runCostOf(r.id, byRoot[r.id], maxSteps)
		c.at, c.tokens, c.workMS = r.at, r.spent, r.workMS
		out = append(out, c)
	}
	return out
}

// runCostOf is one run's tree's events, in the order written, read into
// what it did. A call is the same as another when its tool and arguments
// are, in the same run of the tree.
func runCostOf(root uuid.UUID, events []db.ListEfficiencyEventsRow, maxSteps int32) runCost {
	c := runCost{run: root}
	type call struct {
		run       uuid.UUID
		name, arg string
	}
	type callID struct {
		run uuid.UUID
		id  string
	}
	made := map[call]int{}
	failed := map[call]bool{}
	byID := map[callID]call{}
	calledRoot, calledTree := map[string]bool{}, map[string]bool{}
	marks := map[wasteMark]bool{}
	rootSteps := 0
	for _, e := range events {
		switch e.Kind {
		case "step":
			c.steps++
			if e.RunID == root {
				rootSteps++
			}
		case "started":
			if e.RunID == root {
				c.offered = int(e.Tools)
			}
		case "tool_call":
			k := call{e.RunID, e.Name, e.ArgsHash}
			// A call made again after it failed is a retry, not a repeat:
			// each is its own mark, and one call is counted once.
			if failed[k] {
				marks[wasteMark{markRetried, e.Name}] = true
				delete(failed, k)
			} else if made[k]++; made[k] == 2 {
				marks[wasteMark{markRepeated, e.Name}] = true
			}
			byID[callID{e.RunID, e.CallID}] = k
			calledTree[e.Name] = true
			if e.RunID == root {
				calledRoot[e.Name] = true
			}
		case "tool_result":
			c.toolMS += e.DurationMs
			if time.Duration(e.DurationMs)*time.Millisecond > longToolWait {
				marks[wasteMark{markLongWait, e.Name}] = true
			}
			if k, ok := byID[callID{e.RunID, e.CallID}]; ok && e.Failed {
				failed[k] = true
			}
		}
	}
	c.called = len(calledRoot)
	if c.offered >= unusedOffered && c.called <= unusedCalled {
		marks[wasteMark{name: markUnused}] = true
	}
	if maxSteps > 0 && rootSteps*100 >= nearCapShare*int(maxSteps) {
		marks[wasteMark{name: markNearCap}] = true
	}
	for t := range calledTree {
		c.tools = append(c.tools, t)
	}
	slices.Sort(c.tools)
	for m := range marks {
		c.marks = append(c.marks, m)
	}
	slices.SortFunc(c.marks, func(a, b wasteMark) int { return strings.Compare(a.key(), b.key()) })
	return c
}

// efficiencyBar is a team's numbers for the sweep.
type efficiencyBar struct {
	spread, markRuns int
}

// reasons are why an agent's good runs are worth a look, as signatures:
// each waste mark in at least markRuns of them, most runs first, then the
// spread when the 90th percentile of tokens or work time is at least spread
// percent of its median. At most keysPerPass, the spread always kept.
func reasons(costs []runCost, bar efficiencyBar) []string {
	count := map[string]int{}
	for _, c := range costs {
		for _, m := range c.marks {
			count[m.key()]++
		}
	}
	var keys []string
	for k, n := range count {
		if n >= bar.markRuns {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(count[b], count[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	spread := spreads(costs, bar.spread)
	room := keysPerPass
	if spread {
		room--
	}
	keys = keys[:min(len(keys), room)]
	if spread {
		keys = append(keys, efficiencyPrefix+reasonSpread)
	}
	return keys
}

// spreads is whether the 90th percentile of tokens, or of work time, is at
// least pct percent of its median.
func spreads(costs []runCost, pct int) bool {
	var tokens, work []int64
	for _, c := range costs {
		tokens, work = append(tokens, c.tokens), append(work, c.workMS)
	}
	wide := func(xs []int64) bool {
		m, ok := median(xs)
		p90, _ := percentile(xs, 90)
		return ok && m > 0 && p90*100 >= int64(pct)*m
	}
	return wide(tokens) || wide(work)
}

// costlier orders runs costliest first: tokens, then work time, then the
// newest.
func costlier(a, b runCost) int {
	if c := cmp.Compare(b.tokens, a.tokens); c != 0 {
		return c
	}
	if c := cmp.Compare(b.workMS, a.workMS); c != 0 {
		return c
	}
	return b.at.Compare(a.at)
}

// likeness is how alike two runs' requests look: a lead part and target the
// digests of both share, else how much their tool use overlaps.
func likeness(a, b runCost, parts map[uuid.UUID][]string) float64 {
	overlap := jaccard(a.tools, b.tools)
	for _, p := range parts[a.run] {
		if slices.Contains(parts[b.run], p) {
			return 2 + overlap
		}
	}
	return overlap
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	both := 0
	for _, x := range a {
		if slices.Contains(b, x) {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}

// splitRuns sorts the good runs into the costliest a pass reads, at most
// half of them so cheap ones are left to pair, and the cheapest the cheap
// ones are chosen from, cheapest first.
func splitRuns(costs []runCost) (costly, pool []runCost) {
	s := slices.Clone(costs)
	slices.SortFunc(s, costlier)
	n := min(costlyToRead, len(s)/2)
	pool = slices.Clone(s[n:])
	slices.Reverse(pool)
	return s[:n], pool[:min(len(pool), cheapPool)]
}

// pairRuns chooses, for each costly run, the most alike of the pool, the
// cheaper on a tie, each run once.
func pairRuns(costly, pool []runCost, parts map[uuid.UUID][]string) []runCost {
	var cheap []runCost
	taken := map[uuid.UUID]bool{}
	for _, c := range costly {
		best, score := -1, -1.0
		for i, p := range pool {
			if taken[p.run] {
				continue
			}
			if l := likeness(c, p, parts); l > score {
				best, score = i, l
			}
		}
		if best >= 0 && len(cheap) < cheapToRead {
			taken[pool[best].run] = true
			cheap = append(cheap, pool[best])
		}
	}
	return cheap
}

// digestParts is the part:target of each run's digests, for likeness.
func (im *Improver) digestParts(ctx context.Context, runs []runCost) (map[uuid.UUID][]string, error) {
	parts := map[uuid.UUID][]string{}
	for _, r := range runs {
		ds, err := im.store.ListRunDigests(ctx, r.run)
		if err != nil {
			return nil, fmt.Errorf("reading run %s's digests: %w", r.run, err)
		}
		for _, d := range ds {
			parts[r.run] = append(parts[r.run], d.Part+":"+d.Target)
		}
	}
	return parts, nil
}

// costsOf is what each of rows cost and did, read from its events. The
// caller chooses the rows: a sweep its good runs, a pass the ones it was given.
func (im *Improver) costsOf(ctx context.Context, rows []costRow, maxSteps int32) ([]runCost, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	events, err := im.store.ListEfficiencyEvents(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading what the runs did: %w", err)
	}
	return measure(rows, events, maxSteps), nil
}

// efficiencyPlan is the pass an agent's good runs call for, when they do:
// enough of them in the team's window, and a reason to look.
func (im *Improver) efficiencyPlan(ctx context.Context, a db.ListEfficiencyAgentsRow) (plannedPass, bool, error) {
	rows, err := im.store.ListEfficiencyRuns(ctx, db.ListEfficiencyRunsParams{
		TeamID: a.TeamID, AgentID: &a.ID, Days: int32(a.Days), PageSize: efficiencyRunsRead,
	})
	if err != nil {
		return plannedPass{}, false, fmt.Errorf("reading agent %s's runs: %w", a.ID, err)
	}
	cr := make([]costRow, len(rows))
	for i, r := range rows {
		cr[i] = costRow{id: r.ID, at: r.CreatedAt, verdict: r.Verdict, up: r.Up, down: r.Down, spent: r.Spent, workMS: r.WorkMs}
	}
	good := slices.DeleteFunc(cr, func(r costRow) bool { return !r.good() })
	if len(good) < int(a.MinRuns) {
		return plannedPass{}, false, nil
	}
	costs, err := im.costsOf(ctx, good, a.MaxSteps)
	if err != nil {
		return plannedPass{}, false, err
	}
	keys := reasons(costs, efficiencyBar{spread: int(a.Spread), markRuns: int(a.MarkRuns)})
	if len(keys) == 0 {
		return plannedPass{}, false, nil
	}
	costly, pool := splitRuns(costs)
	parts, err := im.digestParts(ctx, append(slices.Clone(costly), pool...))
	if err != nil {
		return plannedPass{}, false, err
	}
	cheap := pairRuns(costly, pool, parts)
	var ids []uuid.UUID
	for _, r := range append(costly, cheap...) {
		ids = append(ids, r.run)
	}
	seen := int32(len(costs)) //nolint:gosec // G115: at most efficiencyRunsRead
	return plannedPass{TeamID: a.TeamID, AgentID: a.ID, Trigger: triggerEfficiency, Keys: keys, RunIDs: ids, RunsSeen: &seen}, true, nil
}

// writeEfficiency is what an efficiency pass reads beside the agent: why it
// was started, and each run it may read with what it cost, costliest first.
func (im *Improver) writeEfficiency(ctx context.Context, pass db.ImproverPass, m *material, write writeFn) error {
	var why []string
	if pass.RunsSeen != nil {
		why = append(why, fmt.Sprintf("From %d good runs in the team's window.", *pass.RunsSeen))
	}
	for _, k := range pass.Signatures {
		name, target, _ := strings.Cut(strings.TrimPrefix(k, efficiencyPrefix), ":")
		line := k + ": " + reasonMeaning[name]
		if target != "" {
			line += ", on " + target
		}
		why = append(why, line)
	}
	write("Why this pass", why...)

	rows, err := im.store.ListRunsCost(ctx, pass.RunIds)
	if err != nil {
		return fmt.Errorf("reading the cost of pass %s's runs: %w", pass.ID, err)
	}
	// Every run it was given, chosen as good when it was planned, whatever
	// a later vote says.
	cr := make([]costRow, len(rows))
	for i, r := range rows {
		cr[i] = costRow{id: r.ID, at: r.CreatedAt, verdict: r.Verdict, up: r.Up, down: r.Down, spent: r.Spent, workMS: r.WorkMs}
	}
	costs, err := im.costsOf(ctx, cr, m.state.Agent.MaxSteps)
	if err != nil {
		return err
	}
	slices.SortFunc(costs, costlier)
	var lines []string
	for _, c := range costs {
		lines = append(lines, costLine(c))
	}
	write("What each run cost, costliest first", lines...)

	use, err := im.toolUse(ctx, pass.AgentID)
	if err != nil {
		return err
	}
	write("Its tools in use", use)
	return nil
}

// costLine is one run's cost as a pass reads it.
func costLine(c runCost) string {
	marks := "none"
	if len(c.marks) > 0 {
		var ms []string
		for _, m := range c.marks {
			ms = append(ms, m.String())
		}
		marks = strings.Join(ms, ", ")
	}
	return fmt.Sprintf("%s: %d tokens, %d steps, work %s (tools %s), %d tools offered, %d called; waste marks: %s",
		c.run, c.tokens, c.steps, seconds(c.workMS), seconds(c.toolMS), c.offered, c.called, marks)
}

func seconds(ms int64) string {
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

// toolUse is how many tools the agent's last runs were offered, and how
// many of those they called.
func (im *Improver) toolUse(ctx context.Context, agentID uuid.UUID) (string, error) {
	runs, err := im.store.ListAgentRecentRuns(ctx, db.ListAgentRecentRunsParams{AgentID: &agentID, PageSize: pruneWindow})
	if err != nil {
		return "", fmt.Errorf("reading agent %s's runs: %w", agentID, err)
	}
	use, err := im.store.ListRunsUse(ctx, db.ListRunsUseParams{RunIds: runs, NamingTools: []string{callTool}})
	if err != nil {
		return "", fmt.Errorf("reading agent %s's use of its tools: %w", agentID, err)
	}
	offered, called := map[string]bool{}, map[string]bool{}
	for _, u := range use {
		switch u.Kind {
		case "started":
			for _, t := range u.Tools {
				offered[t] = true
			}
		case "tool_call":
			called[u.Name] = true
		}
	}
	n := 0
	for t := range offered {
		if called[t] {
			n++
		}
	}
	return fmt.Sprintf("its last %d runs were offered %d tools and called %d of them", len(runs), len(offered), n), nil
}
