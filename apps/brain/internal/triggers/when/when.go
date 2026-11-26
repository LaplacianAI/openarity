// Package when is a schedule's time rules: when it next fires, what is due
// when a tick finally looks, and what a rule may say. It holds no database
// and no clock; every time is given.
//
// Three kinds of rule:
//
//   - at: one instant, fired once;
//   - every: a fixed interval counted from an anchor, the first fire;
//   - cron: five fields (minute resolution) or a descriptor such as @daily,
//     read in the schedule's zone.
package when

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type Kind string

const (
	At    Kind = "at"
	Every Kind = "every"
	Cron  Kind = "cron"
)

// LocalLayout is a wall time in a schedule's zone, as last_local keeps it.
const LocalLayout = "2006-01-02T15:04"

const (
	// MaxAhead is how far off a one-shot may be, and how far a cron rule must
	// fire within to be a rule at all.
	MaxAhead = 366 * 24 * time.Hour
	// MaxEvery is the longest interval.
	MaxEvery = 31 * 24 * time.Hour
	// lateAfter is how late a fire may be and still count as on time.
	lateAfter = time.Minute
	// atGrace is how late a one-shot still runs (*user*, 2026-11-26).
	atGrace  = 24 * time.Hour
	minGrace = 2 * time.Minute
	maxGrace = 2 * time.Hour
	// gapSample is how many fires MinGap looks across.
	gapSample = 50
	// maxSteps bounds how many due times are walked: a year of a rule that
	// fires each minute. maxMissed is how many of the newest are kept as
	// missed rows; a history is not a ledger of every miss.
	maxSteps  = 600_000
	maxMissed = 20
	// everyHour is a cron hour field that names all 24.
	everyHour = 1<<24 - 1
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// A Rule is a parsed schedule. Anchor is the first fire for every, and is not
// used by the others.
type Rule struct {
	Kind   Kind
	At     time.Time
	Every  time.Duration
	Cron   string
	Zone   *time.Location
	Anchor time.Time
	spec   cron.Schedule
}

// Parse reads a stored rule: at is an RFC 3339 instant, every whole seconds,
// cron five fields or a descriptor. zone is an IANA name.
func Parse(kind, rule, zone string, anchor time.Time) (Rule, error) {
	loc, err := Zone(zone)
	if err != nil {
		return Rule{}, err
	}
	r := Rule{Kind: Kind(kind), Zone: loc, Anchor: anchor}
	switch r.Kind {
	case At:
		if r.At, err = time.Parse(time.RFC3339, rule); err != nil {
			return Rule{}, fmt.Errorf("the time %q is not an RFC 3339 instant", rule)
		}
	case Every:
		secs, err := strconv.ParseInt(rule, 10, 64)
		if err != nil || secs <= 0 {
			return Rule{}, fmt.Errorf("the interval %q is not a whole number of seconds", rule)
		}
		r.Every = time.Duration(secs) * time.Second
	case Cron:
		if r.spec, err = parseCron(rule); err != nil {
			return Rule{}, err
		}
		r.Cron = rule
	default:
		return Rule{}, fmt.Errorf("a schedule is at a time, every interval, or a cron rule, not %q", kind)
	}
	return r, nil
}

// Zone is an IANA zone by name. "Local" is refused: it is whatever the
// brain's machine is set to.
func Zone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("the time zone %q is not an IANA name such as Europe/London", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("the time zone %q is not an IANA name such as Europe/London", name)
	}
	return loc, nil
}

func parseCron(rule string) (cron.Schedule, error) {
	trimmed := strings.TrimSpace(rule)
	// The zone is the schedule's, never one in the rule; and @every is said
	// as an interval.
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "TZ=") || strings.HasPrefix(upper, "CRON_TZ=") {
		return nil, errors.New("a cron rule names no zone; the schedule's zone is set beside it")
	}
	if strings.HasPrefix(trimmed, "@every") {
		return nil, errors.New("say an interval as every, not as a cron rule")
	}
	spec, err := parser.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("the cron rule %q does not parse: five fields, minute hour day month weekday, such as \"0 9 * * 1-5\"", rule)
	}
	return spec, nil
}

// Stored is the rule as the schedules table keeps it.
func (r Rule) Stored() string {
	switch r.Kind {
	case At:
		return r.At.UTC().Format(time.RFC3339)
	case Every:
		return strconv.FormatInt(int64(r.Every/time.Second), 10)
	case Cron:
		return r.Cron
	}
	return ""
}

// Next is the first fire strictly after after. False is a one-shot already
// past, or a cron rule that never fires again.
func (r Rule) Next(after time.Time) (time.Time, bool) {
	switch r.Kind {
	case At:
		return r.At, r.At.After(after)
	case Every:
		if after.Before(r.Anchor) {
			return r.Anchor, true
		}
		n := after.Sub(r.Anchor)/r.Every + 1
		return r.Anchor.Add(n * r.Every), true
	case Cron:
		next := r.spec.Next(after.In(r.Zone))
		return next, !next.IsZero()
	}
	return time.Time{}, false
}

// Upcoming is up to n fires after after, as a confirmation names them.
func (r Rule) Upcoming(after time.Time, n int) []time.Time {
	var out []time.Time
	for t := after; len(out) < n; {
		next, ok := r.Next(t)
		if !ok {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}

// MinGap is the shortest time between two fires among the next few after
// after: what a cron rule is held to the minimum interval by. Zero is a rule
// that fires at most once.
func (r Rule) MinGap(after time.Time) time.Duration {
	if r.Kind == Every {
		return r.Every
	}
	fires := r.Upcoming(after, gapSample)
	var gap time.Duration
	for i := 1; i < len(fires); i++ {
		if d := fires[i].Sub(fires[i-1]); gap == 0 || d < gap {
			gap = d
		}
	}
	return gap
}

// Check is whether a rule may be saved at now under a team's minimum
// interval. Its error is said to a person.
func (r Rule) Check(now time.Time, minInterval time.Duration) error {
	switch r.Kind {
	case At:
		if !r.At.After(now) {
			return errors.New("the time has already passed")
		}
		if r.At.Sub(now) > MaxAhead {
			return errors.New("the time is more than a year away")
		}
	case Every:
		if r.Every%time.Minute != 0 {
			return errors.New("the interval is in whole minutes")
		}
		if r.Every < minInterval {
			return fmt.Errorf("this team runs a schedule at most once every %s", words(minInterval))
		}
		if r.Every > MaxEvery {
			return errors.New("the interval is at most 31 days")
		}
	case Cron:
		next, ok := r.Next(now)
		if !ok || next.Sub(now) > MaxAhead {
			return errors.New("the cron rule does not fire within a year")
		}
		if gap := r.MinGap(now); gap != 0 && gap < minInterval {
			return fmt.Errorf("this team runs a schedule at most once every %s", words(minInterval))
		}
	}
	return nil
}

// Grace is how late a fire may run: half the period, between 2 minutes and
// 2 hours; a one-shot, a day.
func Grace(r Rule, at time.Time) time.Duration {
	if r.Kind == At {
		return atGrace
	}
	period := r.Every
	if r.Kind == Cron {
		next, ok := r.Next(at)
		if !ok {
			return maxGrace
		}
		period = next.Sub(at)
	}
	return min(max(period/2, minGrace), maxGrace)
}

// Outcome is how a fire went out, as schedule_fires keeps it.
type Outcome string

const (
	Started Outcome = "started"
	Late    Outcome = "late"
	CatchUp Outcome = "catch_up"
	Missed  Outcome = "missed"
)

// A Slot is a fire that is due.
type Slot struct {
	ScheduledFor time.Time
	Outcome      Outcome
	// Local is its wall time in the schedule's zone.
	Local string
}

// A Plan is what a tick does with a schedule it found due.
type Plan struct {
	// Fire is the one fire to run, if any.
	Fire *Slot
	// Missed are times that came and went for good, newest kept.
	Missed []time.Time
	// Next is the fire after now; zero when Done.
	Next time.Time
	// Done is a one-shot that has fired or been missed.
	Done bool
}

// pinsHour is a cron rule that names its hours, such as 1:30 each day: the
// hour the clocks go back repeats 1:30 on the wall, and it means one fire. A
// rule over every hour means each real hour, the repeated one too.
func (r Rule) pinsHour() bool {
	spec, ok := r.spec.(*cron.SpecSchedule)
	return ok && spec.Hour&everyHour != everyHour
}

// Due is what a schedule whose next fire was from does at now. Several due
// times collapse into one: within the grace the newest runs (on time or
// late); beyond it, or with more than one due, the newest runs as a catch-up
// and the rest are missed. Never one run per missed time. For a rule that
// pins its hours, a time whose local wall time is not after the last one
// kept is in the hour the clocks go back, a second time, and is dropped:
// 01:00 and 01:30 each fire once, though the wall reads each twice.
// lastLocal is the last fire's; wall times in LocalLayout sort as they read.
func Due(r Rule, from, now time.Time, lastLocal string) Plan {
	pinned := r.Kind == Cron && r.pinsHour()
	// The newest due times, the one to run and the misses kept before it.
	var slots []time.Time
	due, prev := 0, lastLocal
	t, ok := from, true
	for steps := 0; ok && !t.After(now) && steps < maxSteps; steps++ {
		if local := t.In(r.Zone).Format(LocalLayout); !pinned || local > prev {
			due, prev = due+1, local
			if slots = append(slots, t); len(slots) > maxMissed+1 {
				slots = slots[1:]
			}
		}
		t, ok = r.Next(t)
	}
	if ok && !t.After(now) {
		// More due than a year of minutes: start again from now.
		t, ok = r.Next(now)
	}

	plan := Plan{}
	if ok {
		plan.Next = t
	} else {
		plan.Done = true
	}
	if len(slots) == 0 {
		return plan
	}
	if r.Kind == At {
		plan.Done, plan.Next = true, time.Time{}
	}

	newest := slots[len(slots)-1]
	late := now.Sub(newest)
	grace := Grace(r, newest)
	slot := Slot{ScheduledFor: newest, Local: newest.In(r.Zone).Format(LocalLayout)}
	switch {
	case r.Kind == At && late > grace:
		plan.Missed = []time.Time{newest}
		return plan
	case due == 1 && late < lateAfter:
		slot.Outcome = Started
	case due == 1 && late <= grace:
		slot.Outcome = Late
	default:
		slot.Outcome = CatchUp
	}
	plan.Fire = &slot
	plan.Missed = slots[:len(slots)-1]
	return plan
}

// words says a duration as a person would: "60 minutes", "2 hours", "1 day".
func words(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d >= time.Hour && d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/time.Minute), "minute")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
