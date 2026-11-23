package improve

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The bar from the design's table: a noisy agent needs a larger rise to be
// flagged, a steady one a smaller.
func TestTheBarIsTheAgentsOwnNoise(t *testing.T) {
	for _, tc := range []struct{ bad, runs, want int }{
		{15, 50, 7},
		{5, 50, 4},
		{0, 20, 3},
		{25, 50, 9},
		{50, 50, 11}, // every run bad: no count of ten is rare enough
	} {
		got, ok := bar(tc.bad, tc.runs)
		if !ok || got != tc.want {
			t.Errorf("bar(%d of %d) = %d, %v; want %d", tc.bad, tc.runs, got, ok, tc.want)
		}
	}
	if _, ok := bar(0, baselineLeast-1); ok {
		t.Error("a baseline under ten runs gave a bar")
	}
}

func TestPercentileIsNearestRank(t *testing.T) {
	xs := []int64{50, 10, 40, 20, 30, 60, 70, 80, 90, 100}
	if m, _ := median(xs); m != 50 {
		t.Errorf("median = %d, want 50", m)
	}
	if p, _ := percentile(xs, 90); p != 90 {
		t.Errorf("p90 = %d, want 90", p)
	}
	if _, ok := median(nil); ok {
		t.Error("the median of nothing was ok")
	}
}

func spends(n int, each int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = each
	}
	return out
}

func TestAWatchsVerdict(t *testing.T) {
	baseline := window{runs: 50, bad: 15, spent: spends(50, 1000)}
	before := window{runs: 10, bad: 3, spent: spends(10, 1000)}
	for name, tc := range map[string]struct {
		after window
		want  verdict
	}{
		"within the bar":           {window{runs: 10, bad: 6, spent: spends(10, 1000)}, verdict{status: watchClear}},
		"at the bar":               {window{runs: 10, bad: 7, spent: spends(10, 1000)}, verdict{watchFlagged, flagWorse}},
		"costlier, no fewer bad":   {window{runs: 10, bad: 3, spent: spends(10, 2000)}, verdict{watchFlagged, flagCostlier}},
		"costlier, fewer bad":      {window{runs: 10, bad: 2, spent: spends(10, 2000)}, verdict{status: watchClear}},
		"worse and costlier":       {window{runs: 10, bad: 8, spent: spends(10, 2000)}, verdict{watchFlagged, flagBoth}},
		"costlier at the p90 only": {window{runs: 10, bad: 3, spent: spends(10, 1000)}, verdict{status: watchClear}},
	} {
		if got := judgeWatch(baseline, before, tc.after, efficiencyRule{}); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	// Too little history to judge against: nothing is flagged.
	little := window{runs: 5, bad: 0, spent: spends(5, 10)}
	if got := judgeWatch(little, little, window{runs: 10, bad: 10, spent: spends(10, 99999)}, efficiencyRule{}); got.status != watchClear {
		t.Errorf("with five baseline runs: %+v, want clear", got)
	}
}

// judged is a finished top-level run of agent, created ago, judged verdict,
// that spent tokens; it returns its id.
func (w *world) judged(t *testing.T, agent uuid.UUID, ago, verdict string, tokens int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	w.exec(t, `INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, input, output, status,
		created_at, started_at, finished_at, input_tokens)
		VALUES ($1, $2, $3, $1, $4, 'agent', 'q', 'a', 'succeeded', now() - $5::interval, now() - $5::interval,
		now() - $5::interval + interval '1 minute', $6)`, id, w.team, w.session(t), agent, ago, tokens)
	w.exec(t, `INSERT INTO run_outcomes (run_id, team_id, verdict, model, sampled, digest) VALUES ($1, $2, $3, 'm', false, 'skipped')`,
		id, w.team, verdict)
	return id
}

// several makes n judged runs of agent, the first bad of them bad, created
// at minutes before from, a minute apart.
func (w *world) several(t *testing.T, agent uuid.UUID, n, bad int, from time.Duration, tokens int64) {
	t.Helper()
	for i := range n {
		v := verdictDone
		if i < bad {
			v = verdictNotDone
		}
		w.judged(t, agent, fmt.Sprintf("%d seconds", int((from+time.Duration(i)*time.Minute).Seconds())), v, tokens)
	}
}

// watchedChange is an applied instructions change to agent, watched from
// applied ago.
func (w *world) watchedChange(t *testing.T, agents []uuid.UUID, applied time.Duration) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	w.scan(t, &id, `INSERT INTO improvement_proposals (team_id, agent_id, kind, status, why, hypothesis, scope, signature,
		before, after, decided_at)
		VALUES ($1, $2, 'instructions', 'applied', 'w', 'h', 'domain', 'instructions::skipped_step', '{}', '{}',
		now() - $3::interval) RETURNING id`, w.team, agents[0], fmt.Sprintf("%d seconds", int(applied.Seconds())))
	w.exec(t, `INSERT INTO improvement_watches (proposal_id, team_id, agent_ids, started_at)
		VALUES ($1, $2, $3, now() - $4::interval)`, id, w.team, agents, fmt.Sprintf("%d seconds", int(applied.Seconds())))
	return id
}

func (w *world) watch(t *testing.T, id uuid.UUID) db.ImprovementWatch {
	t.Helper()
	got, err := w.store.GetImprovementWatch(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A change whose runs went clearly worse is flagged, with its counts, once;
// a second sweep writes nothing more.
func TestAChangeThatWentWorseIsFlaggedOnce(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	id := w.watchedChange(t, []uuid.UUID{w.agent}, 24*time.Hour)
	w.several(t, w.agent, 20, 2, 30*time.Hour, 1000) // before: 2 bad in 20
	w.several(t, w.agent, 10, 7, time.Hour, 1000)    // after: 7 bad in 10

	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := w.watch(t, id)
	if got.Status != watchFlagged || got.Flag == nil || *got.Flag != flagWorse || got.FinishedAt == nil ||
		got.BaselineRuns != 20 || got.BaselineBad != 2 || got.BeforeRuns != 10 || got.AfterRuns != 10 || got.AfterBad != 7 ||
		got.Bar == nil || *got.Bar > 7 || got.SigBefore == nil {
		t.Fatalf("watch = %+v", got)
	}
	// Its first sweep counted the day since it was applied.
	if got.OnSeconds < int32((23 * time.Hour).Seconds()) {
		t.Errorf("on seconds = %d, want the day since it was applied", got.OnSeconds)
	}

	w.exec(t, `UPDATE improvement_watches SET status = 'watching', flag = NULL, finished_at = NULL WHERE proposal_id = $1`, id)
	n, err := w.store.FinishImprovementWatch(t.Context(), db.FinishImprovementWatchParams{ProposalID: id, Status: watchClear})
	if err != nil || n != 1 {
		t.Fatalf("finish = %d, %v", n, err)
	}
	if n, _ := w.store.FinishImprovementWatch(t.Context(), db.FinishImprovementWatchParams{ProposalID: id, Status: watchClear}); n != 0 {
		t.Errorf("a finished watch was finished again: %d rows", n)
	}
}

// Within the agent's usual spread a change is clear; runs nobody could judge
// are not counted, so the watch waits for ten that were.
func TestAChangeWithinTheBarIsClearAndCantTellIsSkipped(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	id := w.watchedChange(t, []uuid.UUID{w.agent}, 24*time.Hour)
	w.several(t, w.agent, 20, 4, 30*time.Hour, 1000)
	w.several(t, w.agent, 9, 3, 2*time.Hour, 1000)
	for i := range 5 {
		w.judged(t, w.agent, fmt.Sprintf("%d minutes", 50+i), verdictCantTell, 1000)
	}
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.Status != watchWatching || got.AfterRuns != 9 {
		t.Fatalf("with nine counted runs: %+v", got)
	}
	w.judged(t, w.agent, "10 minutes", verdictDone, 1000)
	w.later(t, id)
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.Status != watchClear || got.AfterRuns != 10 || got.AfterBad != 3 {
		t.Errorf("watch = %+v, want clear with 3 of 10", got)
	}
}

// A costlier change with no fewer failures is flagged for cost.
func TestACostlierChangeIsFlagged(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	id := w.watchedChange(t, []uuid.UUID{w.agent}, 24*time.Hour)
	w.several(t, w.agent, 20, 2, 30*time.Hour, 1000)
	w.several(t, w.agent, 10, 2, time.Hour, 5000)
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := w.watch(t, id)
	if got.Status != watchFlagged || got.Flag == nil || *got.Flag != flagCostlier ||
		got.AfterTokens == nil || *got.AfterTokens != 5000 || got.BaselineP90 == nil || *got.BaselineP90 != 1000 {
		t.Errorf("watch = %+v", got)
	}
}

// A run created before the change, though it finished after, ran on the old
// harness and counts before it.
func TestARunStartedBeforeTheChangeCountsBefore(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	id := w.watchedChange(t, []uuid.UUID{w.agent}, time.Hour)
	w.judged(t, w.agent, "61 minutes", verdictNotDone, 1000) // created a minute before, finished after
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.BaselineRuns != 1 || got.AfterRuns != 0 {
		t.Errorf("watch = %+v, want the run before the change", got)
	}
}

// A change to an agent that works only as a delegate is watched through the
// top-level runs that delegated to it.
func TestADelegateIsWatchedThroughItsRoots(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	var delegate uuid.UUID
	w.scan(t, &delegate, `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'helper', 'test/model', 1024, 'react', 5) RETURNING id`, w.team)
	id := w.watchedChange(t, []uuid.UUID{delegate}, time.Hour)
	root := w.judged(t, w.agent, "30 minutes", verdictNotDone, 1000)
	w.exec(t, `INSERT INTO runs (id, team_id, session_id, parent_run_id, root_run_id, depth, parent_tool_call, agent_id, agent_name,
		input, status, started_at, finished_at) SELECT gen_random_uuid(), team_id, session_id, id, id, 1, 'call_1', $2, 'helper', 'q', 'succeeded', started_at, finished_at FROM runs WHERE id = $1`,
		root, delegate)
	w.judged(t, w.agent, "20 minutes", verdictNotDone, 1000) // the agent alone: not the delegate's
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.AfterRuns != 1 || got.AfterBad != 1 {
		t.Errorf("watch = %+v, want the one run that delegated", got)
	}
}

// A watch counts the time since its last sweep toward its 30 days, only
// while its team has improvement on: time with it off is not counted, and
// a watch without its runs ends once 30 days on have passed.
func TestAWatchWithoutItsRunsEnds(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	id := w.watchedChange(t, []uuid.UUID{w.agent}, 40*24*time.Hour)
	near := int32((watchFor - 20*time.Minute).Seconds())
	w.exec(t, `UPDATE improvement_watches SET on_seconds = $2, last_swept_at = now() - interval '2 days' WHERE proposal_id = $1`, id, near)

	// Two days with improvement off: swept, not counted.
	w.exec(t, `UPDATE team_settings SET improvement = false WHERE team_id = $1`, w.team)
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.Status != watchWatching || got.OnSeconds != near || got.LastSweptAt == nil {
		t.Fatalf("with improvement off: %+v", got)
	}
	w.exec(t, `UPDATE team_settings SET improvement = true WHERE team_id = $1`, w.team)
	w.later(t, id)
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.Status != watchWatching || got.OnSeconds > near+int32((11*time.Minute).Seconds())+5 {
		t.Fatalf("the days off were counted: %+v", got)
	}

	// Half an hour on since the last sweep crosses the 30 days.
	w.exec(t, `UPDATE improvement_watches SET last_swept_at = now() - interval '30 minutes' WHERE proposal_id = $1`, id)
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.Status != watchEnded || got.EndedWhy == nil || *got.EndedWhy != endedTooFew {
		t.Errorf("after 30 days on: %+v", got)
	}
}

// Every watch is reached in turn: a page of older watches in a team with
// improvement off does not keep a newer one from being counted.
func TestEveryWatchIsReachedInTurn(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	var other, otherAgent uuid.UUID
	w.scan(t, &other, `INSERT INTO teams (name) VALUES ('quiet') RETURNING id`)
	w.exec(t, `INSERT INTO team_settings (team_id, improvement) VALUES ($1, false)`, other)
	w.scan(t, &otherAgent, `INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'idle', 'm', 100, 'react', 3) RETURNING id`, other)
	w.exec(t, `WITH p AS (
		INSERT INTO improvement_proposals (team_id, agent_id, kind, status, why, hypothesis, scope, before, after, decided_at)
		SELECT $1, $2, 'instructions', 'applied', 'w', 'h', 'domain', '{}', '{}', now() - interval '20 days'
		FROM generate_series(1, $3) RETURNING id)
		INSERT INTO improvement_watches (proposal_id, team_id, agent_ids, started_at)
		SELECT id, $1, ARRAY[$2]::uuid[], now() - interval '20 days' FROM p`, other, otherAgent, watchPage+5)

	id := w.watchedChange(t, []uuid.UUID{w.agent}, time.Hour)
	w.several(t, w.agent, 3, 1, 30*time.Minute, 1000)
	pages := &countingTicks{WatchStore: w.store}
	watcher.store = pages
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.AfterRuns != 3 || got.LastSweptAt == nil {
		t.Errorf("the newer watch was not reached: %+v", got)
	}
	// Once round them all, a sweep stops: two pages for 106 watches, not 50.
	if pages.n != 2 {
		t.Errorf("one sweep read %d pages, want 2", pages.n)
	}
}

// countingTicks counts the pages a sweep reads.
type countingTicks struct {
	WatchStore
	n int
}

func (c *countingTicks) TickOpenWatches(ctx context.Context, pageSize int32) ([]db.TickOpenWatchesRow, error) {
	c.n++
	return c.WatchStore.TickOpenWatches(ctx, pageSize)
}

// A team that turned improvement off has its watches wait, though their runs
// are in: they are judged once it is back on.
func TestAWatchWaitsWhileImprovementIsOff(t *testing.T) {
	w := newWorld(t)
	watcher := NewWatcher(w.store, discard())
	id := w.watchedChange(t, []uuid.UUID{w.agent}, 24*time.Hour)
	w.several(t, w.agent, 20, 2, 30*time.Hour, 1000)
	w.several(t, w.agent, 10, 7, time.Hour, 1000)
	w.exec(t, `UPDATE team_settings SET improvement = false WHERE team_id = $1`, w.team)
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.Status != watchWatching || got.AfterRuns != 0 {
		t.Fatalf("judged with improvement off: %+v", got)
	}
	w.exec(t, `UPDATE team_settings SET improvement = true WHERE team_id = $1`, w.team)
	w.later(t, id)
	if err := watcher.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.watch(t, id); got.Status != watchFlagged {
		t.Errorf("once back on: %+v", got)
	}
}

// later is the next sweep coming round: the watch was last swept a sweep
// ago, as ten minutes passing would leave it.
func (w *world) later(t *testing.T, id uuid.UUID) {
	t.Helper()
	w.exec(t, `UPDATE improvement_watches SET last_swept_at = now() - interval '11 minutes' WHERE proposal_id = $1`, id)
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
