package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

func loggerConfig(env config.Environment, level slog.Level) *config.Config {
	return &config.Config{Environment: env, LogLevel: level}
}

// Development gets human-readable text. JSON in a terminal is unreadable, and
// nothing local is parsing it.
func TestNewLoggerUsesTextInDevelopment(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	newLogger(loggerConfig(config.EnvironmentDevelopment, slog.LevelInfo), &buf).Info("hello")

	var any map[string]any
	if err := json.Unmarshal(buf.Bytes(), &any); err == nil {
		t.Errorf("development logger emitted JSON: %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("msg=hello")) {
		t.Errorf("development logger emitted %q, want text key=value", buf.String())
	}
}

// Everything else gets JSON, one object per line, so a log aggregator can
// query by field. Assert it actually parses rather than eyeballing braces.
func TestNewLoggerUsesJSONOutsideDevelopment(t *testing.T) {
	t.Parallel()

	for _, env := range []config.Environment{
		config.EnvironmentProduction,
		config.EnvironmentStaging,
		config.EnvironmentTest,
	} {
		var buf bytes.Buffer
		newLogger(loggerConfig(env, slog.LevelInfo), &buf).Info("hello", "count", 3)

		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Errorf("%s logger emitted unparseable output %q: %v", env, buf.String(), err)
			continue
		}
		if rec["msg"] != "hello" {
			t.Errorf("%s logger msg = %v, want hello", env, rec["msg"])
		}
		if rec["count"] != float64(3) {
			t.Errorf("%s logger dropped the attribute: %v", env, rec)
		}
	}
}

// The level has to reach the handler. Wire it wrong and production quietly
// emits every debug line, which is both a cost and a leak.
func TestNewLoggerFiltersBelowConfiguredLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newLogger(loggerConfig(config.EnvironmentProduction, slog.LevelWarn), &buf)

	logger.Debug("debug line")
	logger.Info("info line")
	if buf.Len() != 0 {
		t.Errorf("level warn emitted a lower-level record: %s", buf.String())
	}

	logger.Warn("warn line")
	if !bytes.Contains(buf.Bytes(), []byte("warn line")) {
		t.Errorf("level warn dropped a warn record: %s", buf.String())
	}
}

// Debug must actually get through when asked for, or the setting is useless.
func TestNewLoggerEmitsDebugWhenConfigured(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	newLogger(loggerConfig(config.EnvironmentProduction, slog.LevelDebug), &buf).Debug("debug line")

	if !bytes.Contains(buf.Bytes(), []byte("debug line")) {
		t.Errorf("level debug dropped a debug record: %s", buf.String())
	}
}

// AddSource costs a stack walk on every record. Worth it locally, not in
// production.
func TestNewLoggerAddsSourceOnlyInDevelopment(t *testing.T) {
	t.Parallel()

	var dev bytes.Buffer
	newLogger(loggerConfig(config.EnvironmentDevelopment, slog.LevelInfo), &dev).Info("hello")
	if !bytes.Contains(dev.Bytes(), []byte("logger_test.go")) {
		t.Errorf("development logger has no source location: %s", dev.String())
	}

	var prod bytes.Buffer
	newLogger(loggerConfig(config.EnvironmentProduction, slog.LevelInfo), &prod).Info("hello")
	if bytes.Contains(prod.Bytes(), []byte("logger_test.go")) {
		t.Errorf("production logger paid for AddSource: %s", prod.String())
	}
}

// Two loggers must not share state. A handler built once and reused would
// interleave a request logger's attributes into the startup logger.
func TestNewLoggerWritersAreIndependent(t *testing.T) {
	t.Parallel()

	var a, b bytes.Buffer
	cfg := loggerConfig(config.EnvironmentProduction, slog.LevelInfo)

	newLogger(cfg, &a).With("listener", "api").Info("first")
	newLogger(cfg, &b).Info("second")

	if bytes.Contains(b.Bytes(), []byte("listener")) {
		t.Errorf("attributes leaked between loggers: %s", b.String())
	}
	if bytes.Contains(a.Bytes(), []byte("second")) {
		t.Errorf("records leaked between writers: %s", a.String())
	}
}
