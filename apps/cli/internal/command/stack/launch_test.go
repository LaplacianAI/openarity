package stack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

// The step setup calls "Starting Openarity" was stopPostgres, which starts
// nothing. Nothing said so: no test asked what the step did, only that setup
// called it. Setup then printed a URL, opened a browser at it, and the window
// offered "Open the dashboard" for a port with nothing behind it.
func TestTheStepThatStartsTheStackIsNotTheOneThatStopsPostgres(t *testing.T) {
	t.Parallel()

	steps := realSteps(nil, engine.Platform{GOOS: "darwin", GOARCH: "arm64"})
	if steps.StartStack == nil {
		t.Fatal("realSteps() wires no StartStack")
	}

	// Funcs cannot be compared with ==, but their code pointers can, and this
	// is the one comparison that catches the original: StartStack was
	// stopPostgres itself.
	if reflect.ValueOf(steps.StartStack).Pointer() == reflect.ValueOf(stopPostgres).Pointer() {
		t.Error("the step setup calls \"Starting Openarity\" is stopPostgres, which starts nothing")
	}
	if reflect.ValueOf(steps.StartStack).Pointer() != reflect.ValueOf(startStack).Pointer() {
		t.Error("StartStack is wired to something other than startStack")
	}
}

// Setup used to say "Openarity is running" the moment it had written the
// state file, which was true of nothing.
func TestWaitingForReadyAnswersOnlyWhenTheBrainDoes(t *testing.T) {
	t.Parallel()

	// Counted atomically: the handler runs on the server's goroutine and the
	// assertions on this one, and the race detector is right about that.
	var asked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := asked.Add(1)
		if r.URL.Path != "/readyz" {
			t.Errorf("asked for %s, want /readyz — healthz answers before the database does", r.URL.Path)
		}
		// What a cold start actually looks like from outside: the listener
		// is not up yet and something else answers (404), then the brain is
		// up but the database is not (503), then ready. Only the last of
		// those is running — a check that accepted anything below 500 would
		// call the first one a success.
		switch n {
		case 1:
			w.WriteHeader(http.StatusNotFound)
			return
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	port, err := strconv.Atoi(server.URL[strings.LastIndex(server.URL, ":")+1:])
	if err != nil {
		t.Fatalf("the test server's port: %v", err)
	}

	start := time.Now()
	if err := waitForReady(t.Context(), port); err != nil {
		t.Fatalf("waitForReady() = %v, want it to wait rather than give up", err)
	}
	if got := asked.Load(); got < 3 {
		t.Errorf("asked %d times, want it to keep asking until the answer changed", got)
	}
	if time.Since(start) > howLongToWaitForReady {
		t.Error("waitForReady() took longer than it promises to")
	}
}

// A port nobody is listening on must end in a sentence rather than a hang.
func TestWaitingForReadyGivesUpAndSaysWhatToRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	err := waitForReady(ctx, 1)
	if err == nil {
		t.Fatal("waitForReady() on a dead port = nil")
	}
}

// A supervisor that is already holding the install is not an error: loading
// the launch agent starts one during setup, and setup started one itself.
func TestAnInstallAlreadyHeldIsRecognised(t *testing.T) {
	t.Parallel()

	layout := engine.NewLayout(t.TempDir())
	if err := os.MkdirAll(layout.Root, 0o700); err != nil {
		t.Fatal(err)
	}

	if got := running(layout); got != 0 {
		t.Errorf("running() with no pid file = %d, want 0", got)
	}

	// Our own pid is the one process we know is alive.
	if err := os.WriteFile(filepath.Join(layout.Root, pidFile),
		[]byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := running(layout); got != os.Getpid() {
		t.Errorf("running() = %d, want this process %d", got, os.Getpid())
	}

	// A pid nothing owns. 0x7FFFFFFF is above every platform's pid_max.
	if err := os.WriteFile(filepath.Join(layout.Root, pidFile),
		[]byte("2147483647"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := running(layout); got != 0 {
		t.Errorf("running() with a stale pid = %d, want 0 so a crashed install can be started again", got)
	}
}

// Neither the stop nor the spawn is real here: this is about what happens
// between them and after.
func nothingToStop(context.Context, engine.Plan) error { return nil }

func planFor(t *testing.T, port int) engine.Plan {
	t.Helper()

	return engine.Plan{
		Layout:   engine.NewLayout(t.TempDir()),
		Binaries: map[string]string{},
		Ports:    engine.Ports{API: port},
	}
}

// The whole point of the step: it is not done when the supervisor has been
// spawned, it is done when the brain answers. A spawn that brings nothing up
// must not report success — that is exactly what setup did before, and the
// person who clicked the button it offered got "refused to connect".
func TestStartingIsNotDoneUntilTheBrainAnswers(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	plan := planFor(t, 1)

	spawned := false
	err := launch(ctx, plan, nothingToStop, func(engine.Plan) error {
		spawned = true
		return nil
	})

	if !spawned {
		t.Error("launch() never spawned anything")
	}
	if err == nil {
		t.Error("launch() = nil with nothing listening, so setup would say it is running")
	}
}

// And when something does come up, it stops waiting and says so.
func TestStartingSucceedsOnceSomethingIsListening(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	port, err := strconv.Atoi(server.URL[strings.LastIndex(server.URL, ":")+1:])
	if err != nil {
		t.Fatalf("the test server's port: %v", err)
	}

	plan := planFor(t, port)

	if err := launch(t.Context(), plan, nothingToStop, func(engine.Plan) error { return nil }); err != nil {
		t.Errorf("launch() = %v, want it to finish once the brain answers", err)
	}
}

// An explicit environment is not an empty one. The rule is that a child must
// never inherit the shell wholesale — an OPENARITY_ value in it would reach
// the brain and override what the installer wrote — and the first version of
// this took that as far as []string{}: the supervisor died on its first line
// with "$HOME is not defined", and setup waited four minutes for something
// that was never coming.
func TestTheSupervisorGetsAnEnvironmentItCanStartIn(t *testing.T) {
	// No t.Parallel: t.Setenv.
	t.Setenv("OPENARITY_POSTGRES_DSN", "postgres://someone:hunter2@example.com/db")
	t.Setenv("HOME", "/home/someone")

	// Read off the command that would have been started, not off the helper,
	// so setting cmd.Env back to []string{} is caught here too.
	cmd := supervisorCommand("/opt/openarity/oa", "/an/install", io.Discard)

	if got, want := cmd.Args, []string{"/opt/openarity/oa", "stack", "start", "--root", "/an/install"}; !slices.Equal(got, want) {
		t.Errorf("the supervisor is started as %v, want %v", got, want)
	}

	env := cmd.Env
	if len(env) == 0 {
		t.Fatal("the supervisor is given an empty environment, which is how it died before it ran")
	}

	var home bool
	for _, entry := range env {
		if strings.HasPrefix(entry, "OPENARITY_") {
			t.Errorf("supervisorEnv() carries %q from the shell", entry)
		}
		if entry == "HOME=/home/someone" {
			home = true
		}
	}
	if !home {
		t.Error("supervisorEnv() sets no HOME, and oa reads its own config before it runs anything")
	}
}

// The stop is injectable so the tests above need no pg_ctl, which means
// nothing else notices if the real one stops being passed in. This does:
// with no binaries, stopPostgres cannot find pg_ctl and says so, and that
// error can only come from the step still being there.
func TestStartingStillStopsTheClusterTheInstallUsed(t *testing.T) {
	t.Parallel()

	err := startStack(t.Context(), planFor(t, 1))
	if err == nil {
		t.Fatal("startStack() with no pg_ctl = nil")
	}
	if !strings.Contains(err.Error(), "pg_ctl") {
		t.Errorf("startStack() = %v, want it to have tried to stop the cluster first", err)
	}
}
