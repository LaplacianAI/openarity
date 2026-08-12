package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
)

// These write a real file, so each gets its own config directory. That reads
// the environment, which is why they cannot be t.Parallel().
func isolate(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir) // macOS ignores XDG_CONFIG_HOME
}

// execute drives the whole binary the way a shell does, so a command that is
// built but never registered fails here rather than passing every test
// written about its body.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	root := newRootCmd(&out, &out)
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

// seed runs a command whose failure would make the assertion below
// meaningless rather than merely wrong.
func seed(t *testing.T, args ...string) {
	t.Helper()

	if _, err := execute(t, args...); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
}

func TestSetWritesTheServer(t *testing.T) {
	isolate(t)

	if _, err := execute(t, "config", "set", "server", "https://brain.example.com"); err != nil {
		t.Fatalf("config set server: %v", err)
	}

	saved, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.Active().Server != "https://brain.example.com" {
		t.Errorf("saved server = %q", saved.Active().Server)
	}
}

// A typo must be refused on the way in. Saved happily, it would sit there
// being silently ignored on every run with nothing saying why.
func TestSetRefusesAnUnknownTheme(t *testing.T) {
	isolate(t)

	out, err := execute(t, "config", "set", "theme", "solarized")
	if err == nil {
		t.Fatal("an unknown theme was accepted")
	}
	if !strings.Contains(out+err.Error(), "solarized") {
		t.Errorf("the message does not name the value: %v", err)
	}

	saved, _ := config.Load()
	if saved.Theme != "" {
		t.Errorf("a rejected theme was written anyway: %q", saved.Theme)
	}
}

func TestSetWritesAValidTheme(t *testing.T) {
	isolate(t)

	if _, err := execute(t, "config", "set", "theme", "DARK"); err != nil {
		t.Fatalf("config set theme: %v", err)
	}

	saved, _ := config.Load()
	if saved.Theme != "dark" {
		t.Errorf("saved theme = %q, want it normalised to dark", saved.Theme)
	}
}

// An unknown key is a typo, not a new setting. Accepting it would write a
// file that silently does nothing.
func TestSetRefusesAnUnknownKey(t *testing.T) {
	isolate(t)

	if _, err := execute(t, "config", "set", "colour", "red"); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

// Setting one value must not drop the others. Writing the whole struct from a
// zero value is the obvious way to get this wrong.
func TestSetKeepsTheOtherValues(t *testing.T) {
	isolate(t)

	if _, err := execute(t, "config", "set", "server", "https://brain.example.com"); err != nil {
		t.Fatalf("set server: %v", err)
	}
	if _, err := execute(t, "config", "set", "theme", "light"); err != nil {
		t.Fatalf("set theme: %v", err)
	}

	saved, _ := config.Load()
	if saved.Active().Server != "https://brain.example.com" {
		t.Errorf("the server was lost when the theme was set: %+v", saved)
	}
	if saved.Theme != "light" {
		t.Errorf("saved theme = %q", saved.Theme)
	}
}

func TestUnsetRemovesOnlyItsKey(t *testing.T) {
	isolate(t)

	seed(t, "config", "set", "server", "https://brain.example.com")
	seed(t, "config", "set", "theme", "dark")

	if _, err := execute(t, "config", "unset", "theme"); err != nil {
		t.Fatalf("config unset: %v", err)
	}

	saved, _ := config.Load()
	if saved.Theme != "" {
		t.Errorf("theme survived unset: %q", saved.Theme)
	}
	if saved.Active().Server != "https://brain.example.com" {
		t.Errorf("unset removed the server too: %+v", saved)
	}
}

// The command people run while screen sharing, and paste into issues. The
// token must never be in it.
func TestShowNeverPrintsTheToken(t *testing.T) {
	isolate(t)

	const secret = "oa_live_7f3c9a_do_not_print"
	t.Setenv("OPENARITY_TOKEN", secret)

	out, err := execute(t, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the token is in the output:\n%s", out)
	}
	if !strings.Contains(out, "set") {
		t.Errorf("the output does not say a token is set:\n%s", out)
	}
}

// With four places to set one value, "I set it and it did not take" is
// unanswerable without saying which one won.
func TestShowNamesTheSource(t *testing.T) {
	isolate(t)

	t.Setenv("OPENARITY_SERVER", "https://from-env.example.com")
	seed(t, "config", "set", "server", "https://from-file.example.com")

	out, err := execute(t, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out, "OPENARITY_SERVER") {
		t.Errorf("the output does not name the winning source:\n%s", out)
	}
	if !strings.Contains(out, "https://from-env.example.com") {
		t.Errorf("the output does not carry the effective value:\n%s", out)
	}
}

func TestPathPrintsTheFileLocation(t *testing.T) {
	isolate(t)

	out, err := execute(t, "config", "path")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}

	want, _ := config.Path()
	if !strings.Contains(out, want) {
		t.Errorf("config path printed %q, want %q", out, want)
	}
}

// The subcommands are the contract. One built and never registered passes
// every test written about its body.
func TestEverySubcommandIsRegistered(t *testing.T) {
	isolate(t)

	out, err := execute(t, "config", "--help")
	if err != nil {
		t.Fatalf("config --help: %v", err)
	}
	for _, verb := range []string{"show", "set", "unset", "path"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not registered:\n%s", verb, out)
		}
	}
}
