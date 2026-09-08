package stack_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
	cmdstack "github.com/LaplacianAI/openarity/apps/cli/internal/command/stack"
	"github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

func commands() []clitest.Build {
	return []clitest.Build{cmdstack.New}
}

// A verb missing from AddCommand gets no linter warning, so the help output is
// what proves every one is reachable.
func TestEveryVerbIsRegistered(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands(), "stack", "--help")
	if err != nil {
		t.Fatalf("stack --help = %v", err)
	}
	for _, verb := range []string{"setup", "start", "stop", "status"} {
		if !strings.Contains(out, verb) {
			t.Errorf("stack --help does not name %q:\n%s", verb, out)
		}
	}
}

// install writes a finished install into a temporary root and returns it.
func install(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "openarity")
	layout := stack.NewLayout(root)
	if err := layout.Create(); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if err := stack.SaveState(layout.State, stack.State{
		Root:     root,
		Versions: stack.Versions{Postgres: "18.6.0", Dex: "v2.45.1", Brain: "local"},
		Ports:    stack.Ports{API: 21120, Webhook: 21121, Dex: 5556, Postgres: 21432},
		Arch:     "arm64",
	}); err != nil {
		t.Fatalf("SaveState() = %v", err)
	}
	return root
}

// The whole reason `oa status` takes --json: sub-project B parses it, and a
// table with styling in it is not something to parse.
func TestStatusPrintsJSONThatParses(t *testing.T) {
	clitest.Isolate(t)
	root := install(t)

	out, err := clitest.Execute(t, commands(), "stack", "status", "--root", root, "-o", "json")
	if err != nil {
		t.Fatalf("stack status = %v", err)
	}

	var views []struct {
		Name    string `json:"name"`
		Running bool   `json:"running"`
		PID     int    `json:"pid"`
		Port    int    `json:"port"`
	}
	if err := json.Unmarshal([]byte(out), &views); err != nil {
		t.Fatalf("status did not print JSON: %v\n%s", err, out)
	}
	if len(views) == 0 {
		t.Fatal("status printed an empty array for an install that exists")
	}
	for _, v := range views {
		if v.Running {
			t.Errorf("%s is reported running when nothing was started", v.Name)
		}
	}
}

// A nil slice marshals to null, and `jq length` fails on null.
//
// This cannot fail today and the test is kept deliberately: componentNames is
// a fixed list of four, so the loop always appends and the slice is never nil
// whichever way it was built. Replacing the make() with a var declaration
// changes nothing, which was confirmed by doing it. The test earns its place
// the moment the components come from the state file rather than a constant —
// an install that recorded none would then print null and break every
// consumer that pipes this to jq.
func TestStatusPrintsAnArrayAndNotNull(t *testing.T) {
	clitest.Isolate(t)
	root := install(t)

	out, err := clitest.Execute(t, commands(), "stack", "status", "--root", root, "-o", "json")
	if err != nil {
		t.Fatalf("stack status = %v", err)
	}
	if strings.Contains(out, "null") {
		t.Errorf("status printed null:\n%s", out)
	}
}

func TestEveryFormatPrintsStatus(t *testing.T) {
	clitest.Isolate(t)
	root := install(t)

	for _, format := range []string{"table", "json", "yaml"} {
		out, err := clitest.Execute(t, commands(), "stack", "status", "--root", root, "-o", format)
		if err != nil {
			t.Errorf("stack status -o %s = %v", format, err)
			continue
		}
		if !strings.Contains(out, "postgres") {
			t.Errorf("stack status -o %s did not name postgres:\n%s", format, out)
		}
	}
}

// Not "no such file or directory" from somewhere three layers down. The person
// has not run setup, and that is what they should be told.
func TestACommandOnNoInstallSaysToRunSetup(t *testing.T) {
	clitest.Isolate(t)
	root := filepath.Join(t.TempDir(), "nothing")

	for _, verb := range []string{"status", "start", "stop"} {
		_, err := clitest.Execute(t, commands(), "stack", verb, "--root", root)
		if err == nil {
			t.Errorf("stack %s on no install = nil, want an error", verb)
			continue
		}
		if !strings.Contains(err.Error(), "oa stack setup") {
			t.Errorf("stack %s = %q, want it to name the command that fixes it", verb, err)
		}
	}
}

// The refusal has to reach the person as a sentence rather than as a wrapped
// sentinel nobody prints.
func TestSetupOverAnInstallSaysItIsAlreadyInstalled(t *testing.T) {
	clitest.Isolate(t)
	root := install(t)

	_, err := clitest.Execute(t, commands(), "stack", "setup", "--root", root)
	if err == nil {
		t.Fatal("stack setup over an install = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "already installed") {
		t.Errorf("stack setup = %q, want it to say the install exists", err)
	}
	if !strings.Contains(err.Error(), "oa stack start") {
		t.Errorf("stack setup = %q, want it to name what to run instead", err)
	}
}

// Every argument is checked. Without cobra.NoArgs a typo is silently ignored,
// and `oa stack status --json` — the flag a person will reach for first —
// would look like it worked.
func TestExtraArgumentsAreRefused(t *testing.T) {
	clitest.Isolate(t)
	root := install(t)

	for _, verb := range []string{"setup", "start", "stop", "status"} {
		if _, err := clitest.Execute(t, commands(), "stack", verb, "--root", root, "unexpected"); err == nil {
			t.Errorf("stack %s accepted an unexpected argument", verb)
		}
	}
}

// The state file is not a credential store and the status output must not
// become one either.
func TestNoFormatPrintsAnythingSecret(t *testing.T) {
	clitest.Isolate(t)
	root := install(t)

	for _, format := range []string{"table", "json", "yaml"} {
		out, err := clitest.Execute(t, commands(), "stack", "status", "--root", root, "-o", format)
		if err != nil {
			t.Fatalf("stack status -o %s = %v", format, err)
		}
		for _, word := range []string{"password", "passphrase", "secret", "token"} {
			if strings.Contains(strings.ToLower(out), word) {
				t.Errorf("status -o %s mentions %q:\n%s", format, word, out)
			}
		}
	}
}

func TestStatusReportsTheRecordedPorts(t *testing.T) {
	clitest.Isolate(t)
	root := install(t)

	out, err := clitest.Execute(t, commands(), "stack", "status", "--root", root, "-o", "json")
	if err != nil {
		t.Fatalf("stack status = %v", err)
	}
	// The ports the install was built with, not the defaults chosen again.
	// `oa start` binds these, so status showing anything else would send
	// somebody to the wrong address.
	if !strings.Contains(out, "21432") {
		t.Errorf("status did not report the recorded Postgres port:\n%s", out)
	}
}

func TestTheRootFlagDefaultsToThePlatformDirectory(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands(), "stack", "status", "--help")
	if err != nil {
		t.Fatalf("stack status --help = %v", err)
	}
	if !strings.Contains(out, "--root") {
		t.Errorf("stack status --help does not document --root:\n%s", out)
	}
	// It must not print a path from the machine that built the binary.
	if strings.Contains(out, os.TempDir()) {
		t.Errorf("the --root default leaked a build-time path:\n%s", out)
	}
}
