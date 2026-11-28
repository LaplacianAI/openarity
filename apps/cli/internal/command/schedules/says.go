package schedules

import (
	"strings"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/trigger"
)

func whenSays(w client.ScheduleWhen, zone string) string {
	switch w.Kind {
	case client.Every:
		if w.EverySeconds != nil {
			return "every " + trigger.LengthSays(*w.EverySeconds)
		}
	case client.Cron:
		if w.Cron != nil {
			return cli.Safe(*w.Cron)
		}
	case client.At:
		if w.At != nil {
			return "once, " + trigger.In(*w.At, zone)
		}
	}
	return cli.Safe(string(w.Kind))
}

func nextSays(s client.Schedule) string {
	switch {
	case s.NextFireAt != nil:
		return trigger.In(*s.NextFireAt, s.Timezone)
	case len(s.Upcoming) > 0 && s.Status == client.ScheduleStatusProposed:
		return "once confirmed"
	}
	return "-"
}

func upcomingSays(s client.Schedule) string {
	times := make([]string, 0, len(s.Upcoming))
	for _, at := range s.Upcoming {
		times = append(times, trigger.In(at, s.Timezone))
	}
	return strings.Join(times, ", ")
}
