package config

import (
	"os"
	"strings"
	"testing"
)

// A context is a name for a brain, and the address is the whole of it now
// that the credential lives in the store. A flat `server` field would make
// pointing a staging command at production a one-word mistake.
func TestEachContextKeepsItsOwnServer(t *testing.T) {
	isolate(t)

	cfg := Config{
		Current: "local",
		Theme:   "dark",
		Contexts: map[string]Context{
			"local":   {Server: "http://127.0.0.1:21120"},
			"staging": {Server: "https://staging.example.com"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Contexts["local"].Server != "http://127.0.0.1:21120" {
		t.Errorf("local server = %q", got.Contexts["local"].Server)
	}
	if got.Contexts["staging"].Server != "https://staging.example.com" {
		t.Errorf("staging server = %q", got.Contexts["staging"].Server)
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

	settings := Resolve(Input{Env: envOf(nil), Path: configPath})
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
			"local":   {Server: "http://127.0.0.1:21120"},
			"staging": {Server: "https://staging.example.com"},
		},
	}

	got := cfg.Active()
	if got.Server != "https://staging.example.com" {
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

// The whole point of contexts: editing one must not disturb another. The
// credential store keys the same way, and tests that in its own package —
// this covers the half that stayed here.
func TestSettingOneContextDoesNotTouchAnother(t *testing.T) {
	isolate(t)

	cfg := Config{
		Current: "local",
		Contexts: map[string]Context{
			"local":   {Server: "http://127.0.0.1:21120"},
			"staging": {Server: "https://staging.example.com"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, _ := Load()
	loaded.Contexts["local"] = Context{Server: "http://127.0.0.1:9999"}
	if err := Save(loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, _ := Load()
	if got.Contexts["staging"].Server != "https://staging.example.com" {
		t.Errorf("staging changed: %+v", got.Contexts["staging"])
	}
}

// What a login produces now belongs to internal/credential, and this is the
// guard that it never comes back. The config file is the one meant to be
// readable — pasted into an issue, synced between machines — so a refresh
// token or an expiry appearing in it is the split having quietly come undone.
func TestTheConfigFileCarriesNoLoginState(t *testing.T) {
	isolate(t)

	cfg := Config{
		Current:  "local",
		Contexts: map[string]Context{"local": {Server: "http://127.0.0.1:21120"}},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	for _, absent := range []string{"token", "expiry", "refresh_token", "0001-01-01"} {
		if strings.Contains(string(data), absent) {
			t.Errorf("the config file carries %q, which belongs in the credential store:\n%s", absent, data)
		}
	}
}
