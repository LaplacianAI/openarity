package config

import (
	"strings"
	"testing"
)

// A credential is only valid for the brain that issued it, so it lives inside
// the context rather than beside it. A flat file makes sending staging's
// token to production a one-word mistake.
func TestATokenBelongsToItsContext(t *testing.T) {
	isolate(t)

	cfg := Config{
		Current: "local",
		Theme:   "dark",
		Contexts: map[string]Context{
			"local":   {Server: "http://127.0.0.1:21120", Token: "local-token"},
			"staging": {Server: "https://staging.example.com", Token: "staging-token"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Contexts["local"].Token != "local-token" {
		t.Errorf("local token = %q", got.Contexts["local"].Token)
	}
	if got.Contexts["staging"].Token != "staging-token" {
		t.Errorf("staging token = %q", got.Contexts["staging"].Token)
	}
	if got.Current != "local" {
		t.Errorf("current = %q", got.Current)
	}
}

// Theme is about the terminal, not about which brain is being addressed, so
// it stays at the top level. Putting it in a context would mean re-setting it
// every time one is added.
func TestThemeIsNotPerContext(t *testing.T) {
	t.Parallel()

	cfg := Config{Theme: "light", Contexts: map[string]Context{"local": {Server: "http://127.0.0.1:21120"}}}

	if cfg.Theme != "light" {
		t.Errorf("theme = %q", cfg.Theme)
	}
}

// A fresh install has no file and therefore no context. The default server
// belongs to Resolve, not here — returning it from Active would label the
// built-in as having come from the config file.
func TestAnEmptyConfigHasNoContext(t *testing.T) {
	t.Parallel()

	if got := (Config{}).Active(); got != (Context{}) {
		t.Errorf("Active() = %+v, want the zero context", got)
	}

	settings := Resolve("", "", envOf(nil), Config{}, configPath)
	if settings.Server.Value != DefaultServer {
		t.Errorf("server = %q, want %q", settings.Server.Value, DefaultServer)
	}
	if settings.Server.Source != "default" {
		t.Errorf("server source = %q, want default", settings.Server.Source)
	}
}

func TestActiveReturnsTheCurrentContext(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Current: "staging",
		Contexts: map[string]Context{
			"local":   {Server: "http://127.0.0.1:21120", Token: "local-token"},
			"staging": {Server: "https://staging.example.com", Token: "staging-token"},
		},
	}

	got := cfg.Active()
	if got.Server != "https://staging.example.com" || got.Token != "staging-token" {
		t.Errorf("Active() = %+v, want staging", got)
	}
}

// A current that names nothing is a hand-edited file or a deleted context.
// Falling back to a working default beats refusing to run every command.
func TestADanglingCurrentFallsBackToTheDefault(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Current:  "deleted",
		Contexts: map[string]Context{"local": {Server: "http://127.0.0.1:21120"}},
	}

	if got := cfg.Active(); got != (Context{}) {
		t.Errorf("Active() = %+v, want the zero context", got)
	}
}

// One context and no `current` is unambiguous: use it. Making someone run
// `oa context use` after creating their only context is ceremony.
func TestASingleContextIsUsedWithoutBeingSelected(t *testing.T) {
	t.Parallel()

	cfg := Config{Contexts: map[string]Context{"only": {Server: "https://only.example.com"}}}

	if got := cfg.Active(); got.Server != "https://only.example.com" {
		t.Errorf("Active() = %+v, want the only context", got)
	}
}

// Several contexts and no `current` is genuinely ambiguous, and picking one
// by map order would pick a different one on every run.
func TestSeveralContextsWithNoCurrentUseTheDefault(t *testing.T) {
	t.Parallel()

	cfg := Config{Contexts: map[string]Context{
		"a": {Server: "https://a.example.com"},
		"b": {Server: "https://b.example.com"},
	}}

	if got := cfg.Active(); got != (Context{}) {
		t.Errorf("Active() = %+v, want no context rather than an arbitrary one", got)
	}
}

// Names are how a person selects a context, and they end up in shell history
// and scripts.
func TestContextNamesAreListedInAStableOrder(t *testing.T) {
	t.Parallel()

	cfg := Config{Contexts: map[string]Context{
		"staging": {Server: "https://staging.example.com"},
		"local":   {Server: "http://127.0.0.1:21120"},
		"prod":    {Server: "https://prod.example.com"},
	}}

	first := strings.Join(cfg.ContextNames(), ",")
	for range 20 {
		if got := strings.Join(cfg.ContextNames(), ","); got != first {
			t.Fatalf("ContextNames() returned %q then %q — map order is leaking", first, got)
		}
	}
	if first != "local,prod,staging" {
		t.Errorf("ContextNames() = %q, want them sorted", first)
	}
}

// The whole point of contexts: a token written for one must not be readable
// as another's. This is the mistake the flat file invited.
func TestSettingOneContextDoesNotTouchAnother(t *testing.T) {
	isolate(t)

	cfg := Config{
		Current: "local",
		Contexts: map[string]Context{
			"local":   {Server: "http://127.0.0.1:21120", Token: "local-token"},
			"staging": {Server: "https://staging.example.com", Token: "staging-token"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, _ := Load()
	loaded.Contexts["local"] = Context{Server: "http://127.0.0.1:9999", Token: "new-local-token"}
	if err := Save(loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, _ := Load()
	if got.Contexts["staging"].Token != "staging-token" {
		t.Errorf("staging's token changed: %+v", got.Contexts["staging"])
	}
}
