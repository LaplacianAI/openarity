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
	GiveRunFeedback(ctx context.Context, arg db.UpsertRunFeedbackParams) (db.RunFeedback, error)
	DeleteRunFeedback(ctx context.Context, arg db.DeleteRunFeedbackParams) (int64, error)
	GetRunOutcomeView(ctx context.Context, arg db.GetRunOutcomeViewParams) (db.GetRunOutcomeViewRow, error)
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

type sessionFeedbackList struct {
	Items []sessionFeedback `json:"items"`
}

// putFeedback records the caller's word on an answer, or replaces it. Only
// on a finished top-level run, the one that answered, and only while the
// team has improvement on: a team that has not opted in records nothing.
func (h *handler) putFeedback(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	row, ok := h.feedbackTarget(w, r)
	if !ok {
		return
	}
	if !finished(row.Status) {
		http.Error(w, "feedback is given once the run has finished", http.StatusConflict)
		return
	}
	on, err := h.improving(r.Context(), row.TeamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the team's settings", err)
		return
	}
	if !on {
		http.Error(w, "self-improvement is off for this team, so feedback is not recorded", http.StatusConflict)
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

	// A 👎 on a run judged good and left out of the sample reopens its
	// digest, in the same transaction.
	saved, err := h.store.GiveRunFeedback(r.Context(), db.UpsertRunFeedbackParams{
		RunID: row.ID, TeamID: row.TeamID, UserID: u.ID, Value: req.Value, Note: req.Note, Via: req.Via,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to save the feedback", err)
		return
	}
	h.logger.InfoContext(r.Context(), "Feedback given", "team_id", row.TeamID, "run_id", row.ID,
		"value", req.Value, "via", req.Via, "user_id", u.ID)
	api.WriteJSON(w, h.logger, http.StatusOK, feedbackView{Value: saved.Value, Note: saved.Note, UpdatedAt: saved.UpdatedAt})
}

// deleteFeedback withdraws the caller's word, whether or not the team still
// has improvement on: a word that still counts can always be taken back.
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

// feedbackTarget is the run feedback is about: one the caller may read,
// and top-level.
func (h *handler) feedbackTarget(w http.ResponseWriter, r *http.Request) (db.Run, bool) {
	row, _, ok := h.visible(w, r)
	if !ok {
		return db.Run{}, false
	}
	if row.ParentRunID != nil {
		http.Error(w, "feedback is given on the run that answered, not on work it handed to another agent", http.StatusConflict)
		return db.Run{}, false
	}
	return row, true
}

// finished is whether a run has ended, the only time its answer can be
// weighed.
func finished(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
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
	api.WriteJSON(w, h.logger, http.StatusOK, sessionFeedbackList{Items: items})
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

// outcomeOf is a run's judged outcome and the caller's feedback on it,
// read at once; each nil when there is none.
func (h *handler) outcomeOf(ctx context.Context, runID, userID uuid.UUID) (*outcomeView, *feedbackView, error) {
	v, err := h.store.GetRunOutcomeView(ctx, db.GetRunOutcomeViewParams{RunID: runID, UserID: userID})
	if err != nil {
		return nil, nil, err
	}
	var mine *feedbackView
	if v.MyValue != nil && v.MyNote != nil && v.MyUpdatedAt != nil {
		mine = &feedbackView{Value: *v.MyValue, Note: *v.MyNote, UpdatedAt: *v.MyUpdatedAt}
	}
	if v.Verdict == nil || v.Implicit == nil || v.JudgedAt == nil {
		return nil, mine, nil
	}
	return &outcomeView{
		Verdict: *v.Verdict, Missed: deref(v.Missed), Why: deref(v.Why), Implicit: *v.Implicit,
		Effective: string(improve.Effective(*v.Verdict, *v.Implicit, v.Up, v.Down)),
		JudgedAt:  *v.JudgedAt, Feedback: feedbackCount{Up: v.Up, Down: v.Down},
	}, mine, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
