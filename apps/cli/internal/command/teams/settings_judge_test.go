package teams

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const judgedSaved = `{"model":{"name":"gpt-5","max_tokens":4096},
  "judge_model":{"name":"small-judge","max_tokens":2048,"temperature":0.1},
  "improvement_sample_share":25,"improvement_since":"2026-11-20T08:30:00Z",
  "search":{"provider":"none","url":null,"key_set":false},
  "improvement":true,"compaction_messages":100,"compaction_tokens_min":50000,"compaction_tokens_max":60000,
  "updated_at":"2026-11-21T09:00:00Z"}`

const sharedJudge = `{"model":{"name":"gpt-5","max_tokens":4096},"judge_model":null,
  "improvement_sample_share":10,
  "search":{"provider":"none","url":null,"key_set":false},
  "improvement":false,"compaction_messages":100,"compaction_tokens_min":50000,"compaction_tokens_max":60000,
  "updated_at":"2026-11-21T09:00:00Z"}`

func set(t *testing.T, stored string, args ...string) (map[string]any, string, error) {
	t.Helper()
	script := settingsRoutes(t, stored, http.StatusOK)
	out, err := clitest.Execute(t, commands, append([]string{"teams", "settings", "set", teamID}, args...)...)
	if err != nil {
		return nil, out, err
	}
	return put(t, script), out, nil
}

func TestSettingsShowDisplaysTheJudgeAndShare(t *testing.T) {
	settingsRoutes(t, judgedSaved, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{
		"judge model", "small-judge", "2048", "temperature 0.1",
		"sample share", "25%", "improving since", "2026-11-20 08:30",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestSettingsShowSaysTheJudgeIsTheTeamsModelWhenNull(t *testing.T) {
	settingsRoutes(t, sharedJudge, http.StatusOK)

	out, err := clitest.Execute(t, commands, "teams", "settings", "show", teamID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "same as the team's model") || !strings.Contains(out, "10%") {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out, "improving since") {
		t.Errorf("shows a since the brain did not send:\n%s", out)
	}
}

func TestJudgeModelAloneGetsDefaultMaxTokens(t *testing.T) {
	body, _, err := set(t, sharedJudge, "--judge-model", "small-judge")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := body["judge_model"].(map[string]any)
	if m["name"] != "small-judge" || m["max_tokens"] != float64(defaultJudgeMaxTokens) {
		t.Errorf("judge_model = %v", m)
	}
	if _, has := m["temperature"]; has {
		t.Errorf("invented a temperature: %v", m)
	}
}

func TestJudgeModelFlagsAreAllSent(t *testing.T) {
	body, _, err := set(t, sharedJudge, "--judge-model", "j", "--judge-max-tokens", "512", "--judge-temperature", "0.5")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := body["judge_model"].(map[string]any)
	if m["name"] != "j" || m["max_tokens"] != float64(512) || m["temperature"] != 0.5 {
		t.Errorf("judge_model = %v", m)
	}
}

func TestJudgeFlagsAdjustTheStoredJudgeModel(t *testing.T) {
	body, _, err := set(t, judgedSaved, "--judge-max-tokens", "1024")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := body["judge_model"].(map[string]any)
	if m["name"] != "small-judge" || m["max_tokens"] != float64(1024) || m["temperature"] != 0.1 {
		t.Errorf("judge_model = %v, want the stored one at 1024", m)
	}
}

// Left out, the brain keeps the stored judge model: a command that changes
// something else must not clear it, nor send it back to be re-checked.
func TestJudgeModelIsLeftOutWhenNoJudgeFlagIsGiven(t *testing.T) {
	body, _, err := set(t, judgedSaved, "--improvement", "off")
	if err != nil {
		t.Fatal(err)
	}
	if v, has := body["judge_model"]; has {
		t.Errorf("judge_model = %v sent, want it left out", v)
	}
	if _, has := body["improvement_sample_share"]; has {
		t.Error("sample share sent without a flag")
	}
}

func TestNoJudgeModelSendsNull(t *testing.T) {
	body, _, err := set(t, judgedSaved, "--no-judge-model")
	if err != nil {
		t.Fatal(err)
	}
	v, has := body["judge_model"]
	if !has || v != nil {
		t.Errorf("judge_model = %v (present %v), want an explicit null", v, has)
	}
}

func TestSampleShareIsSentIncludingZero(t *testing.T) {
	for share, want := range map[string]float64{"0": 0, "35": 35, "100": 100} {
		body, _, err := set(t, judgedSaved, "--sample-share", share)
		if err != nil {
			t.Fatal(err)
		}
		if body["improvement_sample_share"] != want {
			t.Errorf("share %s: sent %v", share, body["improvement_sample_share"])
		}
	}
}

func TestJudgeFlagsRefuseBeforeSending(t *testing.T) {
	for name, args := range map[string][]string{
		"a judge model and no judge model":   {"--judge-model", "j", "--no-judge-model"},
		"max tokens and no judge model":      {"--judge-max-tokens", "9", "--no-judge-model"},
		"an empty judge model":               {"--judge-model", " "},
		"a share over 100":                   {"--sample-share", "101"},
		"a negative share":                   {"--sample-share", "-1"},
		"max tokens with no judge to adjust": {"--judge-max-tokens", "9"},
	} {
		t.Run(name, func(t *testing.T) {
			script := settingsRoutes(t, sharedJudge, http.StatusOK)

			_, err := clitest.Execute(t, commands, append([]string{"teams", "settings", "set", teamID}, args...)...)
			if err == nil {
				t.Fatal("accepted")
			}
			if script.Calls(http.MethodPut, settingsPath) != 0 {
				t.Error("sent a PUT anyway")
			}
		})
	}
}

func TestJudgeModelRefusedByTheBrainShowsItsReason(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{
		"GET " + settingsPath: {Status: http.StatusOK, Body: sharedJudge},
		"PUT " + settingsPath: {Status: http.StatusBadRequest, Body: "judge_model.max_tokens must be at least 1"},
	})

	_, err := clitest.Execute(t, commands, "teams", "settings", "set", teamID, "--judge-model", "j")
	if err == nil || !strings.Contains(err.Error(), "judge_model.max_tokens") {
		t.Errorf("err = %v", err)
	}
}
