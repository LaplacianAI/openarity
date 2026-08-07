package config

import (
	"encoding"
	"testing"
)

// A value receiver on UnmarshalText compiles but never runs. Catch it here.
var (
	_ encoding.TextUnmarshaler = (*Environment)(nil)
	_ encoding.TextUnmarshaler = (*LogLevel)(nil)
)

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

func TestLogLevelAcceptsKnownValues(t *testing.T) {
	t.Parallel()

	for _, want := range []LogLevel{
		LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError,
	} {
		var got LogLevel
		if err := got.UnmarshalText([]byte(want)); err != nil {
			t.Errorf("%q rejected: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestLogLevelRejectsUnknownValues(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"banana", "INFO", "warning", "fatal", ""} {
		var l LogLevel
		if err := l.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("%q accepted as %q, want an error", in, l)
		}
	}
}

// The whole point of the enum: a bad value must stop the process starting.
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
