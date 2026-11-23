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
	if !strings.HasPrefix(got, "efficiency, from 34 good runs: repeated call crm__lookup, spread, long tool wait ") || strings.Contains(got, "\x1b") {
		t.Errorf("trigger = %q, want the reasons in words and the escape made safe", got)
	}
	flag := "no_saving"
	if got := flagWords(&flag, []string{"time"}); !strings.Contains(got, "did not get quicker,") {
		t.Errorf("flag = %q", got)
	}
	ms := int64(41_000)
	if got := secondsOf(&ms); got != "41.0 s" {
		t.Errorf("seconds = %q", got)
	}
}
