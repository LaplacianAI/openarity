package reaper

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeEffect is an Effect with no other system behind it, so these tests are
// about the loop and nothing else. It models the one behaviour the loop
// depends on and cannot check: a claim leases what it returns, so a second
// claim in the same sweep hands back the next batch rather than the same one.
type fakeEffect struct {
	pending  []Item
	outcomes map[string]Outcome
	doErr    map[string]error

	claimErr   error
	forgetErr  error
	backlogErr error

	claims  []time.Time
	batches []int32
	did     []string
	forgot  []string
	leased  map[string]bool

	// cancelAfter stands in for a SIGTERM arriving mid-batch, which is the
	// only moment the per-item cancellation check does any work: an already
	// cancelled sweep never enters the loop at all.
	cancelAfter int
	cancel      context.CancelFunc
}

func newFakeEffect(refs ...string) *fakeEffect {
	e := &fakeEffect{
		outcomes: map[string]Outcome{},
		doErr:    map[string]error{},
		leased:   map[string]bool{},
	}
	for _, ref := range refs {
		e.pending = append(e.pending, Item{Ref: ref, TeamID: uuid.New()})
	}
	return e
}

func (*fakeEffect) Name() string { return "fake" }

func (e *fakeEffect) Claim(_ context.Context, retryBefore time.Time, batch int32) ([]Item, error) {
	e.claims = append(e.claims, retryBefore)
	e.batches = append(e.batches, batch)
	if e.claimErr != nil {
		return nil, e.claimErr
	}

	var out []Item
	for i := range e.pending {
		item := &e.pending[i]
		if e.leased[item.Ref] {
			continue
		}
		e.leased[item.Ref] = true
		item.Attempts++
		out = append(out, *item)
		if int32(len(out)) == batch {
			break
		}
	}
	return out, nil
}

func (e *fakeEffect) Do(_ context.Context, item Item) (Outcome, error) {
	e.did = append(e.did, item.Ref)
	if err, bad := e.doErr[item.Ref]; bad {
		return 0, err
	}
	if e.cancel != nil && len(e.did) == e.cancelAfter {
		e.cancel()
	}
	return e.outcomes[item.Ref], nil
}

func (e *fakeEffect) Forget(_ context.Context, item Item) error {
	if e.forgetErr != nil {
		return e.forgetErr
	}
	e.forgot = append(e.forgot, item.Ref)
	for i, p := range e.pending {
		if p.Ref == item.Ref {
			e.pending = append(e.pending[:i], e.pending[i+1:]...)
			break
		}
	}
	return nil
}

func (e *fakeEffect) Backlog(ctx context.Context) (int64, time.Time, error) {
	// A real query on a cancelled context fails. A fake that ignored the
	// context would let the sweep read its backlog on a context a shutdown had
	// already closed, and no test would see it.
	if err := ctx.Err(); err != nil {
		return 0, time.Time{}, err
	}
	if e.backlogErr != nil {
		return 0, time.Time{}, e.backlogErr
	}
	if len(e.pending) == 0 {
		return 0, time.Time{}, nil
	}
	return int64(len(e.pending)), time.Now(), nil
}

func TestASweepAppliesAndForgets(t *testing.T) {
	t.Parallel()

	e := newFakeEffect("one", "two")
	res, err := New(e, discardLogger()).Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if res.Applied != 2 || res.Superseded != 0 || res.Failed != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(e.did) != 2 || len(e.forgot) != 2 {
		t.Errorf("did %v, forgot %v", e.did, e.forgot)
	}
	if res.Effect != "fake" {
		t.Errorf("Effect = %q", res.Effect)
	}
	if res.Outstanding != 0 {
		t.Errorf("Outstanding = %d", res.Outstanding)
	}
}

// Superseded work is finished, not skipped: the tombstone still goes. Leaving
// it would make the sweep retry an item nothing will ever act on, forever.
func TestSupersededWorkIsStillForgotten(t *testing.T) {
	t.Parallel()

	e := newFakeEffect("shared")
	e.outcomes["shared"] = Superseded

	res, err := New(e, discardLogger()).Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Superseded != 1 || res.Applied != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(e.forgot) != 1 {
		t.Errorf("forgot %v, want the tombstone dropped", e.forgot)
	}
}

// One item that refuses must not stop the sweep, or it blocks every erasure
// behind it forever.
func TestOneFailureDoesNotStopTheRest(t *testing.T) {
	t.Parallel()

	e := newFakeEffect("bad", "good")
	e.doErr["bad"] = errors.New("the other system said no")

	res, err := New(e, discardLogger()).Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if res.Applied != 1 || res.Failed != 1 {
		t.Errorf("result = %+v", res)
	}
	if len(e.forgot) != 1 || e.forgot[0] != "good" {
		t.Errorf("forgot %v", e.forgot)
	}

	// The failed one keeps its tombstone, which is the whole retry mechanism.
	if res.Outstanding != 1 {
		t.Errorf("Outstanding = %d, want 1", res.Outstanding)
	}
}

// A tombstone that will not clear is work still recorded, and the next sweep
// repeats the effect. Every effect is idempotent for exactly this reason.
func TestAnUnclearedTombstoneIsReportedNotLost(t *testing.T) {
	t.Parallel()

	e := newFakeEffect("one")
	e.forgetErr = errors.New("connection reset")

	res, err := New(e, discardLogger()).Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(e.did) != 1 {
		t.Errorf("did %v", e.did)
	}
	if res.Failed != 1 || res.Applied != 0 {
		t.Errorf("result = %+v", res)
	}
}

// The claim is the one call whose failure means the database is unusable.
func TestAClaimFailureIsAnError(t *testing.T) {
	t.Parallel()

	e := newFakeEffect("one")
	e.claimErr = errors.New("the database is gone")

	if _, err := New(e, discardLogger()).Sweep(t.Context()); err == nil {
		t.Fatal("Sweep reported success with an unusable database")
	}
}

// The backlog is drained across batches, not one batch per run: a scheduled
// sweep that stopped after a hundred would never catch up with a team deletion
// that produced ten thousand.
func TestSweepKeepsGoingPastOneBatch(t *testing.T) {
	t.Parallel()

	refs := make([]string, 0, batchSize+20)
	for i := range batchSize + 20 {
		refs = append(refs, "ref-"+uuid.NewString()+string(rune('a'+i%26)))
	}
	e := newFakeEffect(refs...)

	res, err := New(e, discardLogger()).Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Applied != len(refs) {
		t.Errorf("applied %d, want %d", res.Applied, len(refs))
	}
	if len(e.claims) < 2 {
		t.Errorf("claimed %d times; one batch cannot have covered it", len(e.claims))
	}
}

// Every claim leases for the same window, so a sweeper that dies mid-batch
// makes its rows visible again after it and not before.
func TestEveryClaimAsksForTheSameWindowAndBatch(t *testing.T) {
	t.Parallel()

	refs := make([]string, 0, batchSize+1)
	for range batchSize + 1 {
		refs = append(refs, uuid.NewString())
	}
	e := newFakeEffect(refs...)

	before := time.Now().Add(-retryAfter)
	if _, err := New(e, discardLogger()).Sweep(t.Context()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	after := time.Now().Add(-retryAfter)

	if len(e.claims) < 2 {
		t.Fatalf("%d claims", len(e.claims))
	}
	for i, cutoff := range e.claims {
		if cutoff.Before(before) || cutoff.After(after) {
			t.Errorf("claim %d cutoff %s is outside [%s, %s]", i, cutoff, before, after)
		}
		if e.batches[i] != batchSize {
			t.Errorf("claim %d asked for %d, want %d", i, e.batches[i], batchSize)
		}
	}
}

// The case the per-item cancellation check exists for. An already cancelled
// sweep never enters the batch loop, so only a cancellation arriving part way
// through one reaches it — and a batch is a hundred calls to another service.
func TestACancellationPartWayThroughABatchStopsTheBatch(t *testing.T) {
	t.Parallel()

	refs := make([]string, 0, 10)
	for range 10 {
		refs = append(refs, uuid.NewString())
	}
	e := newFakeEffect(refs...)

	ctx, cancel := context.WithCancel(t.Context())
	e.cancel, e.cancelAfter = cancel, 3

	if _, err := New(e, discardLogger()).Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(e.did) != 3 {
		t.Errorf("acted on %d items after cancelling at 3; the sweep did not stop", len(e.did))
	}
	if len(e.pending) != 7 {
		t.Errorf("%d tombstones remain, want 7", len(e.pending))
	}
}

func TestACancelledSweepStillReportsItsBacklog(t *testing.T) {
	t.Parallel()

	e := newFakeEffect("one", "two", "three")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, err := New(e, discardLogger()).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(e.did) != 0 {
		t.Errorf("acted on %v after cancellation", e.did)
	}

	// The backlog still reads, on a context the cancellation cannot close.
	if res.Outstanding != 3 {
		t.Errorf("Outstanding = %d, want 3 — the backlog was not read", res.Outstanding)
	}
}

func TestAnEmptyBacklogIsNotAnError(t *testing.T) {
	t.Parallel()

	e := newFakeEffect()
	res, err := New(e, discardLogger()).Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res != (Result{Effect: "fake"}) {
		t.Errorf("result = %+v", res)
	}
	if len(e.claims) != 1 {
		t.Errorf("claimed %d times for an empty backlog", len(e.claims))
	}
}

// A backlog that cannot be read is not a failed sweep — the work is done and
// only the reporting is missing.
func TestAnUnreadableBacklogDoesNotFailTheSweep(t *testing.T) {
	t.Parallel()

	e := newFakeEffect("one")
	e.backlogErr = errors.New("connection reset")

	res, err := New(e, discardLogger()).Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Applied != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestOverdueIsAboutAgeNotCount(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		res  Result
		want bool
	}{
		"nothing outstanding":  {Result{}, false},
		"outstanding and new":  {Result{Outstanding: 900, Oldest: time.Now()}, false},
		"one, and a day stuck": {Result{Outstanding: 1, Oldest: time.Now().Add(-25 * time.Hour)}, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.res.Overdue(); got != tc.want {
				t.Errorf("Overdue() = %v, want %v", got, tc.want)
			}
		})
	}
}
