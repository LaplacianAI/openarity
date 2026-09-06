package stack

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder collects the order things happened in, across goroutines.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) note(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *recorder) joined() string { return strings.Join(r.seen(), " ") }

// component builds a long-lived child that records when its probe runs.
func component(t *testing.T, r *recorder, name string, ready func() error) Component {
	t.Helper()

	c := helper(t, "stubborn")
	c.Name = name

	return Component{
		Name:  name,
		Child: c,
		Ready: func(context.Context) error {
			r.note("ready:" + name)
			if ready == nil {
				return nil
			}
			return ready()
		},
	}
}

func testStack(components ...Component) *Stack {
	return &Stack{
		Components:   components,
		ReadyTimeout: 5 * time.Second,
		StopGrace:    500 * time.Millisecond,
	}
}

// The ordering the whole design rests on. Postgres has to be accepting
// connections before the brain runs its migrations, and dex has to answer
// before the brain is any use — so a component does not start until the one
// before it is ready, not merely started.
func TestAComponentDoesNotStartUntilThePreviousOneIsReady(t *testing.T) {
	r := &recorder{}

	first := component(t, r, "first", nil)
	second := component(t, r, "second", nil)

	// Wrapping Start is how the order is observed: Component holds the Child,
	// so the probe recording above and this recording together give the full
	// interleaving.
	second.BeforeStart = func(context.Context) error {
		r.note("start:second")
		return nil
	}
	first.BeforeStart = func(context.Context) error {
		r.note("start:first")
		return nil
	}

	s := testStack(first, second)
	t.Cleanup(func() { _ = s.Stop(context.WithoutCancel(t.Context())) })

	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	want := "start:first ready:first start:second ready:second"
	if got := r.joined(); got != want {
		t.Errorf("order was %q, want %q", got, want)
	}
}

// A stack that half starts is worse than one that does not start: the person
// gets an error, and four processes they did not ask for are still holding
// ports and a data directory.
func TestAComponentThatNeverBecomesReadyStopsTheOnesBeforeIt(t *testing.T) {
	r := &recorder{}

	first := component(t, r, "first", nil)
	broken := component(t, r, "broken", func() error { return errors.New("not today") })

	s := testStack(first, broken)
	s.ReadyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { _ = s.Stop(context.WithoutCancel(t.Context())) })

	err := s.Start(t.Context())
	if err == nil {
		t.Fatal("Start() = nil, want the failure of the component that never became ready")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("Start() = %q, want it to name the component", err)
	}

	if first.Child.Running() {
		t.Error("the first component is still running after the second failed to start")
	}
	if broken.Child.Running() {
		t.Error("the failing component is still running")
	}
}

// Reverse order, because the dependencies run the other way: stopping
// Postgres first would pull the database out from under a brain that is still
// serving requests.
func TestStopRunsInReverseOrder(t *testing.T) {
	r := &recorder{}

	first := component(t, r, "first", nil)
	second := component(t, r, "second", nil)
	first.Shutdown = func(context.Context) error { r.note("stop:first"); return nil }
	second.Shutdown = func(context.Context) error { r.note("stop:second"); return nil }

	s := testStack(first, second)
	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}

	got := r.joined()
	if !strings.Contains(got, "stop:second stop:first") {
		t.Errorf("stop order was %q, want second before first", got)
	}
}

// Postgres is stopped with `pg_ctl stop -m fast` rather than a signal, so a
// component may bring its own way of asking. Asking is all it is: the child
// is still stopped afterwards, so a Shutdown that silently does nothing
// cannot leave a process behind.
func TestAShutdownThatDoesNothingStillLeavesNothingRunning(t *testing.T) {
	r := &recorder{}

	c := component(t, r, "postgres", nil)
	c.Shutdown = func(context.Context) error { return nil }

	s := testStack(c)
	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	waitForReady(t, c.Child)

	if err := s.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if c.Child.Running() {
		t.Error("the child is still running after Stop — Shutdown was trusted to have worked")
	}
}

// A Shutdown that fails must not stop the rest of the stack from being shut
// down. Returning early there would leave every earlier component running
// because a later one misbehaved.
func TestOneFailingShutdownDoesNotStrandTheOthers(t *testing.T) {
	r := &recorder{}

	first := component(t, r, "first", nil)
	second := component(t, r, "second", nil)
	second.Shutdown = func(context.Context) error { return errors.New("refused") }

	s := testStack(first, second)
	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	if err := s.Stop(t.Context()); err == nil {
		t.Error("Stop() = nil, want the shutdown failure reported")
	}
	if first.Child.Running() {
		t.Error("the first component was left running because the second's shutdown failed")
	}
}

// Postgres is started by pg_ctl rather than supervised directly, because
// postgres.exe refuses to run under an account with administrative rights and
// pg_ctl creates the restricted token that makes it possible. Such a component
// has no child process of its own: it is started, probed and stopped entirely
// through commands.
func TestAComponentWithNoChildIsStartedProbedAndStopped(t *testing.T) {
	r := &recorder{}
	ready := false

	external := Component{
		Name: "postgres",
		BeforeStart: func(context.Context) error {
			r.note("pg_ctl:start")
			ready = true
			return nil
		},
		Ready: func(context.Context) error {
			r.note("probe")
			if !ready {
				return errors.New("not yet")
			}
			return nil
		},
		Shutdown: func(context.Context) error {
			r.note("pg_ctl:stop")
			ready = false
			return nil
		},
	}

	s := testStack(external)
	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if got := r.joined(); !strings.Contains(got, "pg_ctl:start probe") {
		t.Errorf("order was %q, want it started before it was probed", got)
	}

	status := s.Status(t.Context())
	if len(status) != 1 || !status[0].Running {
		t.Errorf("Status() = %+v, want the component reported running", status)
	}

	if err := s.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if !strings.Contains(r.joined(), "pg_ctl:stop") {
		t.Errorf("Stop() did not ask the component to stop: %q", r.joined())
	}
	if s.Status(t.Context())[0].Running {
		t.Error("a stopped component is still reported running")
	}
}

// Without a child there is nothing whose exit can shortcut the wait, so a
// component that never becomes ready must still give up on the timeout rather
// than looping forever.
func TestAChildlessComponentThatNeverBecomesReadyGivesUp(t *testing.T) {
	s := testStack(Component{
		Name:  "postgres",
		Ready: func(context.Context) error { return errors.New("never") },
	})
	s.ReadyTimeout = 200 * time.Millisecond

	err := s.Start(t.Context())
	if err == nil {
		t.Fatal("Start() = nil, want the readiness timeout")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("Start() = %q, want it to name the component", err)
	}
}

func TestStatusNamesEveryComponent(t *testing.T) {
	r := &recorder{}

	first := component(t, r, "first", nil)
	second := component(t, r, "second", nil)

	s := testStack(first, second)
	t.Cleanup(func() { _ = s.Stop(context.WithoutCancel(t.Context())) })

	before := s.Status(t.Context())
	if len(before) != 2 {
		t.Fatalf("Status() before Start returned %d entries, want 2", len(before))
	}
	for _, st := range before {
		if st.Running {
			t.Errorf("%s is reported running before Start", st.Name)
		}
		if st.PID != 0 {
			t.Errorf("%s has pid %d before Start, want 0", st.Name, st.PID)
		}
	}

	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	for _, st := range s.Status(t.Context()) {
		if !st.Running {
			t.Errorf("%s is not reported running after Start", st.Name)
		}
		if st.PID == 0 {
			t.Errorf("%s has pid 0 after Start", st.Name)
		}
	}
}

// Starting a stack twice would orphan the first set of processes — four of
// them, holding the ports and the data directory, with nothing tracking them.
func TestStartingAStackTwiceIsRefused(t *testing.T) {
	r := &recorder{}

	s := testStack(component(t, r, "only", nil))
	t.Cleanup(func() { _ = s.Stop(context.WithoutCancel(t.Context())) })

	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if err := s.Start(t.Context()); err == nil {
		t.Error("Start() twice = nil, want a refusal")
	}
}
