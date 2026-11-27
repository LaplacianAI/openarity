// Package schedules is how people make, see and change the schedules that
// start runs on the clock, hear what they did, and how an admin sets a
// team's limits for them. The rules are internal/triggers'; this is their
// HTTP face.
package schedules

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/when"
)

const (
	manageEveryones = authz.Action("schedule:manage")
	// writeChannels is what posting at a channel's top level takes.
	writeChannels = authz.Action("channel:write")
	maxBody       = 64 << 10
	newsPage      = 50
)

var statuses = []string{"proposed", "active", "paused", "done", "deleted"}

// Store is what the settings routes read and write; the rest goes through
// the triggers service.
type Store interface {
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	SetTeamSchedules(ctx context.Context, arg db.SetTeamSchedulesParams) (db.TeamSetting, error)
}

type handler struct {
	logger   *slog.Logger
	store    Store
	authz    api.Authorizer
	service  *triggers.Service
	hookBase string
}

// New serves schedules and watchers. hookBase is the webhook listener's
// public address a webhook watcher's URL is on; with none, its URL is not
// shown.
func New(logger *slog.Logger, s Store, a api.Authorizer, service *triggers.Service, hookBase string) *api.Router {
	h := &handler{logger: logger, store: s, authz: a, service: service, hookBase: strings.TrimSuffix(hookBase, "/")}

	r := api.NewRouter("/teams")
	r.Get("/{id}/schedules", h.list)
	r.Post("/{id}/schedules", h.create)
	r.Post("/{id}/schedules/preview", h.preview)
	r.Get("/{id}/schedules/{scheduleID}", h.get)
	r.Patch("/{id}/schedules/{scheduleID}", h.update)
	r.Delete("/{id}/schedules/{scheduleID}", h.delete)
	r.Post("/{id}/schedules/{scheduleID}/confirm", h.confirm)
	r.Post("/{id}/schedules/{scheduleID}/run", h.run)
	r.Post("/{id}/schedules/{scheduleID}/take-over", h.takeOver)
	r.Get("/{id}/schedules/{scheduleID}/fires", h.fires)
	r.Get("/{id}/scheduled/news", h.news)
	r.Post("/{id}/scheduled/news/seen", h.seen)
	r.Get("/{id}/schedule-targets", h.targets)
	r.Get("/{id}/schedule-settings", h.settings)
	r.Put("/{id}/schedule-settings", h.setSettings)
	h.registerWatchers(r)
	return r
}

func (h *handler) who(r *http.Request) (triggers.Who, bool) {
	u := api.Caller(r)
	team, ok := api.TeamFrom(r.Context())
	if !ok {
		return triggers.Who{}, false
	}
	return triggers.Who{
		Team: team, User: u.ID,
		Manage: h.can(r, u, team, manageEveryones), ChannelWrite: h.can(r, u, team, writeChannels),
	}, true
}

func (h *handler) can(r *http.Request, u *auth.User, team uuid.UUID, action authz.Action) bool {
	ok, err := h.authz.Can(r.Context(), u, action, authz.Resource{TeamID: team})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "failed to check "+string(action), "error", err, "team_id", team)
		return false
	}
	return ok
}

// begin is the caller and the team, or the response already written.
func (h *handler) begin(w http.ResponseWriter, r *http.Request) (triggers.Who, bool) {
	if _, ok := api.RequireTeam(w, r, h.logger); !ok {
		return triggers.Who{}, false
	}
	return h.who(r)
}

func scheduleRef(w http.ResponseWriter, r *http.Request) (triggers.Ref, bool) {
	id, err := uuid.Parse(r.PathValue("scheduleID"))
	if err != nil {
		http.Error(w, "schedule id must be a uuid", http.StatusBadRequest)
		return triggers.Ref{}, false
	}
	return triggers.Ref{ID: id}, true
}

// answer writes a refusal as the status it is, or a failure as a 500.
func (h *handler) answer(w http.ResponseWriter, r *http.Request, msg string, err error) {
	status, ok := statusOf(triggers.KindOf(err))
	if !ok {
		api.Fail(w, h.logger, api.Caller(r), msg, err)
		return
	}
	refused(w, status)
	http.Error(w, err.Error(), status)
}

// statusOf is how a refusal is answered, or false for a failure.
func statusOf(k triggers.Kind) (int, bool) {
	switch k {
	case triggers.Invalid:
		return http.StatusBadRequest, true
	case triggers.NotFound:
		return http.StatusNotFound, true
	case triggers.Forbidden:
		return http.StatusForbidden, true
	case triggers.Conflict, triggers.Limit:
		return http.StatusConflict, true
	case triggers.Unavailable:
		return http.StatusServiceUnavailable, true
	case triggers.Unauthorized:
		return http.StatusUnauthorized, true
	}
	return 0, false
}

// refused sets what a refusal's status asks for: when to try again, for a
// secret store that did not answer.
func refused(w http.ResponseWriter, status int) {
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "30")
	}
}

// writeSecretive answers with what carries a secret, kept out of every cache
// on the way.
func (h *handler) writeSecretive(w http.ResponseWriter, status int, secret string, body any) {
	if secret != "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	api.WriteJSON(w, h.logger, status, body)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}
	wanted, ok := api.OneOf(w, r, "status", statuses)
	if !ok {
		return
	}
	everyone, ok := allOf(w, r)
	if !ok {
		return
	}
	var after *time.Time
	var afterID *uuid.UUID
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c listCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		after, afterID = &c.CreatedAt, &c.ID
	}
	rows, err := h.service.ListSchedules(r.Context(), who, everyone, wanted, after, afterID, limit+1)
	if err != nil {
		h.answer(w, r, "failed to list the schedules", err)
		return
	}
	page, err := api.MapPage(rows, limit,
		func(v triggers.ScheduleView) any { return listCursor{CreatedAt: v.CreatedAt, ID: v.ID} }, view)
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to page the schedules", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	var req createRequest
	if !api.DecodeJSONWithin(w, r, &req, maxBody) {
		return
	}
	rule, ok := whenFrom(w, req.When)
	if !ok {
		return
	}
	in := triggers.ScheduleInput{
		Name: req.Name, Objective: req.Objective, When: rule, Timezone: req.Timezone, AgentID: req.AgentID,
	}
	if req.Target != nil {
		in.Target = req.Target.target()
	}
	v, err := h.service.CreateSchedule(r.Context(), who, triggers.Origin{}, in)
	if err != nil {
		h.answer(w, r, "failed to make the schedule", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, view(v))
}

func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	var req previewRequest
	if !api.DecodeJSONWithin(w, r, &req, maxBody) {
		return
	}
	rule, ok := whenFrom(w, req.When)
	if !ok {
		return
	}
	times, zone, err := h.service.Preview(r.Context(), who, rule, req.Timezone)
	if err != nil {
		h.answer(w, r, "failed to preview the schedule", err)
		return
	}
	if times == nil {
		times = []time.Time{}
	}
	api.WriteJSON(w, h.logger, http.StatusOK, preview{Timezone: zone, Upcoming: times})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	h.one(w, r, "failed to read the schedule", func(who triggers.Who, ref triggers.Ref) (triggers.ScheduleView, error) {
		return h.service.Schedule(r.Context(), who, ref)
	})
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	h.one(w, r, "failed to change the schedule", func(who triggers.Who, ref triggers.Ref) (triggers.ScheduleView, error) {
		if !api.DecodeJSONWithin(w, r, &req, maxBody) {
			return triggers.ScheduleView{}, errAnswered
		}
		patch := triggers.SchedulePatch{
			Name: req.Name, Objective: req.Objective, Timezone: req.Timezone, AgentID: req.AgentID, Hub: req.Hub,
			Status: req.Status,
		}
		if req.Hub && req.AgentID != nil {
			http.Error(w, "give agent_id or hub, not both", http.StatusBadRequest)
			return triggers.ScheduleView{}, errAnswered
		}
		if req.When != nil {
			rule, ok := whenFrom(w, *req.When)
			if !ok {
				return triggers.ScheduleView{}, errAnswered
			}
			patch.When = &rule
		}
		if req.Status != nil && *req.Status != triggers.Active && *req.Status != triggers.Paused {
			http.Error(w, "status must be active or paused", http.StatusBadRequest)
			return triggers.ScheduleView{}, errAnswered
		}
		return h.service.UpdateSchedule(r.Context(), who, ref, patch)
	})
}

func (h *handler) confirm(w http.ResponseWriter, r *http.Request) {
	h.one(w, r, "failed to confirm the schedule", func(who triggers.Who, ref triggers.Ref) (triggers.ScheduleView, error) {
		return h.service.Confirm(r.Context(), who, triggers.Origin{}, ref)
	})
}

func (h *handler) takeOver(w http.ResponseWriter, r *http.Request) {
	h.one(w, r, "failed to take over the schedule", func(who triggers.Who, ref triggers.Ref) (triggers.ScheduleView, error) {
		return h.service.TakeOver(r.Context(), who, ref)
	})
}

// errAnswered is a request already refused by the time the service would
// have been asked.
var errAnswered = errors.New("answered")

// one is a route about one schedule that answers with it.
func (h *handler) one(w http.ResponseWriter, r *http.Request, msg string,
	fn func(triggers.Who, triggers.Ref) (triggers.ScheduleView, error),
) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := scheduleRef(w, r)
	if !ok {
		return
	}
	v, err := fn(who, ref)
	if errors.Is(err, errAnswered) {
		return
	}
	if err != nil {
		h.answer(w, r, msg, err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, view(v))
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := scheduleRef(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), who, ref); err != nil {
		h.answer(w, r, "failed to delete the schedule", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) run(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := scheduleRef(w, r)
	if !ok {
		return
	}
	run, err := h.service.RunNow(r.Context(), who, ref)
	if err != nil {
		h.answer(w, r, "failed to run the schedule", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusAccepted, ranNow{RunID: run})
}

func (h *handler) fires(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := scheduleRef(w, r)
	if !ok {
		return
	}
	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}
	var before *time.Time
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c fireCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		before = &c.Before
	}
	rows, err := h.service.Fires(r.Context(), who, ref, before, limit+1)
	if err != nil {
		h.answer(w, r, "failed to list the schedule's fires", err)
		return
	}
	page, err := api.MapPage(rows, limit,
		func(f db.ScheduleFire) any { return fireCursor{Before: f.ScheduledFor} },
		func(f db.ScheduleFire) fire {
			return fire{ScheduledFor: f.ScheduledFor, FiredAt: f.FiredAt, Outcome: f.Outcome, RunID: f.RunID}
		})
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to page the fires", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) news(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	fires, err := h.service.News(r.Context(), who, newsPage+1)
	if err != nil {
		h.answer(w, r, "failed to read the schedules' news", err)
		return
	}
	checks, err := h.service.WatcherNews(r.Context(), who, newsPage+1)
	if err != nil {
		h.answer(w, r, "failed to read the watchers' news", err)
		return
	}
	items := make([]newsItem, 0, len(fires)+len(checks))
	for _, n := range fires {
		items = append(items, newsItem{
			ScheduleID: &n.ScheduleID, Name: n.Name, SessionID: n.SessionID, ScheduledFor: &n.ScheduledFor,
			FiredAt: n.FiredAt, Outcome: n.Outcome, RunID: n.RunID,
		})
	}
	for _, n := range checks {
		items = append(items, newsItem{
			WatcherID: &n.WatcherID, Name: n.Name, SessionID: n.SessionID, FiredAt: n.CheckedAt,
			Outcome: n.Outcome, RunID: n.RunID,
		})
	}
	// Both come newest first; together they are, too.
	slices.SortStableFunc(items, func(a, b newsItem) int { return b.FiredAt.Compare(a.FiredAt) })
	more := len(items) > newsPage
	if more {
		items = items[:newsPage]
	}
	api.WriteJSON(w, h.logger, http.StatusOK, news{Items: items, Count: len(items), More: more})
}

func (h *handler) seen(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	var req seenRequest
	if !api.DecodeJSONWithin(w, r, &req, maxBody) {
		return
	}
	switch {
	case (req.ScheduleID == nil) == (req.WatcherID == nil):
		http.Error(w, "give schedule_id or watcher_id", http.StatusBadRequest)
		return
	case req.ScheduleID != nil:
		if err := h.service.Seen(r.Context(), who, *req.ScheduleID); err != nil {
			h.answer(w, r, "failed to mark the schedule seen", err)
			return
		}
	default:
		if err := h.service.WatcherSeen(r.Context(), who, *req.WatcherID); err != nil {
			h.answer(w, r, "failed to mark the watcher seen", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) settings(w http.ResponseWriter, r *http.Request) {
	team, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	row, err := h.store.GetTeamSettings(r.Context(), team)
	l, err := triggers.LimitsOf(row, err)
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to read the schedule settings", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, settingsOf(l))
}

func (h *handler) setSettings(w http.ResponseWriter, r *http.Request) {
	team, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	var req settings
	if !api.DecodeJSONWithin(w, r, &req, maxBody) {
		return
	}
	if msg := checkSettings(req); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	row, err := h.store.SetTeamSchedules(r.Context(), db.SetTeamSchedulesParams{
		TeamID: team, AgentsMaySchedule: req.AgentsMaySchedule, Timezone: req.Timezone,
		ScheduleMinIntervalMinutes: req.ScheduleMinIntervalMinutes, SchedulesPerMember: req.SchedulesPerMember,
		SchedulesPerTeam: req.SchedulesPerTeam, FiresPerHour: req.FiresPerHour,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	l, err := triggers.LimitsOf(row, err)
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to save the schedule settings", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, settingsOf(l))
}

func settingsOf(l triggers.Limits) settings {
	return settings{
		AgentsMaySchedule: l.AgentsMaySchedule, Timezone: l.Timezone,
		ScheduleMinIntervalMinutes: int32(l.MinInterval / time.Minute), //nolint:gosec // bounded by the column's check
		SchedulesPerMember:         int32(l.PerMember),                 //nolint:gosec // bounded by the column's check
		SchedulesPerTeam:           int32(l.PerTeam),                   //nolint:gosec // bounded by the column's check
		FiresPerHour:               int32(l.FiresPerHour),              //nolint:gosec // bounded by the column's check
	}
}

// checkSettings is the column checks, said for a person before they are hit.
func checkSettings(s settings) string {
	switch {
	case s.Timezone == "" || len(s.Timezone) > 64:
		return "timezone must be an IANA name such as Europe/London"
	case s.ScheduleMinIntervalMinutes < 5 || s.ScheduleMinIntervalMinutes > 10080:
		return "schedule_min_interval_minutes must be between 5 and 10080"
	case s.SchedulesPerMember < 1 || s.SchedulesPerMember > 100:
		return "schedules_per_member must be between 1 and 100"
	case s.SchedulesPerTeam < 1 || s.SchedulesPerTeam > 1000:
		return "schedules_per_team must be between 1 and 1000"
	case s.SchedulesPerTeam < s.SchedulesPerMember:
		return "schedules_per_team must be at least schedules_per_member"
	case s.FiresPerHour < 1 || s.FiresPerHour > 1000:
		return "fires_per_hour must be between 1 and 1000"
	}
	if _, err := when.Zone(s.Timezone); err != nil {
		return "timezone must be an IANA name such as Europe/London"
	}
	return ""
}

// whenFrom reads a rule off the wire, or answers why it cannot.
func whenFrom(w http.ResponseWriter, b whenBody) (triggers.When, bool) {
	set := 0
	for _, given := range []bool{b.At != nil, b.EverySeconds != nil, b.Cron != nil} {
		if given {
			set++
		}
	}
	bad := func(msg string) (triggers.When, bool) {
		http.Error(w, msg, http.StatusBadRequest)
		return triggers.When{}, false
	}
	if set != 1 {
		return bad("when takes exactly one of at, every_seconds or cron")
	}
	switch b.Kind {
	case "at":
		if b.At == nil {
			return bad("when.kind at takes when.at")
		}
		return triggers.When{Kind: when.At, At: *b.At}, true
	case "every":
		if b.EverySeconds == nil || *b.EverySeconds <= 0 || *b.EverySeconds > int64(when.MaxEvery/time.Second) {
			return bad("when.kind every takes when.every_seconds, at most 31 days")
		}
		return triggers.When{Kind: when.Every, Every: time.Duration(*b.EverySeconds) * time.Second}, true
	case "cron":
		if b.Cron == nil {
			return bad("when.kind cron takes when.cron")
		}
		return triggers.When{Kind: when.Cron, Cron: *b.Cron}, true
	default:
		return bad("when.kind must be at, every or cron")
	}
}

// targets lists where the caller may have a schedule answer: the picker's
// places, searched by q, of one kind if asked, a page at a time.
func (h *handler) targets(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}
	q, ok := api.Query(w, r)
	if !ok {
		return
	}
	var offset int
	if raw := r.URL.Query().Get("cursor"); raw != "" && !api.DecodeCursor(w, raw, &offset) {
		return
	}
	page, err := h.service.Targets(r.Context(), who, triggers.TargetQuery{
		Q: q, Kind: r.URL.Query().Get("kind"), Offset: max(offset, 0), Limit: int(limit),
	})
	if err != nil {
		h.answer(w, r, "failed to list where a schedule may answer", err)
		return
	}
	out := placePage{Items: make([]place, len(page.Items)), Problems: make([]placeProblem, len(page.Problems))}
	for i, it := range page.Items {
		out.Items[i] = placeOf(it)
	}
	for i, p := range page.Problems {
		out.Problems[i] = placeProblem{ChannelID: p.ChannelID, Provider: p.Provider, Message: p.Text}
	}
	if page.More {
		next, err := api.EncodeCursor(max(offset, 0) + len(page.Items))
		if err != nil {
			api.Fail(w, h.logger, api.Caller(r), "failed to page the places", err)
			return
		}
		out.NextCursor = next
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}
