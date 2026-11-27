package schedules

import (
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers"
)

// whenBody is a time rule on the wire: one of at, every_seconds or cron, by
// kind.
type whenBody struct {
	Kind         string     `json:"kind"`
	At           *time.Time `json:"at,omitempty"`
	EverySeconds *int64     `json:"every_seconds,omitempty"`
	Cron         *string    `json:"cron,omitempty"`
}

// target is where a schedule answers, as it is shown: own, chat, channel or
// direct, with how the place was shown when it was picked.
type target struct {
	Kind      string     `json:"kind"`
	Label     string     `json:"label,omitempty"`
	ChannelID *uuid.UUID `json:"channel_id,omitempty"`
}

// pick is a place as the picker hands it back on create: what a listed place
// carried, sent back as it was. own needs nothing.
type pick struct {
	Kind      string     `json:"kind"`
	SessionID *uuid.UUID `json:"session_id,omitempty"`
	ChannelID *uuid.UUID `json:"channel_id,omitempty"`
	Ref       string     `json:"ref,omitempty"`
}

func (p pick) target() triggers.Target {
	t := triggers.Target{Kind: p.Kind, Ref: p.Ref}
	if p.SessionID != nil {
		t.SessionID = *p.SessionID
	}
	if p.ChannelID != nil {
		t.ChannelID = *p.ChannelID
	}
	return t
}

func pickOf(t triggers.Target) pick {
	p := pick{Kind: t.Kind, Ref: t.Ref}
	if t.SessionID != uuid.Nil {
		p.SessionID = &t.SessionID
	}
	if t.ChannelID != uuid.Nil {
		p.ChannelID = &t.ChannelID
	}
	return p
}

// place is one place the caller may pick, or see and not pick.
type place struct {
	Kind      string     `json:"kind"`
	Label     string     `json:"label"`
	Detail    string     `json:"detail"`
	Channel   placeOn    `json:"channel"`
	At        *time.Time `json:"at,omitempty"`
	Available bool       `json:"available"`
	Reason    *string    `json:"reason,omitempty"`
	Target    pick       `json:"target"`
}

type placeOn struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Provider string    `json:"provider"`
}

type placeProblem struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Provider  string    `json:"provider"`
	Message   string    `json:"message"`
}

type placePage struct {
	Items      []place        `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Problems   []placeProblem `json:"problems"`
}

func placeOf(it triggers.TargetItem) place {
	p := place{
		Kind: it.Kind, Label: it.Label, Detail: it.Detail, At: it.At, Available: it.Available,
		Channel: placeOn{ID: it.ChannelID, Name: it.ChannelName, Provider: it.Provider}, Target: pickOf(it.Target),
	}
	if it.Reason != "" {
		p.Reason = &it.Reason
	}
	return p
}

type schedule struct {
	ID           uuid.UUID   `json:"id"`
	Name         string      `json:"name"`
	Objective    string      `json:"objective"`
	When         whenBody    `json:"when"`
	Timezone     string      `json:"timezone"`
	AgentID      *uuid.UUID  `json:"agent_id,omitempty"`
	Hub          bool        `json:"hub"`
	Target       target      `json:"target"`
	SessionID    uuid.UUID   `json:"session_id"`
	Status       string      `json:"status"`
	PausedReason *string     `json:"paused_reason,omitempty"`
	NextFireAt   *time.Time  `json:"next_fire_at,omitempty"`
	Upcoming     []time.Time `json:"upcoming"`
	LastFiredAt  *time.Time  `json:"last_fired_at,omitempty"`
	LastOutcome  *string     `json:"last_outcome,omitempty"`
	CreatedBy    *uuid.UUID  `json:"created_by,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

func view(v triggers.ScheduleView) schedule {
	out := schedule{
		ID: v.ID, Name: v.Name, Objective: v.Objective, When: whenOf(v.Schedule), Timezone: v.Timezone,
		AgentID: v.AgentID, Hub: v.Hub, Target: targetOf(v), SessionID: v.SessionID, Status: v.Status,
		PausedReason: v.PausedReason, NextFireAt: v.NextFireAt, Upcoming: v.Upcoming,
		LastFiredAt: v.LastFiredAt, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
	if out.Upcoming == nil {
		out.Upcoming = []time.Time{}
	}
	if v.LastOutcome != "" {
		out.LastOutcome = &v.LastOutcome
	}
	return out
}

func whenOf(s db.Schedule) whenBody {
	w := whenBody{Kind: s.Kind}
	switch s.Kind {
	case "at":
		if at, err := time.Parse(time.RFC3339, s.Rule); err == nil {
			w.At = &at
		}
	case "every":
		if secs, err := strconv.ParseInt(s.Rule, 10, 64); err == nil {
			w.EverySeconds = &secs
		}
	case "cron":
		w.Cron = &s.Rule
	}
	return w
}

type createRequest struct {
	Name      string     `json:"name"`
	Objective string     `json:"objective"`
	When      whenBody   `json:"when"`
	Timezone  string     `json:"timezone"`
	AgentID   *uuid.UUID `json:"agent_id"`
	Target    *pick      `json:"target"`
}

// updateRequest changes what it names. hub sets the agent back to the hub;
// status pauses or resumes.
type updateRequest struct {
	Name      *string    `json:"name"`
	Objective *string    `json:"objective"`
	When      *whenBody  `json:"when"`
	Timezone  *string    `json:"timezone"`
	AgentID   *uuid.UUID `json:"agent_id"`
	Hub       bool       `json:"hub"`
	Status    *string    `json:"status"`
}

type previewRequest struct {
	When     whenBody `json:"when"`
	Timezone string   `json:"timezone"`
}

type preview struct {
	Timezone string      `json:"timezone"`
	Upcoming []time.Time `json:"upcoming"`
}

type fire struct {
	ScheduledFor time.Time  `json:"scheduled_for"`
	FiredAt      time.Time  `json:"fired_at"`
	Outcome      string     `json:"outcome"`
	RunID        *uuid.UUID `json:"run_id,omitempty"`
}

type ranNow struct {
	RunID uuid.UUID `json:"run_id"`
}

// newsItem is a schedule's fire or a watcher's check: which one says, and
// fired_at is when it happened.
type newsItem struct {
	ScheduleID   *uuid.UUID `json:"schedule_id,omitempty"`
	WatcherID    *uuid.UUID `json:"watcher_id,omitempty"`
	Name         string     `json:"name"`
	SessionID    uuid.UUID  `json:"session_id"`
	ScheduledFor *time.Time `json:"scheduled_for,omitempty"`
	FiredAt      time.Time  `json:"fired_at"`
	Outcome      string     `json:"outcome"`
	RunID        *uuid.UUID `json:"run_id,omitempty"`
}

type news struct {
	Items []newsItem `json:"items"`
	Count int        `json:"count"`
	// More is news beyond what one page shows.
	More bool `json:"more"`
}

type seenRequest struct {
	ScheduleID *uuid.UUID `json:"schedule_id"`
	WatcherID  *uuid.UUID `json:"watcher_id"`
}

type settings struct {
	AgentsMaySchedule          bool   `json:"agents_may_schedule"`
	Timezone                   string `json:"timezone"`
	ScheduleMinIntervalMinutes int32  `json:"schedule_min_interval_minutes"`
	SchedulesPerMember         int32  `json:"schedules_per_member"`
	SchedulesPerTeam           int32  `json:"schedules_per_team"`
	FiresPerHour               int32  `json:"fires_per_hour"`
}

type listCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

type fireCursor struct {
	Before time.Time `json:"b"`
}

func targetOf(v triggers.ScheduleView) target {
	t := target{Kind: v.Target, Label: v.TargetLabel}
	if v.Target != triggers.TargetOwn {
		t.ChannelID = v.SessionChannelID
	}
	return t
}
