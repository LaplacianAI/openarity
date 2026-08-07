package config

import (
	"encoding"
	"log/slog"
	"testing"
)

// A value receiver on UnmarshalText compiles but never runs. Catch it here.
var _ encoding.TextUnmarshaler = (*Environment)(nil)

// LogLevel is slog.Level rather than a local enum, which is what lets it drop
// straight into slog.HandlerOptions.Level. Swap the field back to a string and
// this stops compiling.
var _ slog.Leveler = Config{}.LogLevel

func TestEnvironmentAcceptsKnownValues(t *testing.T) {
	t.Parallel()

	for _, want := range []Environment{
		EnvironmentDevelopment, EnvironmentProduction,
		EnvironmentStaging, EnvironmentTest,
	} {
		var got Environment
		if err := got.UnmarshalText([]byte(want)); err != nil {
			t.Errorf("%q rejected: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestEnvironmentRejectsUnknownValues(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"prodution",  // typo
		"Production", // wrong case
		"prod",       // abbreviation
		"",           // empty
		" development",
	} {
		var e Environment
		if err := e.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("%q accepted as %q, want an error", in, e)
		}
	}
}

// A rejected value must not be written to the receiver.
func TestEnvironmentUnchangedOnError(t *testing.T) {
	t.Parallel()

	e := EnvironmentProduction
	if err := e.UnmarshalText([]byte("garbage")); err == nil {
		t.Fatal("expected an error")
	}
	if e != EnvironmentProduction {
		t.Errorf("receiver was overwritten with %q on error", e)
	}
}

// The whole point of the typed fields: a bad value must stop the process
// starting. LogLevel's parsing belongs to the stdlib now, so this asserts the
// wiring — that the field is still routed through UnmarshalText — rather than
// re-testing slog.
func TestLoadRejectsBadEnums(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"OPENARITY_ENVIRONMENT": "prodution",
		"OPENARITY_LOG_LEVEL":   "banana",
	}
	for key, bad := range tests {
		if cfg, err := load(map[string]string{key: bad}); err == nil {
			t.Errorf("%s=%q accepted, got %+v", key, bad, cfg)
		}
	}
}

// slog.Level parses case-insensitively. Asserted through load, not through
// UnmarshalText, so this fails if the field ever stops being a slog.Level.
func TestLoadAcceptsAnyCaseLogLevel(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"warn", "WARN", "Warn"} {
		cfg, err := load(map[string]string{"OPENARITY_LOG_LEVEL": in})
		if err != nil {
			t.Errorf("%q rejected: %v", in, err)
			continue
		}
		if cfg.LogLevel != slog.LevelWarn {
			t.Errorf("%q loaded as %v, want WARN", in, cfg.LogLevel)
		}
	}
}
