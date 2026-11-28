package schedules

import (
	"strconv"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

// shownTime is how a time reads in a table, in the schedule's own zone.
const shownTime = "Mon 2 Jan 15:04"

// In is t in zone as a person reads it; UTC when this machine does not know
// the zone.
func In(t time.Time, zone string) string {
	if loc, err := time.LoadLocation(zone); err == nil {
		t = t.In(loc)
	} else {
		t = t.UTC()
	}
	return t.Format(shownTime)
}

// LengthSays is a length in seconds as a person says it: 90m, 2h, 1d.
func LengthSays(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d%(24*time.Hour) == 0:
		return itoa(int64(d/(24*time.Hour))) + "d"
	case d%time.Hour == 0:
		return itoa(int64(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return itoa(int64(d/time.Minute)) + "m"
	}
	return d.String()
}

func whenSays(w client.ScheduleWhen, zone string) string {
	switch w.Kind {
	case client.Every:
		if w.EverySeconds != nil {
			return "every " + LengthSays(*w.EverySeconds)
		}
	case client.Cron:
		if w.Cron != nil {
			return cli.Safe(*w.Cron)
		}
	case client.At:
		if w.At != nil {
			return "once, " + In(*w.At, zone)
		}
	}
	return string(w.Kind)
}

// WhereSays is where one answers.
func WhereSays(t client.ScheduleTarget) string {
	if t.Kind == client.ScheduleTargetKindOwn {
		return "its own conversation"
	}
	if t.Label != nil && *t.Label != "" {
		return cli.Safe(*t.Label)
	}
	return string(t.Kind)
}

func nextSays(s client.Schedule) string {
	switch {
	case s.NextFireAt != nil:
		return In(*s.NextFireAt, s.Timezone)
	case len(s.Upcoming) > 0 && s.Status == client.ScheduleStatusProposed:
		return "once confirmed"
	}
	return "-"
}

func upcomingSays(s client.Schedule) string {
	times := make([]string, 0, len(s.Upcoming))
	for _, at := range s.Upcoming {
		times = append(times, In(at, s.Timezone))
	}
	return strings.Join(times, ", ")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
