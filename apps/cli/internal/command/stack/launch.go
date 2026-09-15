package stack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

// The step setup calls "Starting Openarity" used to be stopPostgres, which
// stops the cluster the migrations used and starts nothing at all. Setup then
// printed "Openarity is running at http://127.0.0.1:21120/ui", opened a
// browser at it and finished, and the window showed "Open the dashboard" —
// with nothing listening on any of it.
//
// What did eventually start it was the autostart unit, a minute or more later
// and writing nowhere, so the gap between the promise and the fact was
// however long the machine took. A person clicking the button setup had just
// given them got "refused to connect".
//
// So: start it here, wait until the brain answers, and only then let setup
// say it is running.

// howLongToWaitForReady covers a cold start of everything at once — initdb's
// cluster coming up, dex, the brain's own migrations check, and a gateway if
// this install runs one. Measured at around ninety seconds on a laptop with
// OmniRoute; the timeout is generous because the failure it guards against is
// waiting forever, not waiting a while.
const howLongToWaitForReady = 4 * time.Minute

// startStack stops the cluster the install used, then starts the whole stack
// the way `oa stack start` does — as a detached supervisor, so it outlives
// the setup that spawned it and the window that spawned that.
func startStack(ctx context.Context, p engine.Plan) error {
	return launch(ctx, p, stopPostgres, spawnSupervisor)
}

// launch is the order the three parts happen in, with both processes passed
// in so a test can drive it without running either. Without that seam the only
// way to exercise this is to re-exec the test binary, and the readiness wait —
// the part that makes the promise true — would go unchecked. It was unchecked
// before, which is how the step got away with starting nothing.
//
// The stop is a parameter for the same reason and one more: a test that
// supplied a real pg_ctl had to write a stub and exec it immediately, which
// fails intermittently under a loaded machine and failed roughly one run in
// ten of `make check`.
func launch(ctx context.Context, p engine.Plan, stop func(context.Context, engine.Plan) error, spawn func(string) error) error {
	if err := stop(ctx, p); err != nil {
		return err
	}
	if err := spawn(p.Layout.Root); err != nil {
		return err
	}
	return waitForReady(ctx, p.Ports.API)
}

// spawnSupervisor starts the whole stack the way `oa stack start` does — as a
// detached process, so it outlives the setup that spawned it and the window
// that spawned that.
func spawnSupervisor(root string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("stack: cannot find the binary to start with: %w", err)
	}

	// The root is the install directory, from a flag or the platform default,
	// not from anything a stranger sends. It stopped being a Layout field
	// when the spawn learnt to take a root on its own, which is when gosec
	// started asking.
	log, err := os.OpenFile(filepath.Join(root, "logs", "supervisor.log"), //nolint:gosec // the install directory
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	cmd := supervisorCommand(exe, root, log)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("stack: could not start the supervisor: %w", err)
	}
	// Released rather than waited on: it is a daemon now, and holding a child
	// handle we never reap is what makes a zombie.
	return cmd.Process.Release()
}

// supervisorCommand is built rather than run here, so a test can read what
// would have been started: the arguments, and the environment it gets.
//
// exec.Command rather than CommandContext, which noctx wants everywhere and
// which is wrong exactly here: the supervisor has to outlive the setup that
// spawned it, and a context would kill it the moment setup returned. That is
// the whole job of this function.
//
// exe is os.Executable — this binary, re-run with its own subcommand — and
// the only value not written here is the install root, which arrives as one
// argv element to a process started without a shell. There is nothing for a
// metacharacter to do.
//
// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
func supervisorCommand(exe, root string, log io.Writer) *exec.Cmd {
	cmd := exec.Command(exe, "stack", "start", "--root", root) //nolint:gosec,noctx // our own binary, and it must outlive this process
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = supervisorEnv()
	detach(cmd)
	return cmd
}

// supervisorEnv is the floor the supervisor needs, and no more.
//
// An empty environment is not the same as an explicit one: the rule here is
// that a child must never inherit the shell wholesale — an OPENARITY_ value
// in the shell that ran setup would reach the brain and quietly override what
// the installer wrote — and the first version of this took that as far as
// []string{}. `oa` reads its own configuration before it runs a subcommand,
// so the supervisor died on the first line with "$HOME is not defined" and
// setup waited four minutes for something that was never coming.
func supervisorEnv() []string {
	out := systemEnv()
	for _, key := range []string{"PATH", "HOME", "USERPROFILE"} {
		if value := os.Getenv(key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}

// waitForReady is the difference between "it is running" and "it was asked to
// run". /readyz rather than /healthz: healthz answers as soon as the listener
// is up, and the dashboard needs the database behind it.
func waitForReady(ctx context.Context, port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/readyz", port)
	client := &http.Client{Timeout: 3 * time.Second}

	deadline := time.Now().Add(howLongToWaitForReady)
	for {
		if answered(ctx, client, url) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"stack: nothing answered %s within %s — the install is written and `oa stack start` will say why",
				url, howLongToWaitForReady)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func answered(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()

	return res.StatusCode == http.StatusOK
}

// running reports the supervisor already holding this install, if there is
// one. Zero means nothing is.
//
// Two things start an install and both are meant to: setup, and the autostart
// unit at login — which on macOS also fires the moment the agent is loaded,
// which is during setup. Without this they race for the same ports and one of
// them loses in a way nobody sees.
func running(layout engine.Layout) int {
	pid, err := readPID(layout)
	if err != nil || pid == 0 || !alive(pid) {
		return 0
	}
	return pid
}
