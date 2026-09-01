package main

import (
	"strings"
	"testing"
)

// No arguments means serve, so the container image needs no explicit command
// and `make run` keeps working.
func TestParseDefaultsToServe(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {}} {
		cmd, err := parse(args)
		if err != nil {
			t.Errorf("%v rejected: %v", args, err)
			continue
		}
		if cmd.name != commandServe {
			t.Errorf("%v parsed as %q, want serve", args, cmd.name)
		}
	}
}

func TestParseServe(t *testing.T) {
	t.Parallel()

	cmd, err := parse([]string{"serve"})
	if err != nil {
		t.Fatalf("serve rejected: %v", err)
	}
	if cmd.name != commandServe {
		t.Errorf("name = %q, want serve", cmd.name)
	}
	if cmd.direction != "" {
		t.Errorf("serve carried a direction: %q", cmd.direction)
	}
}

func TestParseMigrateDirections(t *testing.T) {
	t.Parallel()

	for arg, want := range map[string]direction{
		"up":   directionUp,
		"down": directionDown,
	} {
		cmd, err := parse([]string{"migrate", arg})
		if err != nil {
			t.Errorf("migrate %s rejected: %v", arg, err)
			continue
		}
		if cmd.name != commandMigrate {
			t.Errorf("migrate %s parsed as %q", arg, cmd.name)
		}
		if cmd.direction != want {
			t.Errorf("migrate %s gave direction %q, want %q", arg, cmd.direction, want)
		}
	}
}

// A typo in a Kubernetes Job — args: ["migrat", "up"] — must stop the process.
// Falling through to serve would start a second copy of the API instead of
// migrating, and it would look healthy doing it.
func TestParseRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"migrat", "up"},
		{"Serve"},   // case matters
		{"--help"},  // no flag parsing exists
		{""},        // an empty argument is not "no argument"
		{"workers"}, // a role's name, pluralised, is not that role
	} {
		cmd, err := parse(args)
		if err == nil {
			t.Errorf("%q accepted as %q", args, cmd.name)
			continue
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%q gave an unhelpful error: %v", args, err)
		}
	}
}

// A missing direction must be an error, not a default. `brain migrate` in a
// shell history should never turn out to have been a schema change.
func TestParseRejectsMigrateWithoutADirection(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"migrate"},
		{"migrate", ""},
		{"migrate", "sideways"},
		{"migrate", "UP"}, // case matters
	} {
		cmd, err := parse(args)
		if err == nil {
			t.Errorf("%q accepted as direction %q", args, cmd.direction)
			continue
		}
		if !strings.Contains(err.Error(), "usage") {
			t.Errorf("%q gave no usage message: %v", args, err)
		}
	}
}

// Trailing arguments are currently ignored rather than rejected. Pinned so the
// behaviour is a decision rather than an accident — if parse starts rejecting
// them, this test says so.
func TestParseIgnoresTrailingArguments(t *testing.T) {
	t.Parallel()

	cmd, err := parse([]string{"migrate", "up", "unexpected"})
	if err != nil {
		t.Fatalf("trailing arguments are now rejected: %v", err)
	}
	if cmd.direction != directionUp {
		t.Errorf("direction = %q, want up", cmd.direction)
	}
}

// parse must not need config, a database, or the environment. That is what
// lets a bad argument fail instantly instead of after a connect timeout.
func TestParseTouchesNothing(t *testing.T) {
	t.Parallel()

	// No stubEnv, no t.Setenv, no database. If parse ever grows a dependency
	// on any of those, this test fails or hangs.
	if _, err := parse([]string{"migrate", "up"}); err != nil {
		t.Fatalf("parse needs something set up: %v", err)
	}
}

// Errors are returned with a zero command, so a caller that ignores the error
// cannot accidentally run something.
func TestParseReturnsAZeroCommandOnError(t *testing.T) {
	t.Parallel()

	cmd, err := parse([]string{"nonsense"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if cmd.name != "" || cmd.direction != "" {
		t.Errorf("error returned a usable command: %+v", cmd)
	}
}

// The error a typo produces has to list what is actually accepted. It is the
// only place a person learns the command names, and it is a string nothing
// else checks — `brain reapp` said "want serve or migrate" for a while after
// reap existed, which reads as "reap is not a thing" rather than as a typo.
func TestTheUnknownCommandErrorNamesEveryCommand(t *testing.T) {
	t.Parallel()

	_, err := parse([]string{"nonsense"})
	if err == nil {
		t.Fatal("parse accepted a command that does not exist")
	}

	for _, name := range []commandName{commandServe, commandMigrate, commandReap, commandWorker} {
		if !strings.Contains(err.Error(), string(name)) {
			t.Errorf("%q is a command and the error does not mention it: %v", name, err)
		}
	}
}
