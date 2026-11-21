package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const budgetSaved = `{"model":{"name":"gpt-5","max_tokens":4096},"judge_model":null,
  "improvement_sample_share":10,"improvement_daily_passes":20,
  "improvement_pass_tokens":250000,"improvement_pass_minutes":15,
  "search":{"provider":"none","url":null,"key_set":false},
  "improvement":true,"compaction_messages":100,"compaction_tokens_min":50000,"compaction_tokens_max":60000,
  "updated_at":"2026-11-21T09:00:00Z"}`

func TestSettingsShowDisplaysThePassBudget(t *testing.T) {
	settingsRoutes(t, budgetSaved, http.StatusOK)
	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "pass budget") || !strings.Contains(out, "250000 tokens, 15 minutes a try") {
		t.Errorf("output:\n%s", out)
	}
}

func TestThePassBudgetIsSentOnlyWhenGiven(t *testing.T) {
	body, _, err := set(t, budgetSaved, "--improvement", "off")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"improvement_pass_tokens", "improvement_pass_minutes"} {
		if v, has := body[k]; has {
			t.Errorf("%s = %v sent without a flag", k, v)
		}
	}
	body, _, err = set(t, budgetSaved, "--pass-tokens", "20000", "--pass-minutes", "25")
	if err != nil {
		t.Fatal(err)
	}
	if body["improvement_pass_tokens"] != float64(20000) || body["improvement_pass_minutes"] != float64(25) {
		t.Errorf("sent %v and %v", body["improvement_pass_tokens"], body["improvement_pass_minutes"])
	}
}

func TestThePassBudgetOutOfRangeRefusesBeforeSending(t *testing.T) {
	for _, tc := range []struct{ flag, n, says string }{
		{"--pass-tokens", "19999", "20000 to 2000000"},
		{"--pass-tokens", "2000001", "20000 to 2000000"},
		{"--pass-minutes", "0", "1 to 25"},
		{"--pass-minutes", "26", "1 to 25"},
	} {
		t.Run(tc.flag+tc.n, func(t *testing.T) {
			script := settingsRoutes(t, budgetSaved, http.StatusOK)
			_, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, tc.flag, tc.n)
			if err == nil || !strings.Contains(err.Error(), tc.flag) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v, want a clear %s error", err, tc.flag)
			}
			if len(script.All()) != 0 {
				t.Errorf("a refused flag made %d requests", len(script.All()))
			}
		})
	}
}
