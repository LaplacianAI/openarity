package improve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The watch's numbers, approved in the I4 design.
const (
	// WatchRuns is how many counted runs after a change decide it, and how
	// many before it a person reads beside them.
	WatchRuns = 10
	// baselineRuns is the most counted runs before a change its bar is
	// estimated from, and baselineLeast the fewest it judges against.
	baselineRuns  = 50
	baselineLeast = 10
	// watchChance is how rarely chance alone may give the bad runs that
	// flag a change.
	watchChance = 0.05
	// watchFor is how long a watch waits for its runs, counted only while
	// the team has improvement on.
	watchFor = 30 * 24 * time.Hour
	// watchPage is how many watches a sweep takes at a time, and
	// watchPages how many pages at most, so one sweep stays bounded.
	watchPage  = 100
	watchPages = 50
	// A window reads this many times its runs, so runs nobody could judge
	// (neither good nor bad) do not leave it short; one with more unjudged
	// runs than that counts fewer, and waits for later runs.
	watchRead = 3
)

const (
	watchWatching = "watching"
	watchClear    = "clear"
	watchFlagged  = "flagged"
	watchCleared  = "cleared"
	watchEnded    = "ended"

	flagWorse    = "worse"
	flagCostlier = "costlier"
	flagBoth     = "both"

	endedTooFew = "too_few_runs"
)

// WatchStore is what the watch reads and writes.
type WatchStore interface {
	TickOpenWatches(ctx context.Context, pageSize int32) ([]db.TickOpenWatchesRow, error)
	GetImprovementProposal(ctx context.Context, arg db.GetImprovementProposalParams) (db.ImprovementProposal, error)
	ListWatchRuns(ctx context.Context, arg db.ListWatchRunsParams) ([]db.ListWatchRunsRow, error)
	ListOverlappingWatches(ctx context.Context, arg db.ListOverlappingWatchesParams) ([]uuid.UUID, error)
	SetWatchProgress(ctx context.Context, arg db.SetWatchProgressParams) error
	FinishImprovementWatch(ctx context.Context, arg db.FinishImprovementWatchParams) (int64, error)
}

// Watcher counts how each applied change went.
type Watcher struct {
	store  WatchStore
	logger *slog.Logger
}

func NewWatcher(s WatchStore, logger *slog.Logger) *Watcher {
	return &Watcher{store: s, logger: logger}
}

// counted is one judged run, good or bad, with what it cost.
type counted struct {
	bad, up, down, signature bool
	spent                    int64
	steps                    int32
}

// window is what a set of counted runs comes to.
type window struct {
	runs, bad, up, down, signature int16
	spent                          []int64
	steps                          []int64
}

func windowOf(rs []counted) window {
	w := window{runs: int16(len(rs))} //nolint:gosec // G115: at most baselineRuns
	for _, r := range rs {
		if r.bad {
			w.bad++
		}
		if r.up {
			w.up++
		}
		if r.down {
			w.down++
		}
		if r.signature {
			w.signature++
		}
		w.spent = append(w.spent, r.spent)
		w.steps = append(w.steps, int64(r.steps))
	}
	return w
}

// bar is the fewest bad runs out of WatchRuns that chance gives less than
// watchChance of the time: a one-sided binomial tail at the baseline's rate
// of bad runs, smoothed by adding one bad and one good run (Laplace), so a
// baseline with none bad still gives a rate above zero. The rate is taken as
// known, so with a short baseline the chance of a false flag is somewhat
// above watchChance; the design accepts that, a flag being a prompt to look
// and never a revert. ok is false with too little history to judge against.
func bar(bad, runs int) (int, bool) {
	if runs < baselineLeast {
		return 0, false
	}
	p := float64(bad+1) / float64(runs+2)
	for k := 0; k <= WatchRuns; k++ {
		if binomialTail(WatchRuns, k, p) < watchChance {
			return k, true
		}
	}
	return WatchRuns + 1, true
}

// binomialTail is the chance of k or more successes in n tries at p.
func binomialTail(n, k int, p float64) float64 {
	tail := 0.0
	for j := k; j <= n; j++ {
		tail += choose(n, j) * math.Pow(p, float64(j)) * math.Pow(1-p, float64(n-j))
	}
	return tail
}

func choose(n, k int) float64 {
	c := 1.0
	for i := 1; i <= k; i++ {
		c = c * float64(n-k+i) / float64(i)
	}
	return c
}

// median is the middle value, the lower of the two middles for an even
// count; ok is false for none.
func median(xs []int64) (int64, bool) {
	return percentile(xs, 50)
}

// percentile is the nearest-rank percentile of xs.
func percentile(xs []int64, pct int) (int64, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	rank := int(math.Ceil(float64(pct) / 100 * float64(len(s))))
	return s[max(rank, 1)-1], true
}

// verdict is what a finished watch comes to.
type verdict struct {
	status string
	flag   string
}

// judgeWatch decides a watch whose after window is full: flagged worse
// when its bad runs reach the bar, costlier when its median cost passes
// the baseline's 90th percentile without fewer bad runs than before, both,
// or clear. Without a bar there is no history to judge against: clear.
func judgeWatch(baseline, before, after window) verdict {
	k, ok := bar(int(baseline.bad), int(baseline.runs))
	worse := ok && int(after.bad) >= k
	costlier := false
	if p90, ok := percentile(baseline.spent, 90); ok && baseline.runs >= baselineLeast {
		if m, ok := median(after.spent); ok && m > p90 && after.bad >= before.bad {
			costlier = true
		}
	}
	switch {
	case worse && costlier:
		return verdict{watchFlagged, flagBoth}
	case worse:
		return verdict{watchFlagged, flagWorse}
	case costlier:
		return verdict{watchFlagged, flagCostlier}
	}
	return verdict{status: watchClear}
}

// Sweep goes round the open watches, the least recently swept first. Each
// counts the time since its last sweep while its team has improvement on;
// one in a team with it off is passed over until the team turns it back on.
// A watch with its runs gets its verdict; one past watchFor without them
// ends.
func (w *Watcher) Sweep(ctx context.Context) error {
	var failed []error
	for range watchPages {
		open, err := w.store.TickOpenWatches(ctx, watchPage)
		if err != nil {
			return errors.Join(append(failed, fmt.Errorf("listing open watches: %w", err))...)
		}
		for _, o := range open {
			if !o.Improving {
				continue
			}
			if err := w.count(ctx, o); err != nil {
				failed = append(failed, err)
			}
		}
		if len(open) < watchPage {
			break
		}
	}
	return errors.Join(failed...)
}

// count reads one watch's windows and writes what they come to.
func (w *Watcher) count(ctx context.Context, o db.TickOpenWatchesRow) error {
	p, err := w.store.GetImprovementProposal(ctx, db.GetImprovementProposalParams{ID: o.ProposalID, TeamID: o.TeamID})
	if err != nil {
		return fmt.Errorf("reading proposal %s: %w", o.ProposalID, err)
	}
	key, hasKey := Key{}, false
	if p.Signature != nil {
		key, hasKey = parseKey(*p.Signature)
	}
	read := func(after bool, n int) ([]counted, []time.Time, error) {
		rows, err := w.store.ListWatchRuns(ctx, db.ListWatchRunsParams{
			TeamID: o.TeamID, AgentIds: o.AgentIds, At: o.StartedAt, After: after,
			Part: key.Part, Target: key.Target, Cause: key.Cause, PageSize: int32(n * watchRead), //nolint:gosec // G115: at most baselineRuns times watchRead
		})
		if err != nil {
			return nil, nil, fmt.Errorf("reading the runs of proposal %s: %w", o.ProposalID, err)
		}
		var out []counted
		var at []time.Time
		for _, r := range rows {
			e := Effective(r.Verdict, "", r.Up, r.Down)
			if e == Neither {
				continue
			}
			out = append(out, counted{bad: e == Bad, up: r.Up > 0, down: r.Down > 0, signature: hasKey && r.HasSignature, spent: r.Spent, steps: r.Steps})
			at = append(at, r.CreatedAt)
			if len(out) == n {
				break
			}
		}
		return out, at, nil
	}
	baseRuns, baseAt, err := read(false, baselineRuns)
	if err != nil {
		return err
	}
	afterRuns, afterAt, err := read(true, WatchRuns)
	if err != nil {
		return err
	}
	baseline, before, after := windowOf(baseRuns), windowOf(baseRuns[:min(WatchRuns, len(baseRuns))]), windowOf(afterRuns)

	since, until := o.StartedAt, time.Now()
	if len(baseAt) > 0 {
		since = baseAt[len(baseAt)-1]
	}
	if len(afterAt) == WatchRuns {
		until = afterAt[len(afterAt)-1]
	}
	overlapping, err := w.store.ListOverlappingWatches(ctx, db.ListOverlappingWatchesParams{
		TeamID: o.TeamID, AgentIds: o.AgentIds, ProposalID: o.ProposalID, Since: since, Until: until,
	})
	if err != nil {
		return fmt.Errorf("reading the changes beside proposal %s: %w", o.ProposalID, err)
	}
	progress := progressOf(o.ProposalID, baseline, before, after, hasKey, overlapping)
	if err := w.store.SetWatchProgress(ctx, progress); err != nil {
		return fmt.Errorf("recording proposal %s's watch: %w", o.ProposalID, err)
	}

	finish := db.FinishImprovementWatchParams{ProposalID: o.ProposalID}
	switch {
	case after.runs >= WatchRuns:
		v := judgeWatch(baseline, before, after)
		finish.Status = v.status
		if v.flag != "" {
			finish.Flag = &v.flag
		}
	case time.Duration(o.OnSeconds)*time.Second >= watchFor:
		ended := endedTooFew
		finish.Status, finish.EndedWhy = watchEnded, &ended
	default:
		return nil
	}
	n, err := w.store.FinishImprovementWatch(ctx, finish)
	if err != nil {
		return fmt.Errorf("finishing proposal %s's watch: %w", o.ProposalID, err)
	}
	if n > 0 {
		w.logger.InfoContext(ctx, "A change's watch finished", "proposal", o.ProposalID, "team", o.TeamID,
			"status", finish.Status, "flag", finish.Flag, "ended", finish.EndedWhy,
			"before_bad", before.bad, "before_runs", before.runs, "after_bad", after.bad, "after_runs", after.runs, "bar", progress.Bar)
	}
	return nil
}

// progressOf is what the windows come to, as the watch row holds it.
func progressOf(id uuid.UUID, baseline, before, after window, hasKey bool, overlapping []uuid.UUID) db.SetWatchProgressParams {
	p := db.SetWatchProgressParams{
		ProposalID:   id,
		BaselineRuns: baseline.runs, BaselineBad: baseline.bad,
		BeforeRuns: before.runs, BeforeBad: before.bad, BeforeUp: before.up, BeforeDown: before.down,
		AfterRuns: after.runs, AfterBad: after.bad, AfterUp: after.up, AfterDown: after.down,
		Overlapping: orEmptyIDs(overlapping),
	}
	if k, ok := bar(int(baseline.bad), int(baseline.runs)); ok {
		b := int16(k) //nolint:gosec // G115: at most WatchRuns + 1
		p.Bar = &b
	}
	p.BeforeTokens = optional(median(before.spent))
	p.AfterTokens = optional(median(after.spent))
	if baseline.runs >= baselineLeast {
		p.BaselineP90 = optional(percentile(baseline.spent, 90))
	}
	p.BeforeSteps = optional32(median(before.steps))
	p.AfterSteps = optional32(median(after.steps))
	if hasKey {
		p.SigBefore, p.SigAfter = &before.signature, &after.signature
	}
	return p
}

func optional(v int64, ok bool) *int64 {
	if !ok {
		return nil
	}
	return &v
}

func optional32(v int64, ok bool) *int32 {
	if !ok {
		return nil
	}
	n := int32(min(v, math.MaxInt32)) //nolint:gosec // G115: clamped
	return &n
}

func orEmptyIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
