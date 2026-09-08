package stack

import (
	"path/filepath"
	"strings"
	"testing"
)

const (
	exe  = "/opt/openarity/oa"
	root = "/data/openarity"
	home = "/home/person"
)

func unitFor(t *testing.T, goos string) Unit {
	t.Helper()

	u, err := UnitFor(goos, home, exe, root)
	if err != nil {
		t.Fatalf("UnitFor(%s) = %v", goos, err)
	}
	return u
}

// None of these run with a useful working directory, so a relative path in a
// unit is a unit that starts nothing and reports success doing it.
func TestEveryUnitNamesTheBinaryByAbsolutePath(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"darwin", "linux", "windows"} {
		u := unitFor(t, goos)
		text := u.Content + " " + strings.Join(u.Register, " ")

		if !strings.Contains(text, exe) {
			t.Errorf("%s: the unit does not name %q:\n%s", goos, exe, text)
		}
	}
}

// An install under a non-default --root has to be started with that same
// --root. Without it the reboot unit starts a different install, or none.
func TestEveryUnitCarriesTheInstallRoot(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"darwin", "linux", "windows"} {
		u := unitFor(t, goos)
		text := u.Content + " " + strings.Join(u.Register, " ")

		if !strings.Contains(text, root) {
			t.Errorf("%s: the unit does not carry the install root:\n%s", goos, text)
		}
		if !strings.Contains(text, "start") {
			t.Errorf("%s: the unit does not run `stack start`:\n%s", goos, text)
		}
	}
}

// All three are per-user and need no administrator. That is the property that
// makes the Windows case affordable at all: a Service would want elevation, a
// different privilege model and a signed binary.
func TestNoUnitNeedsAdministrator(t *testing.T) {
	t.Parallel()

	darwin := unitFor(t, "darwin")
	if !strings.Contains(darwin.Path, filepath.Join(home, "Library", "LaunchAgents")) {
		t.Errorf("the launchd unit goes to %q, want a per-user LaunchAgents path", darwin.Path)
	}
	if strings.Contains(darwin.Path, "LaunchDaemons") {
		t.Error("the launchd unit is a system daemon, which needs root to install")
	}

	linux := unitFor(t, "linux")
	if !strings.Contains(strings.Join(linux.Register, " "), "--user") {
		t.Errorf("the systemd unit is not registered with --user: %v", linux.Register)
	}

	windows := unitFor(t, "windows")
	joined := strings.Join(windows.Register, " ")
	if !strings.Contains(joined, "schtasks") || !strings.Contains(strings.ToUpper(joined), "ONLOGON") {
		t.Errorf("the Windows unit is not a logon-triggered scheduled task: %v", windows.Register)
	}
	if strings.Contains(strings.ToLower(joined), "sc create") {
		t.Error("the Windows unit creates a service, which needs elevation")
	}
}

// Every unit must be removable by the same code that installed it, or an
// uninstall leaves something that starts a stack whose files are gone.
func TestEveryUnitCanBeUndone(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"darwin", "linux", "windows"} {
		u := unitFor(t, goos)
		if u.Path == "" && len(u.Unregister) == 0 {
			t.Errorf("%s: the unit can be installed but not removed", goos)
		}
	}
}

func TestAnUnknownPlatformIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()

	if _, err := UnitFor("plan9", home, exe, root); err == nil {
		t.Error("UnitFor(plan9) = nil, want an error rather than an invented unit")
	}
}

// A path with a space is the common case on Windows and not rare on macOS.
// Asserting the path merely appears is not enough — it appears whether or not
// it is quoted, which is how the first version of this test passed against an
// unquoted ExecStart and an unquoted schtasks /tr.
func TestASpacedPathIsQuotedWhereTheReaderSplitsOnWhitespace(t *testing.T) {
	t.Parallel()

	spaced := `/Users/First Last/Library/Application Support/openarity`
	spacedExe := `/Applications/Open arity/oa`

	// systemd splits ExecStart on whitespace, so both values need quoting.
	linux, err := UnitFor("linux", home, spacedExe, spaced)
	if err != nil {
		t.Fatalf("UnitFor(linux) = %v", err)
	}
	for _, want := range []string{`"` + spacedExe + `"`, `"` + spaced + `"`} {
		if !strings.Contains(linux.Content, want) {
			t.Errorf("the systemd unit does not quote %s:\n%s", want, linux.Content)
		}
	}

	// schtasks /tr takes one string that Windows then splits.
	windows, err := UnitFor("windows", home, spacedExe, spaced)
	if err != nil {
		t.Fatalf("UnitFor(windows) = %v", err)
	}
	tr := ""
	for i, arg := range windows.Register {
		if arg == "/tr" && i+1 < len(windows.Register) {
			tr = windows.Register[i+1]
		}
	}
	if tr == "" {
		t.Fatalf("the Windows unit has no /tr value: %v", windows.Register)
	}
	for _, want := range []string{`"` + spacedExe + `"`, `"` + spaced + `"`} {
		if !strings.Contains(tr, want) {
			t.Errorf("the scheduled task does not quote %s: %s", want, tr)
		}
	}

	// launchd needs no quoting: ProgramArguments is an array of separate
	// <string> elements, so a space inside one cannot split it. It needs XML
	// escaping instead, which the plist test covers.
	darwin, err := UnitFor("darwin", home, spacedExe, spaced)
	if err != nil {
		t.Fatalf("UnitFor(darwin) = %v", err)
	}
	if !strings.Contains(darwin.Content, "<string>"+spaced+"</string>") {
		t.Errorf("the plist does not carry the root as its own argument:\n%s", darwin.Content)
	}
}

// The plist is read by launchd, and a malformed one fails silently at login
// rather than when it is written.
func TestTheLaunchdUnitIsAWellFormedPlist(t *testing.T) {
	t.Parallel()

	u := unitFor(t, "darwin")
	for _, want := range []string{"<?xml", "<plist", "ProgramArguments", "RunAtLoad", "</plist>"} {
		if !strings.Contains(u.Content, want) {
			t.Errorf("the plist has no %s:\n%s", want, u.Content)
		}
	}
	if strings.Count(u.Content, "<array>") != strings.Count(u.Content, "</array>") {
		t.Error("the plist has unbalanced <array> tags")
	}
}

func TestTheSystemdUnitRestartsAndStartsAtLogin(t *testing.T) {
	t.Parallel()

	u := unitFor(t, "linux")
	for _, want := range []string{"[Unit]", "[Service]", "[Install]", "ExecStart=", "Restart=", "WantedBy="} {
		if !strings.Contains(u.Content, want) {
			t.Errorf("the systemd unit has no %s:\n%s", want, u.Content)
		}
	}
}
