package worker

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeJob ignores the runtime it is handed, which is what makes these tests
// cheap: everything the registry decides — ordering, duplicate detection, how
// a failure is reported — happens before any workflow is declared.
type fakeJob struct {
	name      string
	schedules []string
	err       error

	registered bool
	order      *[]string
}

func (j *fakeJob) Name() string { return j.name }

func (j *fakeJob) Register(dbos.Context) ([]dbos.ScheduleSpec, error) {
	j.registered = true
	if j.order != nil {
		*j.order = append(*j.order, j.name)
	}
	if j.err != nil {
		return nil, j.err
	}

	specs := make([]dbos.ScheduleSpec, 0, len(j.schedules))
	for _, name := range j.schedules {
		specs = append(specs, dbos.ScheduleSpec{ScheduleName: name})
	}
	return specs, nil
}

// A worker with nothing to do is a wiring mistake that presents as a perfectly
// healthy process — it starts, logs nothing alarming, and erases nothing. The
// refusal happens before the durable runtime is touched, which is also what
// makes it testable without a database.
func TestAWorkerWithNoJobsRefusesToRun(t *testing.T) {
	t.Parallel()

	err := New("postgres://nowhere/does-not-exist", discardLogger()).Run(t.Context())
	if err == nil {
		t.Fatal("Run accepted a worker with no jobs")
	}
	if !strings.Contains(err.Error(), "no jobs") {
		t.Errorf("err = %v, want it to say the worker has no jobs", err)
	}
}

func TestRegisterCollectsEverySchedule(t *testing.T) {
	t.Parallel()

	first := &fakeJob{name: "reaper.sweep", schedules: []string{"reaper-sweep"}}
	second := &fakeJob{name: "runtime.agents", schedules: []string{"expiry", "cleanup"}}

	w := New("", discardLogger(), first, second)
	specs, err := w.register(nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if len(specs) != 3 {
		t.Fatalf("register returned %d schedules, want 3", len(specs))
	}
	if !first.registered || !second.registered {
		t.Error("a job was not registered, so its workflows would never exist")
	}
}

// Two jobs claiming one schedule name is silent otherwise: ApplySchedules
// takes the last spec for a given name, so the loser registers its workflow,
// logs a clean startup, and simply never fires. The same shape as two
// attachments sharing a ref.
func TestTwoJobsCannotClaimTheSameSchedule(t *testing.T) {
	t.Parallel()

	w := New("", discardLogger(),
		&fakeJob{name: "reaper.sweep", schedules: []string{"nightly"}},
		&fakeJob{name: "runtime.agents", schedules: []string{"nightly"}},
	)

	_, err := w.register(nil)
	if err == nil {
		t.Fatal("register accepted two jobs claiming one schedule name")
	}

	// Both claimants, because knowing there is a clash without knowing who is
	// in it means reading every job to find out.
	for _, want := range []string{"reaper.sweep", "runtime.agents", "nightly"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the clash error does not name %q: %v", want, err)
		}
	}
}

// One job may hold several schedules, and a clash inside one job is still a
// clash. The obvious implementation dedupes only across jobs.
func TestOneJobCannotClaimTheSameScheduleTwice(t *testing.T) {
	t.Parallel()

	w := New("", discardLogger(),
		&fakeJob{name: "reaper.sweep", schedules: []string{"nightly", "nightly"}},
	)

	if _, err := w.register(nil); err == nil {
		t.Fatal("register accepted one job claiming the same schedule twice")
	}
}

// A job that cannot register is a startup failure, and the error has to say
// which one. A worker will eventually hold several, and "register failed" sends
// whoever is on call to read all of them.
func TestARegistrationFailureNamesItsJob(t *testing.T) {
	t.Parallel()

	boom := errors.New("no effects configured")
	w := New("", discardLogger(),
		&fakeJob{name: "reaper.sweep"},
		&fakeJob{name: "runtime.agents", err: boom},
	)

	_, err := w.register(nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the job's own error", err)
	}
	if !strings.Contains(err.Error(), "runtime.agents") {
		t.Errorf("the error does not name the failing job: %v", err)
	}
}

// Registration order is the order the jobs were given in. Nothing depends on
// it today; a map would silently make it random the moment something does.
func TestJobsRegisterInTheOrderTheyWereGiven(t *testing.T) {
	t.Parallel()

	var order []string
	w := New("", discardLogger(),
		&fakeJob{name: "first", order: &order},
		&fakeJob{name: "second", order: &order},
		&fakeJob{name: "third", order: &order},
	)

	if _, err := w.register(nil); err != nil {
		t.Fatalf("register: %v", err)
	}

	want := []string{"first", "second", "third"}
	if len(order) != len(want) {
		t.Fatalf("registered %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("registered %v, want %v", order, want)
		}
	}
}

// A job that asks for nothing periodic is legitimate — an event-driven one
// will — and it must not be mistaken for a failure or trip the clash check.
func TestAJobMayRegisterNoSchedules(t *testing.T) {
	t.Parallel()

	w := New("", discardLogger(),
		&fakeJob{name: "runtime.agents"},
		&fakeJob{name: "reaper.sweep", schedules: []string{"reaper-sweep"}},
	)

	specs, err := w.register(nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(specs) != 1 {
		t.Errorf("register returned %d schedules, want 1", len(specs))
	}
}
