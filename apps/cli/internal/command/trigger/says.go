package trigger

import (
	"strconv"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

// ShownTime is how a time reads in a table.
const ShownTime = "Mon 2 Jan 15:04"

// In is t in zone as a person reads it; UTC when this machine does not know
// the zone.
func In(t time.Time, zone string) string {
	if loc, err := time.LoadLocation(zone); err == nil {
		t = t.In(loc)
	} else {
		t = t.UTC()
	}
	return t.Format(ShownTime)
}

// LengthSays is a length in seconds as a person says it: 90m, 2h, 1d.
func LengthSays(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return d.String()
}

// WhereSays is where one answers.
func WhereSays(t client.ScheduleTarget) string {
	if t.Kind == client.ScheduleTargetKindOwn {
		return "its own conversation"
	}
	if t.Label != nil && *t.Label != "" {
		return cli.Safe(*t.Label)
	}
	return cli.Safe(string(t.Kind))
}
