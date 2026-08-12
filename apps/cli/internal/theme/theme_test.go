package theme

import (
	"strings"
	"testing"
)

// Parsing lives here so there is one definition of what a theme is. config
// stores the value, ui renders it, and neither interprets it — a second
// parser is how "dark" starts meaning different things in two places.
func TestParseAccepts(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]Theme{
		"dark":    Dark,
		"light":   Light,
		"auto":    Auto,
		"DARK":    Dark,
		"Light":   Light,
		" auto ":  Auto,
		"\tdark ": Dark,
	} {
		got, ok := Parse(in)
		if !ok {
			t.Errorf("%q was rejected", in)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

// A typo must be refused on the way in. `oa config set theme solarized`
// saving happily would leave a value that is silently ignored on every run,
// and nothing would ever say why.
func TestParseRejects(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", " ", "solarized", "1", "true", "darkk", "auto-detect"} {
		if got, ok := Parse(bad); ok {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
}

// A rejected value still returns something usable, so a caller that ignores
// the boolean degrades to detection rather than to an empty theme that
// matches no case.
func TestARejectedValueStillReturnsAuto(t *testing.T) {
	t.Parallel()

	if got, _ := Parse("solarized"); got != Auto {
		t.Errorf("Parse rejected and returned %q, want %q", got, Auto)
	}
}

// The names are the file format and the environment variable. Changing one
// silently invalidates every config someone has already written.
func TestTheNamesAreStable(t *testing.T) {
	t.Parallel()

	for got, want := range map[Theme]string{Auto: "auto", Dark: "dark", Light: "light"} {
		if string(got) != want {
			t.Errorf("theme constant is %q, want %q", got, want)
		}
	}
}

// All returns them for help text and for a settings picker. Missing one would
// make it unselectable in a UI that lists them.
func TestAllCarriesEveryTheme(t *testing.T) {
	t.Parallel()

	all := All()
	if len(all) != 3 {
		t.Fatalf("All() = %v, want three themes", all)
	}

	seen := map[Theme]bool{}
	for _, one := range all {
		if _, ok := Parse(string(one)); !ok {
			t.Errorf("All() contains %q, which Parse rejects", one)
		}
		seen[one] = true
	}
	for _, want := range []Theme{Auto, Dark, Light} {
		if !seen[want] {
			t.Errorf("All() is missing %q", want)
		}
	}
}

// Names is what an error message and a help string show, so it has to carry
// every theme and nothing Parse would reject. A list offering a value that
// cannot be set is worse than no list.
func TestNamesCarriesEveryThemeAndOnlyValidOnes(t *testing.T) {
	t.Parallel()

	names := Names()

	for _, one := range All() {
		if !strings.Contains(names, string(one)) {
			t.Errorf("Names() = %q, missing %q", names, one)
		}
	}

	for _, name := range strings.Split(names, ", ") {
		if _, ok := Parse(name); !ok {
			t.Errorf("Names() offers %q, which Parse rejects", name)
		}
	}
}

// It is read by a person, so it is a sentence fragment rather than a Go
// slice. A %v of []Theme prints "[auto dark light]" and reads as debug output.
func TestNamesIsReadable(t *testing.T) {
	t.Parallel()

	names := Names()

	if strings.ContainsAny(names, "[]") {
		t.Errorf("Names() = %q, want no brackets", names)
	}
	if !strings.Contains(names, ", ") {
		t.Errorf("Names() = %q, want comma-separated", names)
	}
}
