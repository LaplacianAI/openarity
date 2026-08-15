package config

import (
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
)

func envOf(pairs map[string]string) Env {
	return func(key string) string { return pairs[key] }
}

const (
	configPath = "/home/someone/.config/openarity/config.yaml"

	// What a keychain answers Location() with. Deliberately not a path: it is
	// the value `oa config show` prints as a token's source, and half the
	// point of the store is that it need not be a file.
	credentialLocation = "the macOS keychain"
)

// Every test writes its own Input literal rather than going through a builder,
// naming only the fields it is about. Env is set everywhere because Resolve
// calls it for every setting and a nil one panics.
func ctx(server string) Config {
	return ctxThemed(server, "")
}

func ctxThemed(server, theme string) Config {
	return Config{
		Current:  "local",
		Contexts: map[string]Context{"local": {Server: server}},
		Theme:    theme,
	}
}

// The order every setting follows: what you typed, then what is exported,
// then what was saved, then the built-in. Anything else surprises someone.
func TestServerPrecedence(t *testing.T) {
	t.Parallel()

	saved := ctx("https://from-file.example.com")
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
		got := Resolve(Input{ServerFlag: tc.flag, Env: tc.env, Saved: tc.saved, Path: configPath})
		if got.Server.Value != tc.want {
			t.Errorf("%s: server = %q, want %q", name, got.Server.Value, tc.want)
		}
	}
}

// The source is the whole point. With four places to set one value, "I set it
// and it did not take" is unanswerable without saying which one won.
func TestEverySettingNamesWhereItCameFrom(t *testing.T) {
	t.Parallel()

	saved := ctxThemed("https://from-file.example.com", "light")
	env := envOf(map[string]string{"OPENARITY_SERVER": "https://from-env.example.com"})

	got := Resolve(Input{Env: env, Saved: saved, Path: configPath})

	if !strings.Contains(got.Server.Source, "OPENARITY_SERVER") {
		t.Errorf("server source = %q, want it to name the variable", got.Server.Source)
	}
	if !strings.Contains(got.Theme.Source, configPath) {
		t.Errorf("theme source = %q, want it to name the file", got.Theme.Source)
	}

	fromFlag := Resolve(Input{
		ServerFlag: "https://from-flag.example.com",
		Env:        env, Saved: saved, Path: configPath,
	})
	if !strings.Contains(fromFlag.Server.Source, "--server") {
		t.Errorf("server source = %q, want it to name the flag", fromFlag.Server.Source)
	}

	fromDefault := Resolve(Input{Env: envOf(nil), Path: configPath})
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

	got := Resolve(Input{
		TokenFlag: secret,
		Env: envOf(map[string]string{
			"OPENARITY_TOKEN":     secret,
			"OPENARITY_DEV_TOKEN": secret,
		}),
		Saved:              ctx(""),
		Path:               configPath,
		Credential:         credential.Credential{Token: secret, Refresh: secret},
		CredentialLocation: credentialLocation,
	})

	if strings.Contains(got.Token.Value, secret) {
		t.Errorf("the token value is in the resolved settings: %q", got.Token.Value)
	}
	if strings.Contains(got.Token.Source, secret) {
		t.Errorf("the token value leaked into its source: %q", got.Token.Source)
	}
	// The refresh token is the more valuable of the two and has no reason to
	// be reported at all, so nothing here may carry it either.
	for _, s := range []Setting{got.Server, got.Theme, got.Output, got.Context} {
		if strings.Contains(s.Value+s.Source, secret) {
			t.Errorf("%s carries the credential: %+v", s.Name, s)
		}
	}
}

// Whether a token is set at all is exactly what someone needs to know while
// debugging, and it is not a secret.
func TestTheTokenReportsWhetherItIsSet(t *testing.T) {
	t.Parallel()

	set := Resolve(Input{
		Env: envOf(nil), Saved: ctx(""), Path: configPath,
		Credential:         credential.Credential{Token: "a-token"},
		CredentialLocation: credentialLocation,
	})
	if set.Token.Value == "" {
		t.Error("a stored token is reported as absent")
	}

	unset := Resolve(Input{
		Env: envOf(nil), Path: configPath,
		CredentialLocation: credentialLocation,
	})
	if unset.Token.Value == set.Token.Value {
		t.Errorf("set and unset report the same thing: %q", unset.Token.Value)
	}
}

// The credential no longer lives in config.yaml, so naming the config file as
// its source would send someone to a file that does not contain it. On a mac
// the answer is not a path at all.
func TestAStoredTokenNamesTheStoreItCameFrom(t *testing.T) {
	t.Parallel()

	got := Resolve(Input{
		Env: envOf(nil), Saved: ctx(""), Path: configPath,
		Credential:         credential.Credential{Token: "a-token"},
		CredentialLocation: credentialLocation,
	})

	if got.Token.Source != credentialLocation {
		t.Errorf("token source = %q, want %q", got.Token.Source, credentialLocation)
	}
	if strings.Contains(got.Token.Source, configPath) {
		t.Error("the token is reported as coming from config.yaml, which no longer holds one")
	}
}

// A token given for one command must not be reported as though it were
// stored — otherwise `oa --token … config show` sends the next person looking
// in a keychain that has nothing in it.
func TestAFlagAndTheEnvironmentOutrankTheStore(t *testing.T) {
	t.Parallel()

	stored := credential.Credential{Token: "from-the-store"}

	fromFlag := Resolve(Input{
		TokenFlag: "from-the-flag", Env: envOf(nil), Path: configPath,
		Credential: stored, CredentialLocation: credentialLocation,
	})
	if fromFlag.Token.Source != "--token" {
		t.Errorf("token source = %q, want --token", fromFlag.Token.Source)
	}

	fromEnv := Resolve(Input{
		Env:        envOf(map[string]string{"OPENARITY_TOKEN": "from-the-environment"}),
		Path:       configPath,
		Credential: stored, CredentialLocation: credentialLocation,
	})
	if fromEnv.Token.Source != "OPENARITY_TOKEN" {
		t.Errorf("token source = %q, want OPENARITY_TOKEN", fromEnv.Token.Source)
	}
}

// Theme follows the same order, and its absence is auto rather than blank —
// a blank theme in `oa config show` reads as broken.
func TestThemePrecedenceAndDefault(t *testing.T) {
	t.Parallel()

	env := envOf(map[string]string{"OPENARITY_THEME": "dark"})

	if got := Resolve(Input{Env: env, Saved: ctxThemed("", "light"), Path: configPath}); got.Theme.Value != "dark" {
		t.Errorf("theme = %q, want the environment to win", got.Theme.Value)
	}
	if got := Resolve(Input{
		Env: envOf(nil), Saved: ctxThemed("", "light"), Path: configPath,
	}); got.Theme.Value != "light" {
		t.Errorf("theme = %q, want the file", got.Theme.Value)
	}
	if got := Resolve(Input{Env: envOf(nil), Path: configPath}); got.Theme.Value != string(DefaultTheme) {
		t.Errorf("theme = %q, want %q", got.Theme.Value, DefaultTheme)
	}
}

// config stores; it does not interpret. Reporting an unrecognised theme as
// "auto" would hide the typo in the one command someone runs to find it —
// ui decides what an unknown value renders as, and that is a separate
// question from what is set.
func TestAnUnknownThemeIsReportedAsSet(t *testing.T) {
	t.Parallel()

	got := Resolve(Input{
		Env:  envOf(map[string]string{"OPENARITY_THEME": "solarized"}),
		Path: configPath,
	})

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
		got := Resolve(Input{OutputFlag: tc.flag, Env: tc.env, Saved: tc.saved, Path: configPath})
		if got.Output.Value != tc.want {
			t.Errorf("%s: output = %q, want %q", name, got.Output.Value, tc.want)
		}
	}
}

// A person at a terminal is the common case. An unset output resolving to
// json would make every command unreadable out of the box.
func TestOutputDefaultsToTable(t *testing.T) {
	t.Parallel()

	got := Resolve(Input{Env: envOf(nil), Path: configPath})

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

	got := Resolve(Input{
		Env:  envOf(map[string]string{"OPENARITY_OUTPUT": "jsonl"}),
		Path: configPath,
	})

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

	prod := Resolve(Input{Env: envOf(nil), Saved: saved, Path: configPath})

	saved.Current = "local"
	local := Resolve(Input{Env: envOf(nil), Saved: saved, Path: configPath})

	if prod.Output.Value != local.Output.Value {
		t.Errorf("output changed with the context: %q then %q", prod.Output.Value, local.Output.Value)
	}
}

// The source is the whole point, and output now has the most places to come
// from. "I set -o json and got a table" is unanswerable without it.
func TestOutputNamesWhereItCameFrom(t *testing.T) {
	t.Parallel()

	fromFlag := Resolve(Input{OutputFlag: "json", Env: envOf(nil), Path: configPath})
	if !strings.Contains(fromFlag.Output.Source, "--output") {
		t.Errorf("output source = %q, want it to name the flag", fromFlag.Output.Source)
	}

	fromFile := Resolve(Input{
		Env: envOf(nil), Saved: Config{Output: "yaml"}, Path: configPath,
	})
	if !strings.Contains(fromFile.Output.Source, configPath) {
		t.Errorf("output source = %q, want it to name the file", fromFile.Output.Source)
	}
}

// Whitespace around a pasted value is not part of it. A server URL with a
// trailing newline produces a request to a host that does not resolve.
func TestResolvedValuesAreTrimmed(t *testing.T) {
	t.Parallel()

	got := Resolve(Input{
		ServerFlag: "  https://from-flag.example.com\n",
		Env:        envOf(nil), Path: configPath,
	})

	if got.Server.Value != "https://from-flag.example.com" {
		t.Errorf("server = %q, want it trimmed", got.Server.Value)
	}
}
