package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
)

// Every failure is prefixed, because a bare sentence on stderr is
// indistinguishable from output of whatever else is running in that shell.
func TestTheErrorIsPrefixedWithTheBinaryName(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printError(&buf, "", errors.New("not authenticated"))

	if !strings.HasPrefix(buf.String(), "oa: ") {
		t.Errorf("the error is not prefixed: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "not authenticated") {
		t.Errorf("the message was lost: %q", buf.String())
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("the error does not end in a newline: %q", buf.String())
	}
}

// The styles are built against the writer being printed to. Redirecting
// stderr to a file must produce a plain sentence — an error is the line most
// likely to be pasted into an issue, and escape sequences make it unreadable.
func TestTheErrorIsPlainOnANonTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printError(&buf, "", errors.New("not authenticated"))

	if strings.Contains(buf.String(), "\x1b") {
		t.Errorf("an escape sequence reached a non-terminal writer: %q", buf.String())
	}
}

// The theme override has to reach this path too. It is the one output a user
// sees when nothing else worked, so it must not be the one place that queries
// the terminal and stalls.
//
// It reads the variable rather than a resolved setting on purpose: this also
// prints failures from resolving settings, including a config file that will
// not parse, so there may be no readable file to take a theme from.
func TestTheErrorHonoursTheThemeVariable(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"light", "dark", "auto", "solarized", ""} {
		var buf bytes.Buffer
		printError(&buf, name, errors.New("not authenticated"))

		if !strings.Contains(buf.String(), "not authenticated") {
			t.Errorf("OPENARITY_THEME=%q: the message was lost: %q", name, buf.String())
		}
		if !strings.HasPrefix(buf.String(), "oa: ") {
			t.Errorf("OPENARITY_THEME=%q: the error is not prefixed: %q", name, buf.String())
		}
	}
}

// A wrapped error can carry a newline — a YAML parse failure does. The
// prefix belongs to the first line, and the rest must not lose it silently by
// being printed as an unrelated block.
func TestAMultiLineErrorKeepsItsPrefix(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printError(&buf, "", errors.New("parse config:\nline 1: bad"))

	first, _, found := strings.Cut(buf.String(), "\n")
	if !found {
		t.Fatalf("expected more than one line: %q", buf.String())
	}
	if !strings.HasPrefix(first, "oa: ") {
		t.Errorf("the first line is not prefixed: %q", first)
	}
}

// Nothing outside this package knows the whole command list, so this is the
// only place the real root can be checked. A command built and registered in
// its own package's tests but left out of commands() would pass everything
// else and ship missing.
func TestEveryCommandIsRegistered(t *testing.T) {
	root := cli.NewRoot(io.Discard, io.Discard, commands)
	_ = root

	registered := map[string]bool{}
	for _, cmd := range root.Commands() {
		registered[cmd.Name()] = true
	}

	for _, want := range []string{"whoami", "config", "context", "teams"} {
		if !registered[want] {
			t.Errorf("%q is not on the root: %v", want, registered)
		}
	}
}

// run is the whole program minus os.Exit: it wires the signal handler, builds
// the root and executes. Nothing else drives it, so a change that broke
// argument handling or writer wiring would only be found by running the binary
// by hand.
func TestRunWritesToTheWritersItIsGiven(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := run(t.Context(), []string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run --help: %v", err)
	}

	if stdout.Len() == 0 && stderr.Len() == 0 {
		t.Fatal("run wrote nothing to either writer")
	}
	combined := stdout.String() + stderr.String()
	for _, want := range []string{"oa talks to a brain", "Available Commands", "login", "users"} {
		if !strings.Contains(combined, want) {
			t.Errorf("help does not mention %q:\n%s", want, combined)
		}
	}
}

// The exit code comes from this error, so an unknown command has to return one
// rather than printing a suggestion and succeeding — a script would carry on.
func TestRunReturnsAnErrorForAnUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(t.Context(), []string{"nonsense"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("an unknown command exited zero")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("the error does not name what was typed: %v", err)
	}
}

// Nothing may reach os.Stdout directly: a test cannot observe it, and neither
// can anything redirecting output to a file.
func TestRunSendsNothingToTheProcessStreams(t *testing.T) {
	real := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = real })

	var stdout, stderr bytes.Buffer
	_ = run(t.Context(), []string{"--help"}, &stdout, &stderr)

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(leaked) != 0 {
		t.Errorf("run wrote to os.Stdout directly: %q", leaked)
	}
}
