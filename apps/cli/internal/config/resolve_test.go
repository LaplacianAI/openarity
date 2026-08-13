package config

import (
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
)

func envOf(pairs map[string]string) Env {
	return func(key string) string { return pairs[key] }
}

const configPath = "/home/someone/.config/openarity/config.yaml"

func ctx(server, token string) Config {
	return ctxThemed(server, token, "")
}

func ctxThemed(server, token, theme string) Config {
	return Config{
		Current:  "local",
		Contexts: map[string]Context{"local": {Server: server, Token: token}},
		Theme:    theme,
	}
}

// The order every setting follows: what you typed, then what is exported,
// then what was saved, then the built-in. Anything else surprises someone.
func TestServerPrecedence(t *testing.T) {
	t.Parallel()

	saved := ctx("https://from-file.example.com", "")
	env := envOf(map[string]string{"OPENARITY_SERVER": "https://from-env.example.com"})

	for name, tc := range map[string]struct {
		flag  string
		env   Env
		saved Config
		want  string
	}{
		"flag wins":        {"https://from-flag.example.com", env, saved, "https://from-flag.example.com"},
		"then environment": {"", env, saved, "https://from-env.example.com"},
		"then the file":    {"", envOf(nil), saved, "https://from-file.example.com"},
		"then the default": {"", envOf(nil), Config{}, DefaultServer},
	} {
		got := Resolve(tc.flag, "", "", tc.env, tc.saved, configPath)
		if got.Server.Value != tc.want {
			t.Errorf("%s: server = %q, want %q", name, got.Server.Value, tc.want)
		}
	}
}

// The source is the whole point. With four places to set one value, "I set it
// and it did not take" is unanswerable without saying which one won.
func TestEverySettingNamesWhereItCameFrom(t *testing.T) {
	t.Parallel()

	saved := ctxThemed("https://from-file.example.com", "", "light")
	env := envOf(map[string]string{"OPENARITY_SERVER": "https://from-env.example.com"})

	got := Resolve("", "", "", env, saved, configPath)

	if !strings.Contains(got.Server.Source, "OPENARITY_SERVER") {
		t.Errorf("server source = %q, want it to name the variable", got.Server.Source)
	}
	if !strings.Contains(got.Theme.Source, configPath) {
		t.Errorf("theme source = %q, want it to name the file", got.Theme.Source)
	}

	fromFlag := Resolve("https://from-flag.example.com", "", "", env, saved, configPath)
	if !strings.Contains(fromFlag.Server.Source, "--server") {
		t.Errorf("server source = %q, want it to name the flag", fromFlag.Server.Source)
	}

	fromDefault := Resolve("", "", "", envOf(nil), Config{}, configPath)
	if !strings.Contains(fromDefault.Server.Source, "default") {
		t.Errorf("server source = %q, want it to say default", fromDefault.Server.Source)
	}
}

// The token is a credential. `oa config show` is the command someone runs
// while screen sharing, or pastes into an issue, so the value must never be
// in the resolved settings at all — not truncated, not masked.
func TestTheTokenValueIsNeverResolved(t *testing.T) {
	t.Parallel()

	const secret = "oa_live_7f3c9a_do_not_print"
	saved := ctx("", secret)
	env := envOf(map[string]string{"OPENARITY_TOKEN": secret, "OPENARITY_DEV_TOKEN": secret})

	got := Resolve("", secret, "", env, saved, configPath)

	if strings.Contains(got.Token.Value, secret) {
		t.Errorf("the token value is in the resolved settings: %q", got.Token.Value)
	}
	if strings.Contains(got.Token.Source, secret) {
		t.Errorf("the token value leaked into its source: %q", got.Token.Source)
	}
}

// Whether a token is set at all is exactly what someone needs to know while
// debugging, and it is not a secret.
func TestTheTokenReportsWhetherItIsSet(t *testing.T) {
	t.Parallel()

	set := Resolve("", "", "", envOf(nil), ctx("", "a-token"), configPath)
	if set.Token.Value == "" {
		t.Error("a saved token is reported as absent")
	}

	unset := Resolve("", "", "", envOf(nil), Config{}, configPath)
	if unset.Token.Value == set.Token.Value {
		t.Errorf("set and unset report the same thing: %q", unset.Token.Value)
	}
}

// Theme follows the same order, and its absence is auto rather than blank —
// a blank theme in `oa config show` reads as broken.
func TestThemePrecedenceAndDefault(t *testing.T) {
	t.Parallel()

	env := envOf(map[string]string{"OPENARITY_THEME": "dark"})

	if got := Resolve("", "", "", env, ctxThemed("", "", "light"), configPath); got.Theme.Value != "dark" {
		t.Errorf("theme = %q, want the environment to win", got.Theme.Value)
	}
	if got := Resolve("", "", "", envOf(nil), ctxThemed("", "", "light"), configPath); got.Theme.Value != "light" {
		t.Errorf("theme = %q, want the file", got.Theme.Value)
	}
	if got := Resolve("", "", "", envOf(nil), Config{}, configPath); got.Theme.Value != string(DefaultTheme) {
		t.Errorf("theme = %q, want %q", got.Theme.Value, DefaultTheme)
	}
}

// config stores; it does not interpret. Reporting an unrecognised theme as
// "auto" would hide the typo in the one command someone runs to find it —
// ui decides what an unknown value renders as, and that is a separate
// question from what is set.
func TestAnUnknownThemeIsReportedAsSet(t *testing.T) {
	t.Parallel()

	got := Resolve("", "", "", envOf(map[string]string{"OPENARITY_THEME": "solarized"}), Config{}, configPath)

	if got.Theme.Value != "solarized" {
		t.Errorf("theme = %q, want it reported verbatim so the typo is visible", got.Theme.Value)
	}
	if !strings.Contains(got.Theme.Source, "OPENARITY_THEME") {
		t.Errorf("theme source = %q, want it to name the variable", got.Theme.Source)
	}
}

// Output carries a flag that theme does not: `-o json` changes per command,
// where a theme is set once. So it has four places, and the flag has to win.
func TestOutputPrecedence(t *testing.T) {
	t.Parallel()

	saved := Config{Output: "yaml"}
	env := envOf(map[string]string{"OPENARITY_OUTPUT": "json"})

	for name, tc := range map[string]struct {
		flag  string
		env   Env
		saved Config
		want  string
	}{
		"flag wins":        {"table", env, saved, "table"},
		"then environment": {"", env, saved, "json"},
		"then the file":    {"", envOf(nil), saved, "yaml"},
		"then the default": {"", envOf(nil), Config{}, string(DefaultOutput)},
	} {
		got := Resolve("", "", tc.flag, tc.env, tc.saved, configPath)
		if got.Output.Value != tc.want {
			t.Errorf("%s: output = %q, want %q", name, got.Output.Value, tc.want)
		}
	}
}

// A person at a terminal is the common case. An unset output resolving to
// json would make every command unreadable out of the box.
func TestOutputDefaultsToTable(t *testing.T) {
	t.Parallel()

	got := Resolve("", "", "", envOf(nil), Config{}, configPath)

	if got.Output.Value != string(output.Table) {
		t.Errorf("output = %q, want %q", got.Output.Value, output.Table)
	}
	if got.Output.Source != "default" {
		t.Errorf("output source = %q, want default", got.Output.Source)
	}
}

// Same rule as theme: config stores, it does not interpret. Reporting an
// unrecognised format as "table" would hide the typo in the one command
// someone runs to find it.
func TestAnUnknownOutputIsReportedAsSet(t *testing.T) {
	t.Parallel()

	got := Resolve("", "", "", envOf(map[string]string{"OPENARITY_OUTPUT": "jsonl"}), Config{}, configPath)

	if got.Output.Value != "jsonl" {
		t.Errorf("output = %q, want it reported verbatim so the typo is visible", got.Output.Value)
	}
	if !strings.Contains(got.Output.Source, "OPENARITY_OUTPUT") {
		t.Errorf("output source = %q, want it to name the variable", got.Output.Source)
	}
}

// Output is a display preference, not a property of a brain. Switching
// context must not silently put someone back on a table mid-script.
func TestOutputIsNotPerContext(t *testing.T) {
	t.Parallel()

	saved := Config{
		Current:  "prod",
		Contexts: map[string]Context{"prod": {Server: "https://brain.example.com"}, "local": {}},
		Output:   "json",
	}

	prod := Resolve("", "", "", envOf(nil), saved, configPath)

	saved.Current = "local"
	local := Resolve("", "", "", envOf(nil), saved, configPath)

	if prod.Output.Value != local.Output.Value {
		t.Errorf("output changed with the context: %q then %q", prod.Output.Value, local.Output.Value)
	}
}

// The source is the whole point, and output now has the most places to come
// from. "I set -o json and got a table" is unanswerable without it.
func TestOutputNamesWhereItCameFrom(t *testing.T) {
	t.Parallel()

	fromFlag := Resolve("", "", "json", envOf(nil), Config{}, configPath)
	if !strings.Contains(fromFlag.Output.Source, "--output") {
		t.Errorf("output source = %q, want it to name the flag", fromFlag.Output.Source)
	}

	fromFile := Resolve("", "", "", envOf(nil), Config{Output: "yaml"}, configPath)
	if !strings.Contains(fromFile.Output.Source, configPath) {
		t.Errorf("output source = %q, want it to name the file", fromFile.Output.Source)
	}
}

// Whitespace around a pasted value is not part of it. A server URL with a
// trailing newline produces a request to a host that does not resolve.
func TestResolvedValuesAreTrimmed(t *testing.T) {
	t.Parallel()

	got := Resolve("  https://from-flag.example.com\n", "", "", envOf(nil), Config{}, configPath)

	if got.Server.Value != "https://from-flag.example.com" {
		t.Errorf("server = %q, want it trimmed", got.Server.Value)
	}
}
