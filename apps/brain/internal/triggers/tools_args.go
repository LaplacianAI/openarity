package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/when"
)

// toolArgs is every field either tool takes; each action reads its own.
type toolArgs struct {
	Action    string  `json:"action"`
	Ref       string  `json:"ref"`
	Name      *string `json:"name"`
	Objective *string `json:"objective"`
	At        string  `json:"at"`
	Every     string  `json:"every"`
	Cron      string  `json:"cron"`
	Timezone  *string `json:"timezone"`
	Agent     string  `json:"agent"`
	Target    string  `json:"target"`
	Source    string  `json:"source"`
	URL       string  `json:"url"`
	Selector  *string `json:"selector"`
	Cooldown  string  `json:"cooldown"`
}

// parseToolArgs reads a call's arguments, refusing a field neither tool
// takes rather than dropping it.
func parseToolArgs(raw json.RawMessage) (toolArgs, error) {
	var a toolArgs
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return a, refuse(Invalid, "the arguments are not this tool's: %s", err.Error())
	}
	return a, nil
}

// ref is the one an action names.
func (a toolArgs) ref() (Ref, error) {
	r := strings.TrimSpace(a.Ref)
	if r == "" {
		return Ref{}, refuse(Invalid, "say which one, by its name or id, as ref")
	}
	if id, err := uuid.Parse(r); err == nil {
		return Ref{ID: id}, nil
	}
	return Ref{Name: r}, nil
}

// agentOf is the agent a new one runs: the one named, or the one talking.
// The runtime says why a name may not be used; anything else is a failure.
func (t toolRun) agentOf(ctx context.Context, name string) (*uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return t.Self, nil
	}
	id, err := t.Agent(ctx, name)
	if err != nil {
		return nil, err
	}
	if t.Self != nil && id == *t.Self {
		return t.Self, nil
	}
	return &id, nil
}

// agentPatch is the agent a change names, as a patch sets it: the hub when
// the run's own is the hub.
func (t toolRun) agentPatch(ctx context.Context, name string) (agentID *uuid.UUID, hub bool, err error) {
	if name == "" {
		return nil, false, nil
	}
	id, err := t.agentOf(ctx, name)
	if err != nil {
		return nil, false, err
	}
	return id, id == nil, nil
}

// targetOf is where a new one answers.
func (t toolRun) targetOf(name string) (Target, error) {
	switch name {
	case "", "here":
		return t.chat, nil
	case "me":
		if t.dashboard {
			return Target{}, refuse(Invalid, "on the dashboard, here is already the person's alone; leave target out")
		}
		return Target{Kind: TargetDirect, ChannelID: t.channel}, nil
	}
	return Target{}, refuse(Invalid, "target is here or me")
}

// whenOf is the time the arguments ask for, nil when they name none. A local
// time is read in zone, the team's when empty.
func (s *Service) whenOf(ctx context.Context, t toolRun, a toolArgs, zone string) (*When, error) {
	n := 0
	for _, v := range []string{a.At, a.Every, a.Cron} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	switch {
	case n == 0:
		return nil, nil
	case n > 1:
		return nil, refuse(Invalid, "say when with one of at, every or cron, not more")
	case strings.TrimSpace(a.Cron) != "":
		return &When{Kind: when.Cron, Cron: strings.TrimSpace(a.Cron)}, nil
	case strings.TrimSpace(a.Every) != "":
		d, err := durationOf(a.Every)
		if err != nil {
			return nil, err
		}
		return &When{Kind: when.Every, Every: d}, nil
	}
	if zone == "" {
		l, err := s.limits(ctx, t.Team)
		if err != nil {
			return nil, err
		}
		zone = l.Timezone
	}
	at, err := atOf(a.At, zone)
	if err != nil {
		return nil, err
	}
	return &When{Kind: when.At, At: at}, nil
}

// atOf reads RFC 3339, or a wall time in zone.
func atOf(v, zone string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if at, err := time.Parse(time.RFC3339, v); err == nil {
		return at, nil
	}
	loc, err := when.Zone(zone)
	if err != nil {
		return time.Time{}, asInvalid(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", v, loc)
	if err != nil {
		return time.Time{}, refuse(Invalid, "at is RFC 3339 or YYYY-MM-DD HH:MM, not %q", v)
	}
	return at, nil
}

// durationOf reads 30m, 1h, 1d or 1w, or any Go duration.
func durationOf(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(v, suffix); ok {
			if k, err := strconv.Atoi(n); err == nil && k > 0 {
				return time.Duration(k) * unit, nil
			}
		}
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, refuse(Invalid, "a length is such as 30m, 1h, 1d or 1w, not %q", v)
	}
	return d, nil
}

// optDuration is a length given, or nil.
func optDuration(v string) (*time.Duration, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	d, err := durationOf(v)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// schedulePatchOf is the change an update asks of the schedule ref. A wall
// time is read in the schedule's own zone, or the new one it names.
func (s *Service) schedulePatchOf(ctx context.Context, t toolRun, ref Ref, a toolArgs) (SchedulePatch, error) {
	if a.Target != "" {
		return SchedulePatch{}, refuse(Invalid, "where it answers cannot change; delete it and make another")
	}
	p := SchedulePatch{Name: a.Name, Objective: a.Objective, Timezone: a.Timezone}
	zone := deref(a.Timezone)
	if zone == "" && strings.TrimSpace(a.At) != "" {
		v, err := s.Schedule(ctx, t.who, ref)
		if err != nil {
			return p, err
		}
		zone = v.Timezone
	}
	var err error
	if p.When, err = s.whenOf(ctx, t, a, zone); err != nil {
		return p, err
	}
	p.AgentID, p.Hub, err = t.agentPatch(ctx, a.Agent)
	return p, err
}

// watcherPatchOf is the change an update asks of a watcher.
func (t toolRun) watcherPatchOf(ctx context.Context, a toolArgs) (WatcherPatch, error) {
	switch {
	case a.Target != "":
		return WatcherPatch{}, refuse(Invalid, "where it answers cannot change; delete it and make another")
	case a.URL != "" || a.Source != "":
		return WatcherPatch{}, refuse(Invalid, "what it watches cannot change; delete it and make another")
	case a.At != "" || a.Cron != "" || a.Timezone != nil:
		return WatcherPatch{}, refuse(Invalid, "a watcher takes every and cooldown, not at, cron or timezone")
	}
	p := WatcherPatch{Name: a.Name, Objective: a.Objective, Selector: a.Selector}
	var err error
	if p.Every, err = optDuration(a.Every); err != nil {
		return p, err
	}
	if p.Cooldown, err = optDuration(a.Cooldown); err != nil {
		return p, err
	}
	p.AgentID, p.Hub, err = t.agentPatch(ctx, a.Agent)
	return p, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
