package runs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const maxNoteRunes = 1000

// FeedbackStore is what a person's word on a run reads and writes.
type FeedbackStore interface {
	UpsertRunFeedback(ctx context.Context, arg db.UpsertRunFeedbackParams) (db.RunFeedback, error)
	DeleteRunFeedback(ctx context.Context, arg db.DeleteRunFeedbackParams) (int64, error)
	GetRunFeedback(ctx context.Context, arg db.GetRunFeedbackParams) (db.RunFeedback, error)
	CountRunFeedback(ctx context.Context, runID uuid.UUID) (db.CountRunFeedbackRow, error)
	GetRunOutcome(ctx context.Context, runID uuid.UUID) (db.RunOutcome, error)
	ReopenRunDigest(ctx context.Context, runID uuid.UUID) error
	ListMyFeedbackInSession(ctx context.Context, arg db.ListMyFeedbackInSessionParams) ([]db.ListMyFeedbackInSessionRow, error)
}

type feedbackRequest struct {
	Value string `json:"value"`
	Note  string `json:"note"`
	// Where it was given: dashboard (the default) or cli.
	Via string `json:"via"`
}

type feedbackView struct {
	Value     string    `json:"value"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
}

type sessionFeedback struct {
	RunID uuid.UUID `json:"run_id"`
	feedbackView
}

// putFeedback records the caller's word on an answer, or replaces it. Only
// on a top-level run, the one that answered, and only while the team has
// improvement on: a team that has not opted in records nothing.
func (h *handler) putFeedback(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	row, ok := h.feedbackTarget(w, r)
	if !ok {
		return
	}
	var req feedbackRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Via == "" {
		req.Via = "dashboard"
	}
	switch {
	case req.Value != "up" && req.Value != "down":
		http.Error(w, "value must be up or down", http.StatusBadRequest)
		return
	case utf8.RuneCountInString(req.Note) > maxNoteRunes:
		http.Error(w, "note must be at most 1000 characters", http.StatusBadRequest)
		return
	case req.Via != "dashboard" && req.Via != "cli":
		http.Error(w, "via must be dashboard or cli", http.StatusBadRequest)
		return
	}

	saved, err := h.store.UpsertRunFeedback(r.Context(), db.UpsertRunFeedbackParams{
		RunID: row.ID, TeamID: row.TeamID, UserID: u.ID, Value: req.Value, Note: req.Note, Via: req.Via,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to save the feedback", err)
		return
	}
	// A 👎 on a run judged good and left out of the sample: it is digested
	// after all.
	if req.Value == "down" {
		if err := h.store.ReopenRunDigest(r.Context(), row.ID); err != nil {
			api.Fail(w, h.logger, u, "failed to queue the run's digest", err)
			return
		}
	}
	h.logger.InfoContext(r.Context(), "Feedback given", "team_id", row.TeamID, "run_id", row.ID,
		"value", req.Value, "via", req.Via, "user_id", u.ID)
	api.WriteJSON(w, h.logger, http.StatusOK, feedbackView{Value: saved.Value, Note: saved.Note, UpdatedAt: saved.UpdatedAt})
}

func (h *handler) deleteFeedback(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	row, ok := h.feedbackTarget(w, r)
	if !ok {
		return
	}
	if _, err := h.store.DeleteRunFeedback(r.Context(), db.DeleteRunFeedbackParams{RunID: row.ID, UserID: u.ID}); err != nil {
		api.Fail(w, h.logger, u, "failed to remove the feedback", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// feedbackTarget is the run feedback is given on: one the caller may read,
// top-level, in a team with improvement on.
func (h *handler) feedbackTarget(w http.ResponseWriter, r *http.Request) (db.Run, bool) {
	row, _, ok := h.visible(w, r)
	if !ok {
		return db.Run{}, false
	}
	if row.ParentRunID != nil {
		http.Error(w, "feedback is given on the run that answered, not on work it handed to another agent", http.StatusConflict)
		return db.Run{}, false
	}
	on, err := h.improving(r.Context(), row.TeamID)
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to read the team's settings", err)
		return db.Run{}, false
	}
	if !on {
		http.Error(w, "self-improvement is off for this team, so feedback is not recorded", http.StatusConflict)
		return db.Run{}, false
	}
	return row, true
}

func (h *handler) improving(ctx context.Context, teamID uuid.UUID) (bool, error) {
	s, err := h.store.GetTeamSettings(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return s.Improvement, err
}

// listSessionFeedback is the caller's own feedback on every run of a
// session they may read: what the conversation view shows as pressed.
func (h *handler) listSessionFeedback(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	sessionID, err := uuid.Parse(r.PathValue("sessionID"))
	if err != nil {
		http.Error(w, "sessionID must be a uuid", http.StatusBadRequest)
		return
	}
	_, err = h.store.GetSessionVisible(r.Context(), db.GetSessionVisibleParams{
		ID: sessionID, TeamID: teamID, Moderator: h.moderates(r, u, teamID), Viewer: u.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the session", err)
		return
	}
	rows, err := h.store.ListMyFeedbackInSession(r.Context(), db.ListMyFeedbackInSessionParams{SessionID: sessionID, UserID: u.ID})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the feedback", err)
		return
	}
	items := make([]sessionFeedback, 0, len(rows))
	for _, f := range rows {
		items = append(items, sessionFeedback{RunID: f.RunID, feedbackView: feedbackView{Value: f.Value, Note: f.Note, UpdatedAt: f.UpdatedAt}})
	}
	api.WriteJSON(w, h.logger, http.StatusOK, map[string]any{"items": items})
}

// outcomeView is a judged run's outcome as whoever may read the run sees it.
type outcomeView struct {
	Verdict   string        `json:"verdict"`
	Missed    string        `json:"missed"`
	Why       string        `json:"why"`
	Implicit  string        `json:"implicit"`
	Effective string        `json:"effective"`
	JudgedAt  time.Time     `json:"judged_at"`
	Feedback  feedbackCount `json:"feedback"`
}

type feedbackCount struct {
	Up   int32 `json:"up"`
	Down int32 `json:"down"`
}

// outcomeOf is a run's judged outcome and the caller's feedback on it; both
// nil for a run not judged and not answered.
func (h *handler) outcomeOf(ctx context.Context, runID, userID uuid.UUID) (*outcomeView, *feedbackView, error) {
	var mine *feedbackView
	f, err := h.store.GetRunFeedback(ctx, db.GetRunFeedbackParams{RunID: runID, UserID: userID})
	switch {
	case err == nil:
		mine = &feedbackView{Value: f.Value, Note: f.Note, UpdatedAt: f.UpdatedAt}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, nil, err
	}
	o, err := h.store.GetRunOutcome(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, mine, nil
	}
	if err != nil {
		return nil, nil, err
	}
	counts, err := h.store.CountRunFeedback(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	return &outcomeView{
		Verdict: o.Verdict, Missed: o.Missed, Why: o.Why, Implicit: o.Implicit,
		Effective: string(improve.Effective(o.Verdict, o.Implicit, counts.Up, counts.Down)),
		JudgedAt:  o.CreatedAt, Feedback: feedbackCount{Up: counts.Up, Down: counts.Down},
	}, mine, nil
}
