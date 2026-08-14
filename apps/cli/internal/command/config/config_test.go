package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
)

// The root these tests drive.
var commands = []clitest.Build{New}

func TestSetWritesTheServer(t *testing.T) {
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "config", "set", "server", "https://brain.example.com"); err != nil {
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
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "config", "set", "theme", "solarized")
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
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "config", "set", "theme", "DARK"); err != nil {
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
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "config", "set", "colour", "red"); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

// Setting one value must not drop the others. Writing the whole struct from a
// zero value is the obvious way to get this wrong.
func TestSetKeepsTheOtherValues(t *testing.T) {
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "config", "set", "server", "https://brain.example.com"); err != nil {
		t.Fatalf("set server: %v", err)
	}
	if _, err := clitest.Execute(t, commands, "config", "set", "theme", "light"); err != nil {
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
	clitest.Isolate(t)

	clitest.Seed(t, commands, "config", "set", "server", "https://brain.example.com")
	clitest.Seed(t, commands, "config", "set", "theme", "dark")

	if _, err := clitest.Execute(t, commands, "config", "unset", "theme"); err != nil {
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

func TestSetWritesTheOutputFormat(t *testing.T) {
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "config", "set", "output", "JSON"); err != nil {
		t.Fatalf("config set output: %v", err)
	}

	saved, _ := config.Load()
	if saved.Output != "json" {
		t.Errorf("saved output = %q, want it normalised to json", saved.Output)
	}
}

// `oa teams list -o jsonl > out.json` succeeding with a table in the file is
// worse than failing, so the typo has to be refused on the way in.
func TestSetRefusesAnUnknownOutputFormat(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "config", "set", "output", "jsonl")
	if err == nil {
		t.Fatal("an unknown output format was accepted")
	}
	if !strings.Contains(out+err.Error(), "jsonl") {
		t.Errorf("the message does not name the value: %v", err)
	}

	saved, _ := config.Load()
	if saved.Output != "" {
		t.Errorf("a rejected format was written anyway: %q", saved.Output)
	}
}

// A format that reached the config file unparsed would fail on every later
// command, including the one someone runs to find out why.
func TestASavedFormatIsAlwaysUsable(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "config", "set", "output", "yaml")

	if _, err := clitest.Execute(t, commands, "config", "show"); err != nil {
		t.Fatalf("a saved format broke the next command: %v", err)
	}
}

// An unparseable format is a hard error rather than a silent fall back to the
// table, unlike theme — a wrong colour still shows the data, a wrong format
// corrupts a redirect.
func TestAnUnknownFormatOnTheFlagFails(t *testing.T) {
	clitest.Isolate(t)

	if _, err := clitest.Execute(t, commands, "config", "show", "--output", "jsonn"); err == nil {
		t.Fatal("an unknown format on the flag was accepted")
	}
}

func TestShowListsTheOutputFormat(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out, "output") {
		t.Errorf("config show does not list the output format:\n%s", out)
	}
}

func TestUnsetRemovesTheOutputFormat(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "config", "set", "output", "json")
	clitest.Seed(t, commands, "config", "set", "theme", "dark")

	if _, err := clitest.Execute(t, commands, "config", "unset", "output"); err != nil {
		t.Fatalf("config unset output: %v", err)
	}

	saved, _ := config.Load()
	if saved.Output != "" {
		t.Errorf("output survived unset: %q", saved.Output)
	}
	if saved.Theme != "dark" {
		t.Errorf("unset removed the theme too: %+v", saved)
	}
}

// The command people run while screen sharing, and paste into issues. The
// token must never be in it.
func TestShowNeverPrintsTheToken(t *testing.T) {
	clitest.Isolate(t)

	const secret = "oa_live_7f3c9a_do_not_print"
	t.Setenv("OPENARITY_TOKEN", secret)

	out, err := clitest.Execute(t, commands, "config", "show")
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
	clitest.Isolate(t)

	t.Setenv("OPENARITY_SERVER", "https://from-env.example.com")
	clitest.Seed(t, commands, "config", "set", "server", "https://from-file.example.com")

	out, err := clitest.Execute(t, commands, "config", "show")
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
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "config", "path")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}

	want, _ := config.Path()
	if !strings.Contains(out, want) {
		t.Errorf("config path printed %q, want %q", out, want)
	}
}

// The token must be absent from every format, not just the one someone
// happened to test. A machine format is the one that ends up in a file.
func TestNoFormatEverPrintsTheToken(t *testing.T) {
	clitest.Isolate(t)

	const secret = "oa_live_7f3c9a_do_not_print"
	t.Setenv("OPENARITY_TOKEN", secret)

	for _, format := range []string{"table", "json", "yaml"} {
		out, err := clitest.Execute(t, commands, "config", "show", "-o", format)
		if err != nil {
			t.Fatalf("config show -o %s: %v", format, err)
		}
		if strings.Contains(out, secret) {
			t.Errorf("%s printed the token:\n%s", format, out)
		}
	}
}

// `oa config show -o json` is what a script reads, so it has to be a document
// rather than a table with braces in it.
func TestShowRendersJSON(t *testing.T) {
	clitest.Isolate(t)

	clitest.Seed(t, commands, "config", "set", "server", "https://brain.example.com")

	out, err := clitest.Execute(t, commands, "config", "show", "-o", "json")
	if err != nil {
		t.Fatalf("config show -o json: %v", err)
	}

	var got []map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the output is not json: %v\n%s", err, out)
	}

	found := map[string]string{}
	for _, one := range got {
		found[one["name"]] = one["value"]
	}
	for _, want := range []string{"context", "server", "theme", "output", "token"} {
		if _, ok := found[want]; !ok {
			t.Errorf("the json is missing %q: %v", want, found)
		}
	}
	if found["server"] != "https://brain.example.com" {
		t.Errorf("server = %q", found["server"])
	}
}

// Five settings, each once, in a fixed order. A duplicated entry would print
// the same row twice and a script reading the array would take the last.
func TestShowListsEverySettingExactlyOnce(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "config", "show", "-o", "json")
	if err != nil {
		t.Fatalf("config show -o json: %v", err)
	}

	var got []map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v", err)
	}

	want := []string{"context", "server", "theme", "output", "token"}
	if len(got) != len(want) {
		t.Fatalf("got %d settings, want %d: %v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i]["name"] != name {
			t.Errorf("setting %d is %q, want %q", i, got[i]["name"], name)
		}
	}
}

// Every setting carries where it came from, and json is where a script would
// read it to decide whether a value is safe to overwrite.
func TestShowCarriesTheSourceInJSON(t *testing.T) {
	clitest.Isolate(t)

	t.Setenv("OPENARITY_SERVER", "https://from-env.example.com")

	out, err := clitest.Execute(t, commands, "config", "show", "-o", "json")
	if err != nil {
		t.Fatalf("config show -o json: %v", err)
	}

	var got []map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v", err)
	}

	for _, one := range got {
		if one["name"] == "server" && one["source"] != "OPENARITY_SERVER" {
			t.Errorf("server source = %q, want it to name the variable", one["source"])
		}
	}
}

// Colour would land inside a json string and the consumer would fail to parse.
func TestNoFormatCarriesEscapeSequences(t *testing.T) {
	clitest.Isolate(t)

	for _, format := range []string{"json", "yaml"} {
		out, err := clitest.Execute(t, commands, "config", "show", "-o", format)
		if err != nil {
			t.Fatalf("config show -o %s: %v", format, err)
		}
		if strings.Contains(out, "\x1b") {
			t.Errorf("%s carries an escape sequence: %q", format, out)
		}
	}
}

// The subcommands are the contract. One built and never registered passes
// every test written about its body.
func TestEverySubcommandIsRegistered(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "config", "--help")
	if err != nil {
		t.Fatalf("config --help: %v", err)
	}
	for _, verb := range []string{"show", "set", "unset", "path"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not registered:\n%s", verb, out)
		}
	}
}
