package stack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFor builds a Setup whose every external step is recorded rather than
// performed. What is under test here is the order, the resumability and what
// reaches disk — not whether initdb works, which is Postgres's problem.
func setupFor(t *testing.T, r *recorder) *Setup {
	t.Helper()

	root := filepath.Join(t.TempDir(), "openarity")
	layout := NewLayout(root)

	// The binaries have to exist: Run resolves all three before it creates
	// anything, so a missing one is reported before there is a cluster on
	// disk to clean up.
	bin := t.TempDir()
	for _, name := range []string{"postgres", "dex", "brain"} {
		executable(t, bin, hostBinary(name))
	}

	return &Setup{
		Layout:   layout,
		Settings: DefaultSettings(),
		Finder:   LocalFinder{Dir: bin},
		Steps: Steps{
			// The stub leaves behind what initdb leaves behind. Resumability
			// is decided by looking at the cluster, not by a progress file
			// that can disagree with the disk.
			InitDB: func(_ context.Context, p Plan) error {
				r.note("initdb")
				return os.WriteFile(filepath.Join(p.Layout.Data, "PG_VERSION"), []byte("18\n"), 0o600)
			},
			CreateDBs:  func(context.Context, Plan) error { r.note("createdbs"); return nil },
			Migrate:    func(context.Context, Plan) error { r.note("migrate"); return nil },
			WriteDex:   func(_ context.Context, p Plan, hash string) error { r.note("dex:" + hash); return nil },
			StartStack: func(context.Context, Plan) error { r.note("start"); return nil },
			Open:       func(string) error { r.note("open"); return nil },
		},
	}
}

func TestSetupRunsItsStepsInOrder(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	if _, err := s.Run(t.Context()); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	got := r.joined()
	// initdb before the databases exist, the databases before the migration,
	// dex's config written before anything is started, and the browser last.
	for _, pair := range [][2]string{
		{"initdb", "createdbs"},
		{"createdbs", "migrate"},
		{"migrate", "start"},
		{"start", "open"},
	} {
		if strings.Index(got, pair[0]) > strings.Index(got, pair[1]) {
			t.Errorf("%s ran after %s, in %q", pair[0], pair[1], got)
		}
	}
}

// The database password never reaches the terminal at all — unlike the
// passphrase, nobody needs to type it. It lives in one 0600 file, and the two
// files a person is most likely to share must not contain it.
func TestTheDatabasePasswordStaysInItsOwnFile(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	if _, err := s.Run(t.Context()); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	secret, err := os.ReadFile(s.Layout.Secret) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}
	password := strings.TrimSpace(string(secret))
	if password == "" {
		t.Fatal("setup wrote an empty database password")
	}

	info, err := os.Stat(s.Layout.Secret)
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the secret file is mode %04o, want 0600", mode)
	}

	raw, err := os.ReadFile(s.Layout.State) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("reading the state: %v", err)
	}
	if strings.Contains(string(raw), password) {
		t.Error("the database password is in stack.yaml")
	}

	entries, err := os.ReadDir(s.Layout.Logs)
	if err != nil {
		t.Fatalf("reading the log directory: %v", err)
	}
	for _, e := range entries {
		logged, err := os.ReadFile(filepath.Join(s.Layout.Logs, e.Name())) //nolint:gosec // a path this test created
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if strings.Contains(string(logged), password) {
			t.Errorf("the database password is in the log file %s", e.Name())
		}
	}
}

// Regenerating it on a resume would leave a cluster whose role has the old
// password and a file holding the new one — an install that starts and then
// cannot authenticate, with nothing saying why.
func TestTheDatabasePasswordSurvivesAResume(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	failing := errors.New("the network went away")
	s.Steps.Migrate = func(context.Context, Plan) error { return failing }

	if _, err := s.Run(t.Context()); !errors.Is(err, failing) {
		t.Fatalf("Run() = %v, want the step's error", err)
	}
	first, err := os.ReadFile(s.Layout.Secret) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}

	s.Steps.Migrate = func(context.Context, Plan) error { return nil }
	if _, err := s.Run(t.Context()); err != nil {
		t.Fatalf("Run() on the second attempt = %v", err)
	}

	second, err := os.ReadFile(s.Layout.Secret) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}
	if string(first) != string(second) {
		t.Error("the second attempt generated a new database password, which the cluster does not have")
	}
}

// The security guard. The passphrase is the one secret setup produces, and
// the log file is what people paste into issues.
func TestThePassphraseReachesTheTerminalAndNothingElse(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	var written string
	s.Steps.WriteDex = func(_ context.Context, p Plan, hash string) error {
		written = hash
		return os.WriteFile(filepath.Join(p.Layout.Dex, "config.yaml"), []byte("hash: "+hash), 0o600)
	}

	result, err := s.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if result.Passphrase == "" {
		t.Fatal("Run() produced no passphrase")
	}

	// It is never the thing stored. dex holds a bcrypt hash, and a hash that
	// equals its input is not a hash.
	if written == result.Passphrase {
		t.Fatal("the passphrase itself was handed to dex rather than a hash of it")
	}

	for _, path := range []string{
		filepath.Join(s.Layout.Dex, "config.yaml"),
		s.Layout.State,
	} {
		raw, err := os.ReadFile(path) //nolint:gosec // paths this test created
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if strings.Contains(string(raw), result.Passphrase) {
			t.Errorf("the passphrase is in %s", path)
		}
	}

	// And not in any log the install wrote. This is the one that matters:
	// openarity.log is what a person attaches to a bug report.
	entries, err := os.ReadDir(s.Layout.Logs)
	if err != nil {
		t.Fatalf("reading the log directory: %v", err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(s.Layout.Logs, e.Name())) //nolint:gosec // paths this test created
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if strings.Contains(string(raw), result.Passphrase) {
			t.Errorf("the passphrase is in the log file %s", e.Name())
		}
	}
}

func TestEveryPassphraseIsDifferent(t *testing.T) {
	r := &recorder{}

	first, err := setupFor(t, r).Run(t.Context())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	second, err := setupFor(t, r).Run(t.Context())
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if first.Passphrase == second.Passphrase {
		t.Error("two installs produced the same passphrase")
	}
}

// A laptop sleeps and wifi drops, so an interrupted setup is the common case
// rather than the exception. Re-running must continue rather than fail, and
// must not start over — initdb over an existing cluster destroys it.
func TestSetupResumesAfterAFailedStep(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	failing := errors.New("the network went away")
	s.Steps.Migrate = func(context.Context, Plan) error {
		r.note("migrate")
		return failing
	}

	if _, err := s.Run(t.Context()); !errors.Is(err, failing) {
		t.Fatalf("Run() = %v, want the step's error", err)
	}

	// Nothing was recorded, because the install is not finished.
	if s.Layout.Installed() {
		t.Error("a failed setup wrote the state file")
	}

	r.events = nil
	s.Steps.Migrate = func(context.Context, Plan) error { r.note("migrate"); return nil }

	if _, err := s.Run(t.Context()); err != nil {
		t.Fatalf("Run() on the second attempt = %v", err)
	}
	if got := r.joined(); strings.Contains(got, "initdb") {
		t.Errorf("the second attempt ran initdb again, over an existing cluster: %q", got)
	}
	if !s.Layout.Installed() {
		t.Error("the completed setup wrote no state file")
	}
}

// The refusal that makes `oa setup` safe to mistype.
func TestSetupRefusesToRunOverAFinishedInstall(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	if _, err := s.Run(t.Context()); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	r.events = nil
	_, err := s.Run(t.Context())
	if !errors.Is(err, ErrInstalled) {
		t.Fatalf("Run() over an install = %v, want ErrInstalled", err)
	}
	if got := r.joined(); got != "" {
		t.Errorf("the refused run still did work: %q", got)
	}
}

// A missing binary is found before anything is created. Discovering it after
// initdb has made a cluster means the person has to clean up a half install
// before they can retry.
func TestAMissingBinaryIsFoundBeforeAnythingIsCreated(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)
	s.Finder = LocalFinder{Dir: t.TempDir()}
	t.Setenv("PATH", t.TempDir())

	if _, err := s.Run(t.Context()); err == nil {
		t.Fatal("Run() with no binaries = nil, want an error")
	}
	if got := r.joined(); got != "" {
		t.Errorf("work was done before the binaries were resolved: %q", got)
	}
}

// Ports are chosen by binding, and recorded, so `oa start` uses the same ones
// the install was built with rather than choosing again.
// oa stack start runs in a later process with no --bin-dir, so the paths the
// install was actually built against have to survive in the state file.
// Without them start resolves the install's own bin/ and finds nothing.
func TestTheResolvedBinariesAreRecorded(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	if _, err := s.Run(t.Context()); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	state, err := LoadState(s.Layout.State)
	if err != nil {
		t.Fatalf("LoadState() = %v", err)
	}
	for _, name := range required {
		if state.Binaries[name] == "" {
			t.Errorf("the path to %s was not recorded", name)
		}
	}
}

func TestTheChosenPortsAreRecorded(t *testing.T) {
	r := &recorder{}
	s := setupFor(t, r)

	if _, err := s.Run(t.Context()); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	state, err := LoadState(s.Layout.State)
	if err != nil {
		t.Fatalf("LoadState() = %v", err)
	}
	for name, port := range map[string]int{
		"api": state.Ports.API, "webhook": state.Ports.Webhook,
		"dex": state.Ports.Dex, "postgres": state.Ports.Postgres,
	} {
		if port == 0 {
			t.Errorf("the %s port was not recorded", name)
		}
	}
	seen := map[int]string{}
	for name, port := range map[string]int{
		"api": state.Ports.API, "webhook": state.Ports.Webhook,
		"dex": state.Ports.Dex, "postgres": state.Ports.Postgres,
	} {
		if other, taken := seen[port]; taken {
			t.Errorf("%s and %s were both given port %d", name, other, port)
		}
		seen[port] = name
	}
}
