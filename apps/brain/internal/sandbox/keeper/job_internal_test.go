package keeper

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func TestTheTemplatesCronTicksEveryThirtySeconds(t *testing.T) {
	t.Parallel()
	p := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	s, err := p.Parse(templatesCron)
	if err != nil {
		t.Fatalf("templatesCron = %q does not parse as DBOS reads it: %v", templatesCron, err)
	}
	first := s.Next(time.Date(2026, 11, 21, 3, 7, 1, 0, time.UTC))
	if got := s.Next(first).Sub(first); got != 30*time.Second {
		t.Errorf("ticks %v apart, want 30s", got)
	}
	if _, err := cron.ParseStandard(templatesCron); err == nil {
		t.Error("the five-field parser read the six-field form; the test would not catch a five-field constant")
	}
}

// The names are stored state: renaming either orphans the schedule.
func TestTheTemplatesJobRegistersOneSchedule(t *testing.T) {
	t.Parallel()
	j := TemplatesJob(NewTemplates(nil, nil, nil, Images{}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	if j.Name() != "sandbox.e2b-templates" {
		t.Errorf("Name = %q", j.Name())
	}
	specs := j.schedules()
	if len(specs) != 1 || specs[0].ScheduleName != "e2b-templates" ||
		specs[0].WorkflowName != "openarity.sandbox.e2b-templates" || specs[0].AutomaticBackfill {
		t.Errorf("schedules = %+v", specs)
	}
}
