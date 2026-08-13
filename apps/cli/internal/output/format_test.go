package output

import (
	"strings"
	"testing"
)

// Parsing lives here so there is one definition of what a format is. config
// stores the value and the printer renders it; neither interprets it.
func TestParseAccepts(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]Format{
		"table":   Table,
		"json":    JSON,
		"yaml":    YAML,
		"JSON":    JSON,
		"Table":   Table,
		" yaml ":  YAML,
		"\tjson ": JSON,
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

// A typo has to be refused on the way in. Accepted and saved, `-o jsonn` would
// silently print a table into something that expected to parse JSON.
func TestParseRejects(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", " ", "jsonn", "yml", "wide", "text", "1", "true", "json-pretty"} {
		if got, ok := Parse(bad); ok {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
}

// A caller that ignores the boolean must still get a usable format rather than
// an empty one that matches no case.
func TestARejectedValueStillReturnsTheDefault(t *testing.T) {
	t.Parallel()

	if got, _ := Parse("jsonn"); got != Default {
		t.Errorf("Parse rejected and returned %q, want %q", got, Default)
	}
}

// A person at a terminal is the common case, so that is what an unset format
// has to mean.
func TestTheDefaultIsTable(t *testing.T) {
	t.Parallel()

	if Default != Table {
		t.Errorf("Default = %q, want %q", Default, Table)
	}
}

// The names are the flag value, the file format and the environment variable.
// Changing one invalidates every script already written against it.
func TestTheNamesAreStable(t *testing.T) {
	t.Parallel()

	for got, want := range map[Format]string{Table: "table", JSON: "json", YAML: "yaml"} {
		if string(got) != want {
			t.Errorf("format constant is %q, want %q", got, want)
		}
	}
}

func TestAllCarriesEveryFormat(t *testing.T) {
	t.Parallel()

	all := All()
	if len(all) != 3 {
		t.Fatalf("All() = %v, want three formats", all)
	}

	seen := map[Format]bool{}
	for _, one := range all {
		if _, ok := Parse(string(one)); !ok {
			t.Errorf("All() contains %q, which Parse rejects", one)
		}
		seen[one] = true
	}
	for _, want := range []Format{Table, JSON, YAML} {
		if !seen[want] {
			t.Errorf("All() is missing %q", want)
		}
	}
}

// Names is what an error message and a help string show, so it has to carry
// every format and nothing Parse would reject. A list offering a value that
// cannot be set is worse than no list.
func TestNamesCarriesEveryFormatAndOnlyValidOnes(t *testing.T) {
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

// This is what decides whether colour, headings and "no contexts — try..."
// hints are allowed. Getting it backwards for one format puts escape
// sequences inside a JSON string, and the consumer fails to parse it.
func TestOnlyTableIsForAPerson(t *testing.T) {
	t.Parallel()

	if Table.IsMachine() {
		t.Error("table is treated as machine output, so it would print undecorated")
	}
	for _, machine := range []Format{JSON, YAML} {
		if !machine.IsMachine() {
			t.Errorf("%q is treated as human output, so it would carry decoration", machine)
		}
	}
}

// A format added to All but not taught to IsMachine defaults to machine, which
// is the safe direction — but it must be a deliberate choice, not a surprise.
func TestEveryFormatAnswersIsMachine(t *testing.T) {
	t.Parallel()

	human := 0
	for _, one := range All() {
		if !one.IsMachine() {
			human++
		}
	}
	if human != 1 {
		t.Errorf("%d formats are for a person, want exactly one", human)
	}
}
