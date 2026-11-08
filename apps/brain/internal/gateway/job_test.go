package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// The sweep is the retries' clock: a minute apart, so a retry is at most a
// minute late, and no tighter, because DBOS keeps a row for every tick.
func TestTheReplySweepFiresOnceAMinute(t *testing.T) {
	t.Parallel()

	schedule, err := cron.NewParser(cron.Second | cron.Minute | cron.Hour |
		cron.Dom | cron.Month | cron.Dow | cron.Descriptor).Parse(replySweepCron)
	if err != nil {
		t.Fatalf("replySweepCron = %q does not parse: %v", replySweepCron, err)
	}
	first := schedule.Next(time.Date(2026, 11, 8, 3, 7, 12, 0, time.UTC))
	if got := schedule.Next(first).Sub(first); got != time.Minute {
		t.Errorf("ticks are %v apart, want 1m", got)
	}
}

func TestTheReplyJobRefusesToStartWithNoSender(t *testing.T) {
	t.Parallel()

	_, err := ReplyJob(nil).Register(nil)
	if err == nil || !strings.Contains(err.Error(), "no sender") {
		t.Errorf("Register = %v, want it refused for having no sender", err)
	}
}

func TestTheReplySweepDoesNotBackfill(t *testing.T) {
	t.Parallel()

	specs := ReplyJob(&Sender{}).schedules()
	if len(specs) != 1 || specs[0].AutomaticBackfill || specs[0].WorkflowName != sweepRepliesWorkflow {
		t.Errorf("schedules = %+v, want one sweep without backfill", specs)
	}
}
