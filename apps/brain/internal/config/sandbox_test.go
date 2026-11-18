package config

import (
	"strings"
	"testing"
	"time"
)

func TestARoomIdlesThirtyMinutesByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := load(noEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SandboxIdle != 30*time.Minute {
		t.Errorf("SandboxIdle = %v, want 30m", cfg.SandboxIdle)
	}
	if cfg.SandboxImage != "" {
		t.Errorf("SandboxImage = %q, want the brain's own choice", cfg.SandboxImage)
	}
}

// Zero would delete every room on the reaper's next pass, so a server would
// cold start on every call; a negative window is meaningless.
func TestTheRoomIdleWindowMustBePositive(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0", "0s", "-5m"} {
		_, err := load(map[string]string{"OPENARITY_SANDBOX_IDLE": value})
		if err == nil || !strings.Contains(err.Error(), "SANDBOX_IDLE") {
			t.Errorf("%q: err = %v, want one naming SANDBOX_IDLE", value, err)
		}
	}
	for _, value := range []string{"1s", "45m", "2h"} {
		if _, err := load(map[string]string{"OPENARITY_SANDBOX_IDLE": value}); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
}

func TestTheRoomImageOverrideHoldsNoWhitespace(t *testing.T) {
	t.Parallel()

	if _, err := load(map[string]string{"OPENARITY_SANDBOX_IMAGE": "registry.example/room:1"}); err != nil {
		t.Errorf("a plain reference: %v", err)
	}
	_, err := load(map[string]string{"OPENARITY_SANDBOX_IMAGE": "room image"})
	if err == nil || !strings.Contains(err.Error(), "SANDBOX_IMAGE") {
		t.Errorf("err = %v, want one naming SANDBOX_IMAGE", err)
	}
}

func TestStringShowsTheSandboxSettings(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{"OPENARITY_SANDBOX_IDLE": "10m", "OPENARITY_SANDBOX_IMAGE": "registry.example/room:1"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, want := range []string{"SandboxImage:registry.example/room:1", "SandboxIdle:10m0s"} {
		if !strings.Contains(cfg.String(), want) {
			t.Errorf("String() lacks %q: %s", want, cfg)
		}
	}
}
