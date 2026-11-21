package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const passesSaved = `{"model":{"name":"gpt-5","max_tokens":4096},"judge_model":null,
  "improvement_sample_share":10,"improvement_daily_passes":35,
  "search":{"provider":"none","url":null,"key_set":false},
  "improvement":true,"compaction_messages":100,"compaction_tokens_min":50000,"compaction_tokens_max":60000,
  "updated_at":"2026-11-21T09:00:00Z"}`

func TestSettingsShowDisplaysTheDailyPasses(t *testing.T) {
	settingsRoutes(t, passesSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "daily passes") || !strings.Contains(out, "35 at most") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDailyPassesIsSentOnlyWhenGiven(t *testing.T) {
	body, _, err := set(t, passesSaved, "--improvement", "off")
	if err != nil {
		t.Fatal(err)
	}
	if v, has := body["improvement_daily_passes"]; has {
		t.Errorf("daily passes = %v sent without a flag", v)
	}

	for in, want := range map[string]float64{"1": 1, "50": 50, "200": 200} {
		body, _, err := set(t, passesSaved, "--daily-passes", in)
		if err != nil {
			t.Fatal(err)
		}
		if body["improvement_daily_passes"] != want {
			t.Errorf("daily passes %s: sent %v", in, body["improvement_daily_passes"])
		}
	}
}

func TestDailyPassesOutOfRangeRefusesBeforeSending(t *testing.T) {
	for _, n := range []string{"0", "201", "-3"} {
		t.Run(n, func(t *testing.T) {
			script := settingsRoutes(t, passesSaved, http.StatusOK)

			_, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--daily-passes", n)
			if err == nil || !strings.Contains(err.Error(), "--daily-passes") || !strings.Contains(err.Error(), "1 to 200") {
				t.Fatalf("err = %v, want a clear --daily-passes error", err)
			}
			if len(script.All()) != 0 {
				t.Errorf("a refused flag made %d requests", len(script.All()))
			}
		})
	}
}
