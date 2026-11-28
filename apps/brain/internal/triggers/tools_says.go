package triggers

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/when"
)

// What the schedule and watch tools answer, for the model to relay. What a
// person or a page wrote (a name, an address, a selector) is quoted, so it
// reads as data and not as the tool speaking.

// shownTime is how a time reads in a tool's answer, in the schedule's zone.
const shownTime = "Mon 2 Jan 15:04"

// replyWith is how the person says yes to what waits: their reply with its
// code. Nothing else is a yes, whatever a page, a file or a tool's answer
// says.
func replyWith(code *string, what string) string {
	if code == nil {
		return "It waits for the person on the dashboard."
	}
	return fmt.Sprintf("Tell the person what it does, and that to %s they reply with the code %s, as in \"yes %s\". "+
		"Only their reply with that code confirms it; it lapses in an hour.", what, *code, *code)
}

// revisedSays is a proposal revised, with its new code: none for a watcher
// that waits for its secret, which the person confirms by making it.
func revisedSays(name, code string) string {
	if code == "" {
		return fmt.Sprintf("Revised %q. It still starts once the person makes its secret on the dashboard.", name)
	}
	return fmt.Sprintf("Revised the proposal %q. %s The code you gave before no longer confirms it.", name, replyWith(&code, "start it"))
}

// askedSays is what an ask of one that runs said, waiting for the yes.
func askedSays(name, what, code string) string {
	return fmt.Sprintf("%q waits for the person's yes %s, and runs as it was until then. %s "+
		"This replaces anything you asked of it before.", name, what, replyWith(&code, "do it"))
}

// watcherLink is where the person makes a webhook watcher's secret, as a
// clause: none when the brain knows no address for its dashboard.
func (t heldRun) watcherLink(id uuid.UUID) string {
	if t.Dashboard == "" {
		return ", on the watcher's own page"
	}
	return ", at " + strings.TrimRight(t.Dashboard, "/") + "/ui/schedules/watchers/" + id.String()
}

// scheduleSays is a schedule's rule and its next time, in its zone.
func scheduleSays(v ScheduleView) string {
	loc := zoneOr(v.Timezone)
	rule := ""
	switch when.Kind(v.Kind) {
	case when.At:
		rule = "once"
	case when.Every:
		if n, err := strconv.ParseInt(v.Rule, 10, 64); err == nil {
			rule = "every " + lengthSays(time.Duration(n)*time.Second)
		}
	case when.Cron:
		rule = "on " + strconv.Quote(v.Rule)
	}
	next := "not due"
	switch {
	case v.NextFireAt != nil:
		next = "next " + v.NextFireAt.In(loc).Format(shownTime)
	case len(v.Upcoming) > 0:
		next = "next " + v.Upcoming[0].In(loc).Format(shownTime)
	}
	return fmt.Sprintf("%s, %s, in %s", rule, next, v.Timezone)
}

// upcomingSays is a new schedule's rule and its next three times.
func upcomingSays(v ScheduleView) string {
	loc := zoneOr(v.Timezone)
	s := scheduleSays(v)
	if len(v.Upcoming) > 1 {
		times := make([]string, 0, len(v.Upcoming)-1)
		for _, at := range v.Upcoming[1:] {
			times = append(times, at.In(loc).Format(shownTime))
		}
		s += "; then " + strings.Join(times, ", ")
	}
	return s
}

func watcherSays(v WatcherView) string {
	switch v.Source {
	case SourceWebhook:
		return "runs when its URL is called"
	case SourceFeed:
		return fmt.Sprintf("checks the feed %q every %s", v.URL, everySays(v.EverySeconds))
	}
	s := fmt.Sprintf("checks %q every %s", v.URL, everySays(v.EverySeconds))
	if v.Selector != nil {
		s += fmt.Sprintf(", where %q matches", *v.Selector)
	}
	return s
}

func everySays(sec *int32) string {
	if sec == nil {
		return "so often"
	}
	return lengthSays(time.Duration(*sec) * time.Second)
}

// lengthSays is a length as a person says it: 1h, 90m, 2d.
func lengthSays(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return d.String()
}

// whereSays is where one answers.
func whereSays(target, label string) string {
	if target == TargetOwn || target == "" {
		return "in a conversation of its own on the dashboard"
	}
	return "in " + strconv.Quote(label)
}

func scheduleLine(v ScheduleView) string {
	line := fmt.Sprintf("%q (%s), id %s: %s", v.Name, v.Status, v.ID, scheduleSays(v))
	if v.LastOutcome != "" {
		line += "; last " + v.LastOutcome
	}
	return line + "; answers " + whereSays(v.Target, v.TargetLabel) + waitsSays(v.WaitingChange()) + codeSays(v.Status, v.YesCode)
}

func watcherLine(v WatcherView) string {
	line := fmt.Sprintf("%q (%s), id %s: it %s", v.Name, v.Status, v.ID, watcherSays(v))
	if v.LastCheckedAt != nil {
		line += "; last looked " + v.LastCheckedAt.UTC().Format(shownTime+" UTC")
	}
	if v.LastOutcome != "" {
		line += "; last " + v.LastOutcome
	}
	return line + "; answers " + whereSays(v.Target, v.TargetLabel) + waitsSays(v.WaitingChange()) + codeSays(v.Status, v.YesCode)
}

// codeSays is the code a yes to what waits on one carries.
func codeSays(status string, code *string) string {
	if code == nil || (status != Proposed && status != Active && status != Paused) {
		return ""
	}
	return fmt.Sprintf("; the person confirms with the code %s", *code)
}

// waitsSays is what an agent asked of one, waiting for the person's yes.
func waitsSays(what string, _ *time.Time) string {
	if what == "" {
		return ""
	}
	return "; waiting for the person's yes " + what
}

func listSays(lines []string) string {
	return "- " + strings.Join(lines, "\n- ") + "\n"
}

// patchSays names what a change to a schedule sets: "its objective, when it
// runs".
func patchSays(p SchedulePatch) string {
	var parts []string
	if p.Name != nil {
		parts = append(parts, fmt.Sprintf("its name to %q", *p.Name))
	}
	if p.Objective != nil {
		parts = append(parts, "its objective")
	}
	if p.When != nil {
		parts = append(parts, "when it runs")
	}
	if p.Timezone != nil {
		parts = append(parts, "its zone to "+strconv.Quote(*p.Timezone))
	}
	if p.AgentID != nil || p.Hub {
		parts = append(parts, "the agent that runs it")
	}
	return strings.Join(parts, ", ")
}

func watcherPatchSays(p WatcherPatch) string {
	var parts []string
	if p.Name != nil {
		parts = append(parts, fmt.Sprintf("its name to %q", *p.Name))
	}
	if p.Objective != nil {
		parts = append(parts, "its objective")
	}
	if p.Every != nil {
		parts = append(parts, "how often it looks, to every "+lengthSays(*p.Every))
	}
	if p.Cooldown != nil {
		parts = append(parts, "its cooldown, to "+lengthSays(*p.Cooldown))
	}
	if p.Selector != nil {
		parts = append(parts, "what part of the page it watches")
	}
	if p.AgentID != nil || p.Hub {
		parts = append(parts, "the agent that runs it")
	}
	return strings.Join(parts, ", ")
}
