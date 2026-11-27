package schedules

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers"
)

var watcherStatuses = []string{"proposed", "waiting_secret", "active", "paused", "deleted"}

// registerWatchers adds the watcher routes: the schedule routes' twins, with
// checks in place of fires and check-now in place of run-now.
func (h *handler) registerWatchers(r *api.Router) {
	r.Get("/{id}/watchers", h.listWatchers)
	r.Post("/{id}/watchers", h.createWatcher)
	r.Get("/{id}/watchers/{watcherID}", h.getWatcher)
	r.Patch("/{id}/watchers/{watcherID}", h.updateWatcher)
	r.Delete("/{id}/watchers/{watcherID}", h.deleteWatcher)
	r.Post("/{id}/watchers/{watcherID}/confirm", h.confirmWatcher)
	r.Post("/{id}/watchers/{watcherID}/run", h.checkWatcher)
	r.Post("/{id}/watchers/{watcherID}/take-over", h.takeOverWatcher)
	r.Get("/{id}/watchers/{watcherID}/checks", h.checks)
	r.Post("/{id}/watchers/{watcherID}/secret", h.rollSecret)
}

// hookSecret is a webhook watcher's new secret and the URL it signs for,
// shown this once.
type hookSecret struct {
	Secret string  `json:"secret"`
	URL    *string `json:"url,omitempty"`
}

func (h *handler) rollSecret(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := watcherRef(w, r)
	if !ok {
		return
	}
	v, err := h.service.RollSecret(r.Context(), who, ref)
	if err != nil {
		h.answer(w, r, "failed to make the watcher's secret", err)
		return
	}
	h.writeSecretive(w, http.StatusCreated, v.Secret, hookSecret{Secret: v.Secret, URL: h.watcherView(v).HookURL})
}

func watcherRef(w http.ResponseWriter, r *http.Request) (triggers.Ref, bool) {
	id, err := uuid.Parse(r.PathValue("watcherID"))
	if err != nil {
		http.Error(w, "watcher id must be a uuid", http.StatusBadRequest)
		return triggers.Ref{}, false
	}
	return triggers.Ref{ID: id}, true
}

func (h *handler) listWatchers(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}
	wanted, ok := api.OneOf(w, r, "status", watcherStatuses)
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
	rows, err := h.service.ListWatchers(r.Context(), who, everyone, wanted, after, afterID, limit+1)
	if err != nil {
		h.answer(w, r, "failed to list the watchers", err)
		return
	}
	page, err := api.MapPage(rows, limit,
		func(v triggers.WatcherView) any { return listCursor{CreatedAt: v.CreatedAt, ID: v.ID} }, h.watcherView)
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to page the watchers", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

// maxSeconds is past any interval or cooldown a watcher takes, and far below
// where seconds would overflow as a duration.
const maxSeconds = 1 << 31

// seconds refuses a count of seconds no watcher takes, before it becomes a
// duration.
func seconds(w http.ResponseWriter, name string, n *int64) bool {
	if n != nil && (*n < 0 || *n > maxSeconds) {
		http.Error(w, name+" is out of range", http.StatusBadRequest)
		return false
	}
	return true
}

// allOf reads ?all, which asks for everyone's.
func allOf(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch r.URL.Query().Get("all") {
	case "", "false":
		return false, true
	case "true":
		return true, true
	}
	http.Error(w, "all must be true or false", http.StatusBadRequest)
	return false, false
}

func (h *handler) createWatcher(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	var req createWatcherRequest
	if !api.DecodeJSONWithin(w, r, &req, maxBody) {
		return
	}
	if !seconds(w, "every_seconds", &req.EverySeconds) || !seconds(w, "cooldown_seconds", req.CooldownSeconds) {
		return
	}
	in := triggers.WatcherInput{
		Name: req.Name, Objective: req.Objective, Source: req.Source, URL: req.URL, Selector: req.Selector,
		Every: time.Duration(req.EverySeconds) * time.Second, AgentID: req.AgentID,
	}
	if req.CooldownSeconds != nil {
		in.Cooldown = ptr(time.Duration(*req.CooldownSeconds) * time.Second)
	}
	if req.Target != nil {
		in.Target = req.Target.target()
	}
	v, err := h.service.CreateWatcher(r.Context(), who, triggers.Origin{}, in)
	if err != nil {
		h.answer(w, r, "failed to make the watcher", err)
		return
	}
	h.writeSecretive(w, http.StatusCreated, v.Secret, h.watcherView(v))
}

func (h *handler) getWatcher(w http.ResponseWriter, r *http.Request) {
	h.oneWatcher(w, r, "failed to read the watcher", func(who triggers.Who, ref triggers.Ref) (triggers.WatcherView, error) {
		return h.service.Watcher(r.Context(), who, ref)
	})
}

func (h *handler) updateWatcher(w http.ResponseWriter, r *http.Request) {
	var req updateWatcherRequest
	h.oneWatcher(w, r, "failed to change the watcher", func(who triggers.Who, ref triggers.Ref) (triggers.WatcherView, error) {
		if !api.DecodeJSONWithin(w, r, &req, maxBody) {
			return triggers.WatcherView{}, errAnswered
		}
		if req.Hub && req.AgentID != nil {
			http.Error(w, "give agent_id or hub, not both", http.StatusBadRequest)
			return triggers.WatcherView{}, errAnswered
		}
		if req.Status != nil && *req.Status != triggers.Active && *req.Status != triggers.Paused {
			http.Error(w, "status must be active or paused", http.StatusBadRequest)
			return triggers.WatcherView{}, errAnswered
		}
		if !seconds(w, "every_seconds", req.EverySeconds) || !seconds(w, "cooldown_seconds", req.CooldownSeconds) {
			return triggers.WatcherView{}, errAnswered
		}
		patch := triggers.WatcherPatch{
			Name: req.Name, Objective: req.Objective, Selector: req.Selector, AgentID: req.AgentID, Hub: req.Hub,
			Status: req.Status,
		}
		if req.EverySeconds != nil {
			patch.Every = ptr(time.Duration(*req.EverySeconds) * time.Second)
		}
		if req.CooldownSeconds != nil {
			patch.Cooldown = ptr(time.Duration(*req.CooldownSeconds) * time.Second)
		}
		return h.service.UpdateWatcher(r.Context(), who, ref, patch)
	})
}

func (h *handler) confirmWatcher(w http.ResponseWriter, r *http.Request) {
	h.oneWatcher(w, r, "failed to confirm the watcher", func(who triggers.Who, ref triggers.Ref) (triggers.WatcherView, error) {
		return h.service.ConfirmWatcher(r.Context(), who, triggers.Origin{}, ref)
	})
}

func (h *handler) checkWatcher(w http.ResponseWriter, r *http.Request) {
	h.oneWatcher(w, r, "failed to check the watcher", func(who triggers.Who, ref triggers.Ref) (triggers.WatcherView, error) {
		return h.service.CheckNow(r.Context(), who, ref)
	})
}

func (h *handler) takeOverWatcher(w http.ResponseWriter, r *http.Request) {
	h.oneWatcher(w, r, "failed to take over the watcher", func(who triggers.Who, ref triggers.Ref) (triggers.WatcherView, error) {
		return h.service.TakeOverWatcher(r.Context(), who, ref)
	})
}

// oneWatcher is a route about one watcher that answers with it.
func (h *handler) oneWatcher(w http.ResponseWriter, r *http.Request, msg string,
	fn func(triggers.Who, triggers.Ref) (triggers.WatcherView, error),
) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := watcherRef(w, r)
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
	h.writeSecretive(w, http.StatusOK, v.Secret, h.watcherView(v))
}

func (h *handler) deleteWatcher(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := watcherRef(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteWatcher(r.Context(), who, ref); err != nil {
		h.answer(w, r, "failed to delete the watcher", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) checks(w http.ResponseWriter, r *http.Request) {
	who, ok := h.begin(w, r)
	if !ok {
		return
	}
	ref, ok := watcherRef(w, r)
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
	rows, err := h.service.Checks(r.Context(), who, ref, before, limit+1)
	if err != nil {
		h.answer(w, r, "failed to list the watcher's checks", err)
		return
	}
	page, err := api.MapPage(rows, limit,
		func(c db.WatcherCheck) any { return fireCursor{Before: c.CheckedAt} },
		func(c db.WatcherCheck) check {
			return check{CheckedAt: c.CheckedAt, Outcome: c.Outcome, Detail: c.Detail, RunID: c.RunID}
		})
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to page the checks", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

type watcher struct {
	ID              uuid.UUID      `json:"id"`
	Name            string         `json:"name"`
	Objective       string         `json:"objective"`
	Source          string         `json:"source"`
	URL             *string        `json:"url,omitempty"`
	Selector        *string        `json:"selector,omitempty"`
	EverySeconds    *int32         `json:"every_seconds,omitempty"`
	CooldownSeconds int32          `json:"cooldown_seconds"`
	AgentID         *uuid.UUID     `json:"agent_id,omitempty"`
	Hub             bool           `json:"hub"`
	Target          target         `json:"target"`
	SessionID       uuid.UUID      `json:"session_id"`
	Status          string         `json:"status"`
	PausedReason    *string        `json:"paused_reason,omitempty"`
	NextCheckAt     *time.Time     `json:"next_check_at,omitempty"`
	LastCheckAt     *time.Time     `json:"last_check_at,omitempty"`
	LastRunAt       *time.Time     `json:"last_run_at,omitempty"`
	LastOutcome     *string        `json:"last_outcome,omitempty"`
	HookURL         *string        `json:"hook_url,omitempty"`
	Secret          string         `json:"secret,omitempty"`
	CreatedBy       *uuid.UUID     `json:"created_by,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	WaitingChange   *waitingChange `json:"waiting_change,omitempty"`
}

func (h *handler) watcherView(v triggers.WatcherView) watcher {
	out := watcher{
		ID: v.ID, Name: v.Name, Objective: v.Objective, Source: v.Source, Selector: v.Selector,
		EverySeconds: v.EverySeconds, CooldownSeconds: v.CooldownSeconds, AgentID: v.AgentID, Hub: v.Hub,
		Target: target{Kind: v.Target, Label: v.TargetLabel}, SessionID: v.SessionID, Status: v.Status,
		PausedReason: v.PausedReason, NextCheckAt: v.NextCheckAt, LastCheckAt: v.LastCheckedAt,
		LastRunAt: v.LastRunAt, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
	if v.URL != "" {
		out.URL = &v.URL
	}
	if v.Target != triggers.TargetOwn {
		out.Target.ChannelID = v.SessionChannelID
	}
	if v.LastOutcome != "" {
		out.LastOutcome = &v.LastOutcome
	}
	if v.HookID != nil && h.hookBase != "" {
		out.HookURL = ptr(h.hookBase + "/hooks/watchers/" + *v.HookID)
	}
	out.Secret = v.Secret
	out.WaitingChange = waitingOf(v.WaitingChange())
	return out
}

type createWatcherRequest struct {
	Name            string     `json:"name"`
	Objective       string     `json:"objective"`
	Source          string     `json:"source"`
	URL             string     `json:"url"`
	Selector        string     `json:"selector"`
	EverySeconds    int64      `json:"every_seconds"`
	CooldownSeconds *int64     `json:"cooldown_seconds"`
	AgentID         *uuid.UUID `json:"agent_id"`
	Target          *pick      `json:"target"`
}

// updateWatcherRequest changes what it names. An empty selector clears it;
// hub sets the agent back to the hub; status pauses or resumes.
type updateWatcherRequest struct {
	Name            *string    `json:"name"`
	Objective       *string    `json:"objective"`
	Selector        *string    `json:"selector"`
	EverySeconds    *int64     `json:"every_seconds"`
	CooldownSeconds *int64     `json:"cooldown_seconds"`
	AgentID         *uuid.UUID `json:"agent_id"`
	Hub             bool       `json:"hub"`
	Status          *string    `json:"status"`
}

type check struct {
	CheckedAt time.Time  `json:"checked_at"`
	Outcome   string     `json:"outcome"`
	Detail    *string    `json:"detail,omitempty"`
	RunID     *uuid.UUID `json:"run_id,omitempty"`
}

func ptr[T any](v T) *T { return &v }
