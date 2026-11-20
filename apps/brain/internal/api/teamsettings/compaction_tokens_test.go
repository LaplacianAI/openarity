package teamsettings

import (
	"net/http"
	"strings"
	"testing"
)

// withTokens is a save of everything else as body has it, with these token
// fields; an empty one is left out.
func withTokens(least, most string) string {
	b := strings.TrimSuffix(body("", searchOff, false, 100), "}")
	if least != "" {
		b += `,"compaction_tokens_min":` + least
	}
	if most != "" {
		b += `,"compaction_tokens_max":` + most
	}
	return b + "}"
}

func TestATeamThatNeverSavedCompactsBetween50And60Thousand(t *testing.T) {
	got := decode(t, newEnv().get(t))

	if got.CompactionTokensMin != 50_000 || got.CompactionTokensMax != 60_000 {
		t.Errorf("tokens = %d to %d, want 50,000 to 60,000", got.CompactionTokensMin, got.CompactionTokensMax)
	}
}

func TestSavingTheTokenLinesReturnsAndStoresThem(t *testing.T) {
	e := newEnv()

	got := decode(t, e.save(t, withTokens("80000", "90000")))

	if got.CompactionTokensMin != 80_000 || got.CompactionTokensMax != 90_000 {
		t.Errorf("returned %d to %d, want 80,000 to 90,000", got.CompactionTokensMin, got.CompactionTokensMax)
	}
	if p := e.store.upserts[0]; p.CompactionTokensMin != 80_000 || p.CompactionTokensMax != 90_000 {
		t.Errorf("stored %d to %d, want 80,000 to 90,000", p.CompactionTokensMin, p.CompactionTokensMax)
	}
}

func TestEqualLinesAreAFixedLine(t *testing.T) {
	got := decode(t, newEnv().save(t, withTokens("40000", "40000")))

	if got.CompactionTokensMin != 40_000 || got.CompactionTokensMax != 40_000 {
		t.Errorf("returned %d to %d, want 40,000 both", got.CompactionTokensMin, got.CompactionTokensMax)
	}
}

// A client from before the fields cannot reset what a team set.
func TestASaveWithoutTheTokenLinesKeepsTheStoredOnes(t *testing.T) {
	e := newEnv()
	e.stored("none", nil, nil)
	e.store.row.CompactionTokensMin, e.store.row.CompactionTokensMax = 70_000, 120_000

	decode(t, e.save(t, withTokens("", "")))

	if p := e.store.upserts[0]; p.CompactionTokensMin != 70_000 || p.CompactionTokensMax != 120_000 {
		t.Errorf("stored %d to %d, want the 70,000 to 120,000 already there", p.CompactionTokensMin, p.CompactionTokensMax)
	}
}

func TestAFirstSaveWithoutTheTokenLinesTakesTheDefaults(t *testing.T) {
	e := newEnv()

	decode(t, e.save(t, withTokens("", "")))

	if p := e.store.upserts[0]; p.CompactionTokensMin != 50_000 || p.CompactionTokensMax != 60_000 {
		t.Errorf("stored %d to %d, want the defaults", p.CompactionTokensMin, p.CompactionTokensMax)
	}
}

func TestATokenLineOutsideItsRangeIsRefusedAndChangesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		least, most, field string
	}{
		"least below 30,000":      {"29999", "60000", "compaction_tokens_min"},
		"least above 1,000,000":   {"1000001", "60000", "compaction_tokens_min"},
		"most below 30,000":       {"30000", "29999", "compaction_tokens_max"},
		"most above 1,000,000":    {"50000", "1000001", "compaction_tokens_max"},
		"least above most":        {"70000", "60000", "compaction_tokens_min, 70000, must not be above compaction_tokens_max, 60000"},
		"most under stored least": {"", "45000", "compaction_tokens_min, 50000, must not be above compaction_tokens_max, 45000"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			e.stored("none", nil, nil)

			rec := e.save(t, withTokens(tc.least, tc.most))

			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("status %d %q, want 400 naming %q", rec.Code, rec.Body, tc.field)
			}
			if len(e.store.upserts) != 0 {
				t.Errorf("a refused save wrote %+v", e.store.upserts)
			}
		})
	}
}

// The edges are in: 30,000 and 1,000,000 are allowed.
func TestTheTokenRangeIncludesItsEnds(t *testing.T) {
	got := decode(t, newEnv().save(t, withTokens("30000", "1000000")))

	if got.CompactionTokensMin != 30_000 || got.CompactionTokensMax != 1_000_000 {
		t.Errorf("returned %d to %d, want 30,000 to 1,000,000", got.CompactionTokensMin, got.CompactionTokensMax)
	}
}
