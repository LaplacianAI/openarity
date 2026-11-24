package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const efficiencySaved = `{"model":{"name":"gpt-5","max_tokens":4096},"judge_model":null,
  "improvement_sample_share":10,"improvement_daily_passes":20,
  "improvement_pass_tokens":250000,"improvement_pass_minutes":15,"improvement_daily_runs":5,
  "improvement_efficiency":true,"improvement_efficiency_runs":20,"improvement_efficiency_days":14,
  "improvement_efficiency_spread":150,"improvement_efficiency_mark_runs":3,"improvement_efficiency_margin":1,
  "search":{"provider":"none","url":null,"key_set":false},
  "improvement":true,"compaction_messages":100,"compaction_tokens_min":50000,"compaction_tokens_max":60000,
  "updated_at":"2026-11-21T09:00:00Z"}`

func TestSettingsShowDisplaysTheEfficiencyPass(t *testing.T) {
	settingsRoutes(t, efficiencySaved, http.StatusOK)
	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"efficiency pass", "20 good runs in 14 days", "costliest 1.5×", "waste mark in 3 runs", "flagged worse 1 bad run sooner"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestTheEfficiencySettingsAreSentOnlyWhenGiven(t *testing.T) {
	body, _, err := set(t, efficiencySaved, "--improvement", "on")
	if err != nil {
		t.Fatal(err)
	}
	for k := range body {
		if strings.HasPrefix(k, "improvement_efficiency") {
			t.Errorf("%s sent without a flag", k)
		}
	}
	body, _, err = set(t, efficiencySaved, "--efficiency", "off", "--efficiency-runs", "40", "--efficiency-days", "21",
		"--efficiency-spread", "300", "--efficiency-mark-runs", "5", "--efficiency-margin", "0")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"improvement_efficiency": false, "improvement_efficiency_runs": float64(40), "improvement_efficiency_days": float64(21),
		"improvement_efficiency_spread": float64(300), "improvement_efficiency_mark_runs": float64(5), "improvement_efficiency_margin": float64(0),
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
}

func TestTheEfficiencySettingsOutOfRangeRefuseBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"--efficiency", "yes"}, "on or off"},
		{[]string{"--efficiency-runs", "9"}, "10 to 500"},
		{[]string{"--efficiency-days", "61"}, "7 to 60"},
		{[]string{"--efficiency-spread", "124"}, "125 to 1000"},
		{[]string{"--efficiency-mark-runs", "51"}, "2 to 50"},
		{[]string{"--efficiency-margin", "4"}, "0 to 3"},
		{[]string{"--efficiency-runs", "10", "--efficiency-mark-runs", "11"}, "cannot be above"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			script := settingsRoutes(t, efficiencySaved, http.StatusOK)
			_, err := clitest.Execute(t, commands, append([]string{"teams", "settings", "set", teamID}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v, want %q", err, tc.says)
			}
			if len(script.All()) != 0 {
				t.Errorf("a refused flag made %d requests", len(script.All()))
			}
		})
	}
}

// The margin says how much sooner an efficiency change is flagged, and a
// brain that sent none is shown as unknown, not as the strictest.
func TestTheMarginIsSaidInWords(t *testing.T) {
	m := func(v int32) *int32 { return &v }
	for _, tc := range []struct {
		margin *int32
		want   string
	}{
		{nil, "flagged worse ? bad runs sooner"},
		{m(0), "watched as strictly as any change"},
		{m(1), "flagged worse 1 bad run sooner"},
		{m(2), "flagged worse 2 bad runs sooner"},
	} {
		if got := strictWords(tc.margin); got != tc.want {
			t.Errorf("margin %v: %q, want %q", tc.margin, got, tc.want)
		}
	}
}

func TestTheReplaySettingsAreSentOnlyWhenGiven(t *testing.T) {
	body, _, err := set(t, efficiencySaved, "--improvement", "on")
	if err != nil {
		t.Fatal(err)
	}
	for k := range body {
		if strings.HasPrefix(k, "improvement_replay") {
			t.Errorf("%s sent without a flag", k)
		}
	}
	body, _, err = set(t, efficiencySaved, "--replay", "on", "--replay-runs", "6", "--replay-tokens", "300000",
		"--replay-minutes", "15", "--replay-daily", "80")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"improvement_replay": true, "improvement_replay_runs": float64(6), "improvement_replay_tokens": float64(300000),
		"improvement_replay_minutes": float64(15), "improvement_replay_daily": float64(80),
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
}

func TestTheReplaySettingsOutOfRangeRefuseBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"--replay", "yes"}, "on or off"},
		{[]string{"--replay-runs", "0"}, "1 to 10"},
		{[]string{"--replay-runs", "11"}, "1 to 10"},
		{[]string{"--replay-tokens", "19999"}, "20000 to 1000000"},
		{[]string{"--replay-minutes", "31"}, "1 to 30"},
		{[]string{"--replay-daily", "501"}, "1 to 500"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			script := settingsRoutes(t, efficiencySaved, http.StatusOK)
			_, err := clitest.Execute(t, commands, append([]string{"teams", "settings", "set", teamID}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v, want %q", err, tc.says)
			}
			if len(script.All()) != 0 {
				t.Errorf("a refused flag made %d requests", len(script.All()))
			}
		})
	}
}

func TestSettingsShowDisplaysTryingOnPastRuns(t *testing.T) {
	saved := strings.Replace(efficiencySaved, `"improvement_efficiency_margin":1,`,
		`"improvement_efficiency_margin":1,"improvement_replay":true,"improvement_replay_runs":4,"improvement_replay_tokens":200000,"improvement_replay_minutes":10,"improvement_replay_daily":40,`, 1)
	settingsRoutes(t, saved, http.StatusOK)
	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"try on past runs", "4 past runs a change", "200000 tokens and 10 minutes a try", "40 tries a day"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
