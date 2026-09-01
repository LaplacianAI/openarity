package reaper

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// dbosParser is the parser DBOS builds — cron.New(cron.WithSeconds()) — so
// this checks sweepCron against the thing that will actually read it rather
// than against a convenient approximation.
func dbosParser() cron.Parser {
	return cron.NewParser(cron.Second | cron.Minute | cron.Hour |
		cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

// The constant has to parse, and it has to parse as *seconds-first*. Six
// fields is not a style preference: DBOS enables seconds, so the familiar
// five-field form is refused outright and the refusal lands at worker start
// rather than at boot.
func TestTheSweepCronParsesAsDBOSWillReadIt(t *testing.T) {
	t.Parallel()

	schedule, err := dbosParser().Parse(sweepCron)
	if err != nil {
		t.Fatalf("sweepCron = %q does not parse: %v\nDBOS builds its parser with "+
			"seconds enabled, so a five-field expression is rejected", sweepCron, err)
	}

	// Fifteen minutes, on the quarter hour, and not something that happens to
	// parse while meaning every fifteen seconds.
	at := time.Date(2026, 9, 1, 3, 7, 0, 0, time.UTC)
	first := schedule.Next(at)
	second := schedule.Next(first)

	if got := second.Sub(first); got != 15*time.Minute {
		t.Errorf("consecutive ticks are %v apart, want 15m — %q parses but does "+
			"not mean what the deployment docs say it means", got, sweepCron)
	}
	if first.Second() != 0 {
		t.Errorf("first tick is at %v, want the top of the minute", first)
	}
}

// The mutation this guards against is the obvious one: someone "fixes" the
// constant to the five-field form they recognise. Without this the whole suite
// still passes, because nothing else parses it.
func TestTheFiveFieldFormWouldBeRefused(t *testing.T) {
	t.Parallel()

	if _, err := dbosParser().Parse("*/15 * * * *"); err == nil {
		t.Fatal("the five-field form parsed, so the six-field constant is not " +
			"load-bearing and this guard proves nothing")
	}
}

func TestTheJobRegistersOneScheduleThatBackfills(t *testing.T) {
	t.Parallel()

	// schedules() rather than Register: everything asserted here is about what
	// the job asks for, and Register cannot run without a live durable runtime
	// behind it.
	specs := SweepJob(discardLogger(), newFakeEffect("one")).schedules()
	if len(specs) != 1 {
		t.Fatalf("schedules() returned %d, want 1", len(specs))
	}

	spec := specs[0]
	if spec.ScheduleName != sweepSchedule {
		t.Errorf("ScheduleName = %q, want %q", spec.ScheduleName, sweepSchedule)
	}
	if spec.Schedule != sweepCron {
		t.Errorf("Schedule = %q, want %q", spec.Schedule, sweepCron)
	}
	if spec.WorkflowName != sweepWorkflow {
		t.Errorf("WorkflowName = %q, want %q — the name is stored state, and a "+
			"schedule pointing at a workflow nobody registered never fires",
			spec.WorkflowName, sweepWorkflow)
	}
	if !spec.AutomaticBackfill {
		t.Error("AutomaticBackfill is off, so ticks missed while the process " +
			"was down are lost — the one thing a CronJob also cannot do")
	}
}

// A job with nothing to sweep would install a schedule, fire every fifteen
// minutes, erase nothing, and look entirely healthy doing it.
func TestAJobWithNoEffectsRefusesToRegister(t *testing.T) {
	t.Parallel()

	if _, err := SweepJob(discardLogger()).Register(nil); err == nil {
		t.Fatal("Register accepted a job with no effects")
	}
}

func TestTheJobIsNamed(t *testing.T) {
	t.Parallel()

	if got := SweepJob(discardLogger(), newFakeEffect("one")).Name(); got == "" {
		t.Error("Name is empty, so the worker cannot say which job failed")
	}
}

// Secrets before objects, because destroying a team's key is the half of an
// erasure that does not wait. The slice order is the decision; this is the
// test that notices when someone reorders it.
func TestSweepAllRunsEveryEffectInOrder(t *testing.T) {
	t.Parallel()

	var order []string
	secrets := newFakeEffect("key")
	secrets.name, secrets.order = "secrets", &order
	objects := newFakeEffect("blob")
	objects.name, objects.order = "objects", &order

	if err := SweepAll(t.Context(), discardLogger(), secrets, objects); err != nil {
		t.Fatalf("SweepAll: %v", err)
	}

	if len(order) < 2 || order[0] != "secrets" {
		t.Errorf("effects ran in order %v, want secrets first", order)
	}
	if len(secrets.did) != 1 || len(objects.did) != 1 {
		t.Errorf("secrets did %v, objects did %v — both should have swept",
			secrets.did, objects.did)
	}
}

// An overdue backlog is the alarm, and it has to name which effect is behind
// and how far. A bare error would tell an operator that something is wrong and
// nothing about where to look.
func TestSweepAllReportsEveryOverdueEffect(t *testing.T) {
	t.Parallel()

	stuck := newFakeEffect("one")
	stuck.name = "secrets"
	stuck.oldest = time.Now().Add(-48 * time.Hour)
	stuck.doErr = map[string]error{"one": errors.New("vault refused")}

	alsoStuck := newFakeEffect("two")
	alsoStuck.name = "objects"
	alsoStuck.oldest = time.Now().Add(-30 * time.Hour)
	alsoStuck.doErr = map[string]error{"two": errors.New("bucket refused")}

	err := SweepAll(t.Context(), discardLogger(), stuck, alsoStuck)
	if !errors.Is(err, ErrOverdue) {
		t.Fatalf("err = %v, want ErrOverdue", err)
	}

	for _, want := range []string{"secrets", "objects"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the overdue error does not name %q: %v\n"+
				"an alarm that omits an effect hides half the backlog", want, err)
		}
	}
}

// A backlog that is merely non-empty is a busy delete, not a failure. Nine
// hundred a minute old is normal; one a day old is a sweep failing every run.
func TestAFreshBacklogIsNotOverdue(t *testing.T) {
	t.Parallel()

	busy := newFakeEffect("one")
	busy.doErr = map[string]error{"one": errors.New("transient")}

	if err := SweepAll(t.Context(), discardLogger(), busy); err != nil {
		t.Fatalf("SweepAll on a fresh backlog = %v, want nil", err)
	}
}

// A claim that fails is not an overdue erasure — it is a broken sweep, and it
// must surface as itself so a caller can tell "the database is unreachable"
// from "data that should be gone is still here".
func TestSweepAllReturnsARealFailureUnwrapped(t *testing.T) {
	t.Parallel()

	broken := newFakeEffect("one")
	broken.claimErr = errors.New("connection refused")

	err := SweepAll(t.Context(), discardLogger(), broken)
	if err == nil {
		t.Fatal("SweepAll swallowed a claim failure")
	}
	if errors.Is(err, ErrOverdue) {
		t.Errorf("a claim failure was reported as ErrOverdue: %v", err)
	}
}
