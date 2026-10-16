package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
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

// --- graph ---

const someTeam = "3f2c1b6a-9d4e-4f70-8a55-0c1d2e3f4a5b"

// No team means every team, and it is said by leaving the flag off, never by
// a value of it.
func TestParseGraphRebuildOfEveryTeam(t *testing.T) {
	t.Parallel()

	cmd, err := parse([]string{"graph", "rebuild"})
	if err != nil {
		t.Fatalf("graph rebuild rejected: %v", err)
	}
	if cmd.name != commandGraph || cmd.team != uuid.Nil || cmd.direction != "" {
		t.Errorf("graph rebuild parsed as %+v", cmd)
	}
}

func TestParseGraphRebuildOfOneTeam(t *testing.T) {
	t.Parallel()

	cmd, err := parse([]string{"graph", "rebuild", "--team", someTeam})
	if err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if cmd.name != commandGraph || cmd.team != uuid.MustParse(someTeam) {
		t.Errorf("parsed as %+v", cmd)
	}
}

// A rebuild drops a graph before it builds one. Anything that is not exactly
// one of the two forms is refused: an argument this cannot read must never
// widen "this team" into "every team".
func TestParseRejectsEveryOtherGraphCommand(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"graph"},
		{"graph", ""},
		{"graph", "Rebuild"},
		{"graph", "drop"},
		{"graph", "--team", someTeam},
		{"graph", "rebuild", "--team"},
		{"graph", "rebuild", "--team", ""},
		{"graph", "rebuild", "--team", "acme"},
		{"graph", "rebuild", "--team", someTeam[:35]},
		{"graph", "rebuild", "--team", someTeam, "extra"},
		{"graph", "rebuild", "--team", someTeam, "--team", someTeam},
		{"graph", "rebuild", "--teams", someTeam},
		{"graph", "rebuild", "-team", someTeam},
		{"graph", "rebuild", "--team=" + someTeam},
		{"graph", "rebuild", someTeam},
		{"graph", "rebuild", "--all"},
		{"graph", "rebuild", "extra"},
		// The id of no team. Taken as a value it would mean every team.
		{"graph", "rebuild", "--team", uuid.Nil.String()},
	} {
		cmd, err := parse(args)
		if err == nil {
			t.Errorf("%q accepted as %+v", args, cmd)
			continue
		}
		if !errors.Is(err, errGraphUsage) || !strings.Contains(err.Error(), "brain graph rebuild [--team <id>]") {
			t.Errorf("%q gave no usage: %v", args, err)
		}
		if cmd != (command{}) {
			t.Errorf("%q returned a usable command with its error: %+v", args, cmd)
		}
	}
}

// uuid.Parse reads more than the one familiar form. Each of these is the
// same id written another way, so accepting them widens nothing; pinned so
// that it is a decision.
func TestATeamIDMayBeWrittenAnyWayAUUIDIs(t *testing.T) {
	t.Parallel()

	for _, written := range []string{
		strings.ToUpper(someTeam),
		"urn:uuid:" + someTeam,
		"{" + someTeam + "}",
		strings.ReplaceAll(someTeam, "-", ""),
	} {
		cmd, err := parse([]string{"graph", "rebuild", "--team", written})
		if err != nil || cmd.team != uuid.MustParse(someTeam) {
			t.Errorf("%q parsed as %v, %v", written, cmd.team, err)
		}
	}
	for _, written := range []string{" " + someTeam, someTeam + " ", someTeam + "0", "0x" + strings.ReplaceAll(someTeam, "-", "")} {
		if cmd, err := parse([]string{"graph", "rebuild", "--team", written}); err == nil {
			t.Errorf("%q accepted as %v", written, cmd.team)
		}
	}
}

// What was typed is in the error, so a bad id is seen for what it is.
func TestABadTeamIDIsQuotedBack(t *testing.T) {
	t.Parallel()

	_, err := parse([]string{"graph", "rebuild", "--team", "acme"})
	if err == nil || !strings.Contains(err.Error(), `"acme"`) {
		t.Errorf("err = %v", err)
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

	for _, name := range []commandName{commandServe, commandMigrate, commandReap, commandWorker, commandGraph} {
		if !strings.Contains(err.Error(), string(name)) {
			t.Errorf("%q is a command and the error does not mention it: %v", name, err)
		}
	}
}
