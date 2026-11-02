package config

import (
	"strings"
	"testing"
	"time"
)

func purgeAfter(value string) map[string]string {
	return map[string]string{"OPENARITY_TEAM_PURGE_AFTER": value}
}

// A week, written in hours because a Go duration has no day.
func TestADeletedTeamIsKeptAWeekByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := load(noEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.TeamPurgeAfter != 168*time.Hour {
		t.Errorf("TeamPurgeAfter = %v, want 168h", cfg.TeamPurgeAfter)
	}
}

// Both ends are allowed: an hour is the shortest window that is still one,
// ninety days the longest that is still an erasure.
func TestThePurgeWindowAcceptsItsBounds(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]time.Duration{
		"1h":    time.Hour,
		"60m":   time.Hour,
		"2160h": 2160 * time.Hour,
		"72h":   72 * time.Hour,
	} {
		cfg, err := load(purgeAfter(value))
		if err != nil {
			t.Errorf("%q: %v", value, err)
			continue
		}
		if cfg.TeamPurgeAfter != want {
			t.Errorf("%q: TeamPurgeAfter = %v, want %v", value, cfg.TeamPurgeAfter, want)
		}
	}
}

// Zero would purge a team on the next tick, which is a hard delete with an
// hour's delay. A negative window would purge it before it was deleted.
func TestThePurgeWindowRefusesValuesOutsideItsBounds(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"59m", "59m59s", "2160h1s", "2161h", "0", "0s", "-1h"} {
		_, err := load(purgeAfter(value))
		if err == nil {
			t.Errorf("%q was accepted", value)
			continue
		}
		for _, want := range []string{"TEAM_PURGE_AFTER", "between 1h and 2160h"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error %q does not say %q", value, err, want)
			}
		}
	}
}

// The unit an operator reaches for first. It must fail the boot rather than
// be read as something else.
func TestThePurgeWindowRefusesDays(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"7d", "1w", "week", "168"} {
		if _, err := load(purgeAfter(value)); err == nil {
			t.Errorf("%q was accepted", value)
		}
	}
}

// Not a secret: whoever reads the boot log should see how long a deleted
// team can still be restored.
func TestStringShowsThePurgeWindow(t *testing.T) {
	t.Parallel()

	cfg, err := load(purgeAfter("72h"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s := cfg.String(); !strings.Contains(s, "TeamPurgeAfter:72h0m0s") {
		t.Errorf("String() does not show the purge window: %s", s)
	}
}
