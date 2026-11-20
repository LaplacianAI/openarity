package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

func TestSettingsShowSaysWhenARunCompacts(t *testing.T) {
	settingsRoutes(t, braveSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("teams settings show: %v", err)
	}
	if want := "250 messages, or 80,000 to 90,000 tokens, a tenth of the run's budget"; !strings.Contains(out, want) {
		t.Errorf("missing %q:\n%s", want, out)
	}
}

func TestSettingsShowSaysAFixedTokenLineOnce(t *testing.T) {
	fixed := strings.Replace(neverSaved, `"compaction_tokens_min":50000`, `"compaction_tokens_min":60000`, 1)
	settingsRoutes(t, fixed, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("teams settings show: %v", err)
	}
	if want := "100 messages, or 60,000 tokens"; !strings.Contains(out, want) || strings.Contains(out, "a tenth") {
		t.Errorf("want %q alone:\n%s", want, out)
	}
}

func TestSettingsSetSendsTheTokenLinesGiven(t *testing.T) {
	script := settingsRoutes(t, braveSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--compaction-tokens-min", "40000", "--compaction-tokens-max", "70000"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	body := put(t, script)
	if body["compaction_tokens_min"] != float64(40_000) || body["compaction_tokens_max"] != float64(70_000) {
		t.Errorf("sent %v to %v, want 40000 to 70000", body["compaction_tokens_min"], body["compaction_tokens_max"])
	}
}

// Left out, the brain keeps what is stored, so a flag not given is not sent.
func TestSettingsSetSendsNoTokenLineNotGiven(t *testing.T) {
	script := settingsRoutes(t, braveSaved, http.StatusOK)

	if _, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID,
		"--compaction-tokens-max", "120000"); err != nil {
		t.Fatalf("teams settings set: %v", err)
	}

	body := put(t, script)
	if _, sent := body["compaction_tokens_min"]; sent {
		t.Errorf("sent compaction_tokens_min %v; leaving it out is what keeps the stored one", body["compaction_tokens_min"])
	}
	if body["compaction_tokens_max"] != float64(120_000) {
		t.Errorf("compaction_tokens_max = %v, want 120000", body["compaction_tokens_max"])
	}
}
