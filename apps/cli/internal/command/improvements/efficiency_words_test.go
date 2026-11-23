package improvements

import (
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

// An efficiency pass says so, with the good runs it drew from; a change
// that saved nothing is said in words.
func TestEfficiencyIsSaidInWords(t *testing.T) {
	n := int32(34)
	p := &client.ImproverPass{
		Trigger: client.ImproverPassTriggerEfficiency, RunsSeen: &n,
		Signatures: []string{"efficiency:repeated_call:crm__lookup", "efficiency:spread", "efficiency:long_tool_wait:x\x1b[2Jy"},
	}
	got := triggerCell(p)
	if !strings.HasPrefix(got, "efficiency, from 34 good runs: the same call made twice crm__lookup, costly runs far above the usual, a long wait on a tool ") || strings.Contains(got, "\x1b") {
		t.Errorf("trigger = %q, want the reasons in words and the escape made safe", got)
	}
	if got := triggerCell(&client.ImproverPass{Trigger: client.ImproverPassTriggerEfficiency, Signatures: []string{"efficiency:new_mark"}}); got != "efficiency: new mark" {
		t.Errorf("an unknown reason = %q, want its name in words", got)
	}
	if got := triggerCell(&client.ImproverPass{Trigger: client.ImproverPassTriggerEfficiency, Signatures: []string{"efficiency:odd\x1b[2J_mark"}}); strings.Contains(got, "\x1b") {
		t.Errorf("an unknown reason = %q, want the escape made safe", got)
	}
	if got := triggerCell(&client.ImproverPass{Trigger: client.ImproverPassTriggerEfficiency, Signatures: []string{"tool:crm__lookup:timeout", "efficiency:spread"}}); got != "efficiency: costly runs far above the usual" {
		t.Errorf("a pass with another signature = %q, want only its own reasons", got)
	}
	ms := int64(41_000)
	if got := secondsOf(&ms); got != "41.0 s" {
		t.Errorf("seconds = %q", got)
	}
}

// "No saving" says what the change aimed at, from the proposal's saves
// carried to its watch.
func TestNoSavingFollowsWhatWasAimedAt(t *testing.T) {
	flag := client.ImprovementWatchFlag("no_saving")
	for _, tc := range []struct {
		saves []client.ImprovementSaves
		want  string
	}{
		{[]client.ImprovementSaves{"cost"}, "did not get cheaper,"},
		{[]client.ImprovementSaves{"time"}, "did not get quicker,"},
		{[]client.ImprovementSaves{"cost", "time"}, "did not get both cheaper and quicker,"},
		{nil, "did not get cheaper or quicker,"},
	} {
		v := viewOf(&client.Improvement{Saves: tc.saves, Watch: &client.ImprovementWatch{Status: "flagged", Flag: &flag}})
		if got := flagWords(v.Watch.Flag, v.Watch.saves); !strings.Contains(got, tc.want) {
			t.Errorf("saves %v: %q, want %q", tc.saves, got, tc.want)
		}
	}
}
