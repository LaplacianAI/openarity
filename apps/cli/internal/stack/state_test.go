package stack

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sample() State {
	return State{
		Root:     "/tmp/openarity",
		Versions: Versions{Postgres: "18.6.0", Dex: "v2.45.1", Brain: "v0.2.0"},
		Ports:    Ports{API: 21120, Webhook: 21121, Dex: 5556, Postgres: 21432},
		Arch:     "arm64",
		Binaries: map[string]string{"postgres": "/bin/postgres", "dex": "/bin/dex", "brain": "/bin/brain"},
	}
}

func TestStateRoundTrips(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "stack.yaml")
	want := sample()

	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState() = %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadState() = %+v, want %+v", got, want)
	}
}

// The guard that makes `oa setup` safe to mistype. An install is a Postgres
// cluster with data in it; a second setup that re-ran initdb over the top
// would destroy it, and the person running it would have no reason to expect
// that from a command they had run successfully before.
func TestSaveStateRefusesToOverwriteAnInstall(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "stack.yaml")
	if err := SaveState(path, sample()); err != nil {
		t.Fatalf("SaveState() = %v", err)
	}

	second := sample()
	second.Ports.API = 9999

	err := SaveState(path, second)
	if !errors.Is(err, ErrInstalled) {
		t.Fatalf("SaveState() over an install = %v, want ErrInstalled", err)
	}

	// And it changed nothing. A refusal that still wrote would be worse than
	// no refusal, because the message would say the opposite of what happened.
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() = %v", err)
	}
	if got.Ports.API != 21120 {
		t.Errorf("the refused write changed the state: API port = %d, want 21120", got.Ports.API)
	}
}

// Upgrade has to write over an install by definition, so it uses a different
// function rather than a flag on the same one. A boolean argument at the call
// site would read as `SaveState(path, s, true)` and say nothing.
func TestUpdateStateOverwritesDeliberately(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "stack.yaml")
	if err := SaveState(path, sample()); err != nil {
		t.Fatalf("SaveState() = %v", err)
	}

	upgraded := sample()
	upgraded.Versions.Brain = "v0.3.0"
	if err := UpdateState(path, upgraded); err != nil {
		t.Fatalf("UpdateState() = %v", err)
	}

	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() = %v", err)
	}
	if got.Versions.Brain != "v0.3.0" {
		t.Errorf("Brain version = %q, want the upgraded one", got.Versions.Brain)
	}
}

func TestTheStateFileIsNotWorldReadable(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("file modes are not the access-control mechanism on Windows")
	}
	t.Parallel()

	path := filepath.Join(t.TempDir(), "stack.yaml")
	if err := SaveState(path, sample()); err != nil {
		t.Fatalf("SaveState() = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %04o, want 0600", mode)
	}
}

// Nothing in the state file is a credential, and this asserts it stays that
// way — the file is the obvious place for a future field to be added
// carelessly, and it is the file people will paste into an issue.
func TestTheStateFileHoldsNoCredential(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "stack.yaml")
	if err := SaveState(path, sample()); err != nil {
		t.Fatalf("SaveState() = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	for _, word := range []string{"password", "passphrase", "secret", "token", "hash"} {
		if strings.Contains(strings.ToLower(string(raw)), word) {
			t.Errorf("stack.yaml mentions %q — credentials do not belong in it", word)
		}
	}
}

func TestAMissingStateFileSaysSo(t *testing.T) {
	t.Parallel()

	_, err := LoadState(filepath.Join(t.TempDir(), "absent.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadState() on a missing file = %v, want os.ErrNotExist", err)
	}
}
