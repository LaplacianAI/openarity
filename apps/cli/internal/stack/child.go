package stack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Child is one supervised process — Postgres, dex, the brain, its worker.
// It is deliberately not a daemon: something has to stay alive holding these,
// and deciding what does that is the supervisor's job, not this type's.
type Child struct {
	Name string // what to call it in an error a person reads
	Path string // the binary
	Args []string
	Env  []string // explicit and complete; see Start
	Log  string   // appended to, never truncated

	mu      sync.Mutex
	cmd     *exec.Cmd
	log     *os.File
	done    chan struct{}
	waitErr error
}

// Start launches the process and returns as soon as it is running. It does
// not wait for the process to be *useful* — a readiness probe is the
// supervisor's concern, because what "ready" means differs per child.
func (c *Child) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running() {
		return fmt.Errorf("stack: %s is already running as pid %d", c.Name, c.cmd.Process.Pid)
	}

	// O_APPEND, never O_TRUNC. A restart must not erase why the last run
	// failed, which is the moment somebody goes looking for it.
	log, err := os.OpenFile(c.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("stack: opening the log for %s: %w", c.Name, err)
	}

	// G204 is inherent here: launching a configured binary is what this type
	// is for. Path is never caller input — it comes from Layout, which
	// derives it from the install root, or from the version recorded in
	// stack.yaml. A person who can edit those files can already run anything.
	cmd := exec.CommandContext(ctx, c.Path, c.Args...) //nolint:gosec // see above

	// Never nil. Go's exec reads that as "give the child the parent's entire
	// environment", which would hand a supervised brain every OPENARITY_*
	// variable in the shell that ran `oa start` — silently overriding the
	// config the installer wrote, with nothing in any log to say why.
	// baseEnv is the platform floor: empty on Unix, and SystemRoot on
	// Windows, where an empty block stops a process being created at all.
	cmd.Env = append(baseEnv(), c.Env...)

	cmd.Stdout = log
	cmd.Stderr = log

	// CommandContext would otherwise kill the child when ctx is cancelled.
	// Stop is the only thing that ends a child here, so that cancellation
	// path is disabled deliberately rather than left as a second, invisible
	// way for a process to die. The context is still carried, so a linter
	// asking for one is satisfied honestly.
	cmd.Cancel = func() error { return nil }

	configure(cmd)

	if err := cmd.Start(); err != nil {
		_ = log.Close()
		// Naming the child matters: "fork/exec: no such file or directory"
		// alone does not say which of the four processes is missing.
		return fmt.Errorf("stack: starting %s: %w", c.Name, err)
	}

	done := make(chan struct{})
	c.cmd, c.log, c.done = cmd, log, done

	// Wait must be called exactly once or the process stays a zombie, so it
	// happens here and every other method reads the result through done.
	go func() {
		err := cmd.Wait()

		c.mu.Lock()
		c.waitErr = err
		_ = log.Close()
		c.mu.Unlock()

		close(done)
	}()

	return nil
}

// Stop asks the child to exit, and kills it if it will not.
//
// The escalation is the whole point. A child that ignores the graceful signal
// must not hang the command: on a laptop that means a person's only way out
// is Activity Monitor, and they reasonably conclude the tool is broken.
func (c *Child) Stop(grace time.Duration) error {
	c.mu.Lock()
	if !c.running() {
		c.mu.Unlock()
		return nil
	}
	proc, done := c.cmd.Process, c.done
	c.mu.Unlock()

	// A failure to signal is not fatal — the process may have exited between
	// the check above and here — so the kill below is still given its turn.
	_ = terminate(proc)

	select {
	case <-done:
		return nil
	case <-time.After(grace):
	}

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stack: killing %s: %w", c.Name, err)
	}

	// Unbounded, and safe to be: a kill cannot be caught or ignored.
	<-done
	return nil
}

// Wait blocks until the child exits and reports how it went.
func (c *Child) Wait() error {
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()

	if done == nil {
		return nil
	}
	<-done

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitErr
}

func (c *Child) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running()
}

// PID is zero when nothing is running, which is what the state file and
// `oa status` both want to print.
func (c *Child) PID() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// running expects c.mu to be held.
func (c *Child) running() bool {
	if c.cmd == nil || c.cmd.Process == nil || c.done == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
