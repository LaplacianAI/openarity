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
	p := &client.ImproverPass{Trigger: client.ImproverPassTriggerEfficiency, RunsSeen: &n}
	if got := triggerCell(p); got != "efficiency, from 34 good runs" {
		t.Errorf("trigger = %q", got)
	}
	flag := "no_saving"
	if got := flagWords(&flag); !strings.Contains(got, "did not get cheaper or quicker") {
		t.Errorf("flag = %q", got)
	}
	ms := int64(41_000)
	if got := secondsOf(&ms); got != "41.0 s" {
		t.Errorf("seconds = %q", got)
	}
}
