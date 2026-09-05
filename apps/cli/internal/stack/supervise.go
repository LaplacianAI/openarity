package stack

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Component is one part of the stack: the process, how to tell when it is
// useful, and anything that has to happen around it.
type Component struct {
	Name  string
	Child *Child

	// BeforeStart runs after everything earlier in the stack is ready and
	// before this component's own process is launched. It is where `brain
	// migrate up` goes: the migration needs a Postgres that is accepting
	// connections, and it must finish before the brain starts serving.
	BeforeStart func(context.Context) error

	// Ready reports whether the component is useful yet, not merely running.
	// Polled until it returns nil or the stack's ReadyTimeout expires. A nil
	// Ready means started is ready enough.
	Ready func(context.Context) error

	// Shutdown asks the component to stop in its own way. Postgres wants
	// `pg_ctl stop -m fast` rather than a signal, because an ungraceful stop
	// there costs data. It is only ever an ask — Stop still stops the child
	// afterwards, so a Shutdown that quietly does nothing cannot leave a
	// process behind.
	Shutdown func(context.Context) error
}

// Status is one line of `oa status`.
type Status struct {
	Name    string
	PID     int
	Running bool
	Ready   bool
}

// Stack starts the components in order and stops them in reverse. It holds
// them for as long as it lives, which is why `oa start` detaches a supervisor
// process rather than starting four processes and returning: something has to
// stay alive owning them, and one PID is easier to find again than four.
type Stack struct {
	Components   []Component
	ReadyTimeout time.Duration
	StopGrace    time.Duration

	mu      sync.Mutex
	started bool
}

const readyPoll = 50 * time.Millisecond

// Start brings the stack up in order, waiting for each component to be ready
// before starting the next. A component that never becomes ready takes the
// whole stack down with it — a half-started stack is worse than one that did
// not start, because the person gets an error *and* processes they did not
// ask for are holding ports and a data directory.
func (s *Stack) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("stack: already started")
	}
	s.started = true
	s.mu.Unlock()

	for i, c := range s.Components {
		if err := s.startOne(ctx, c); err != nil {
			// Everything up to and including the one that failed, in reverse.
			// Using context.WithoutCancel because the failure may well be a
			// cancelled context, and a rollback that skips itself for that
			// reason leaves exactly the mess it exists to prevent.
			_ = s.stopThrough(context.WithoutCancel(ctx), i)

			s.mu.Lock()
			s.started = false
			s.mu.Unlock()

			return err
		}
	}
	return nil
}

func (s *Stack) startOne(ctx context.Context, c Component) error {
	if c.BeforeStart != nil {
		if err := c.BeforeStart(ctx); err != nil {
			return fmt.Errorf("stack: preparing %s: %w", c.Name, err)
		}
	}

	if err := c.Child.Start(ctx); err != nil {
		return err
	}
	return s.waitReady(ctx, c)
}

// waitReady polls rather than waiting for a notification, because none of
// these processes has one to give: Postgres is ready when it accepts a
// connection, dex when it answers, and the brain when /readyz says so.
func (s *Stack) waitReady(ctx context.Context, c Component) error {
	if c.Ready == nil {
		return nil
	}

	deadline := time.Now().Add(s.ReadyTimeout)
	var last error

	for {
		last = c.Ready(ctx)
		if last == nil {
			return nil
		}

		// A process that has already exited is never going to become ready,
		// and waiting the full timeout to say so buries the real reason in
		// the log while the person watches a spinner.
		if !c.Child.Running() {
			return fmt.Errorf("stack: %s exited before it was ready: %w", c.Name, last)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("stack: %s was not ready within %s: %w", c.Name, s.ReadyTimeout, last)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("stack: waiting for %s: %w", c.Name, ctx.Err())
		case <-time.After(readyPoll):
		}
	}
}

// Stop takes the stack down in reverse order, because the dependencies run
// the other way: stopping Postgres first pulls the database out from under a
// brain that is still serving.
func (s *Stack) Stop(ctx context.Context) error {
	err := s.stopThrough(ctx, len(s.Components)-1)

	s.mu.Lock()
	s.started = false
	s.mu.Unlock()

	return err
}

// stopThrough stops components last..0, and keeps going after a failure. An
// early return here would leave every earlier component running because a
// later one misbehaved, so the errors are collected instead.
func (s *Stack) stopThrough(ctx context.Context, last int) error {
	var errs []error

	for i := last; i >= 0; i-- {
		c := s.Components[i]

		if c.Shutdown != nil {
			if err := c.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("stack: asking %s to stop: %w", c.Name, err))
			}
		}
		if err := c.Child.Stop(s.StopGrace); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Status reports every component, running or not. A component that is not
// running is not probed: a readiness check against a dead process is a
// guaranteed timeout, and multiplied by four it is a `oa status` that takes
// half a minute to tell you nothing is up.
func (s *Stack) Status(ctx context.Context) []Status {
	out := make([]Status, 0, len(s.Components))

	for _, c := range s.Components {
		st := Status{Name: c.Name, PID: c.Child.PID(), Running: c.Child.Running()}

		if st.Running && c.Ready != nil {
			probe, cancel := context.WithTimeout(ctx, time.Second)
			st.Ready = c.Ready(probe) == nil
			cancel()
		} else if st.Running {
			st.Ready = true
		}

		out = append(out, st)
	}
	return out
}
