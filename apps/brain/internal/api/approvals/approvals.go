// Package approvals is how people see and answer what a team's agents asked
// them before acting, and how an admin sets when they are asked.
package approvals

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	readEveryConversation = authz.Action("session:read_all")
	decideAny             = authz.Action("approval:decide")
	decideOwn             = authz.Action("approval:self")

	maxTarget      = 200
	defaultMode    = "off"
	defaultTTL     = 60
	minTTL, maxTTL = 5, 1440
)

var (
	statuses = []string{"pending", "approved", "denied", "expired", "cancelled"}
	modes    = []string{"off", "writes", "destructive", "all"}
)

// The ways an answer is refused, as a Decider reports them.
var (
	ErrNotAllowed = errors.New("you may not answer this request")
	ErrNotPending = errors.New("the request is already answered")
	ErrExpired    = errors.New("the request expired before it was answered")
)

// An Answer is one person's yes or no, as the handler read it.
type Answer struct {
	ApprovalID, TeamID, By uuid.UUID
	Approve                bool
	Scope, Reason, Via     string
	MayDecide, MaySelf     bool
}

// Decider answers a request and resumes its run: the runtime's Decide. A
// refusal is ErrNotAllowed; one already answered is ErrNotPending, and one
// too late ErrExpired, each with the request as it stands.
type Decider interface {
	Decide(ctx context.Context, a Answer) (db.Approval, error)
}

type Store interface {
	ListVisibleApprovals(ctx context.Context, arg db.ListVisibleApprovalsParams) ([]db.Approval, error)
	GetVisibleApproval(ctx context.Context, arg db.GetVisibleApprovalParams) (db.Approval, error)
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	SetTeamApproval(ctx context.Context, arg db.SetTeamApprovalParams) (db.TeamSetting, error)
	ListApprovalRules(ctx context.Context, teamID uuid.UUID) ([]db.ApprovalRule, error)
	CreateApprovalRule(ctx context.Context, arg db.CreateApprovalRuleParams) (db.ApprovalRule, error)
	DeleteApprovalRule(ctx context.Context, arg db.DeleteApprovalRuleParams) (int64, error)
}

type handler struct {
	logger  *slog.Logger
	store   Store
	authz   api.Authorizer
	decider Decider
}

func New(logger *slog.Logger, s Store, a api.Authorizer, d Decider) *api.Router {
	h := &handler{logger: logger, store: s, authz: a, decider: d}

	r := api.NewRouter("/teams")
	r.Get("/{id}/approvals", h.list)
	r.Get("/{id}/approvals/{approvalID}", h.get)
	r.Post("/{id}/approvals/{approvalID}/decision", h.decide)
	r.Get("/{id}/approval-settings", h.settings)
	r.Put("/{id}/approval-settings", h.setSettings)
	r.Get("/{id}/approval-rules", h.rules)
	r.Post("/{id}/approval-rules", h.addRule)
	r.Delete("/{id}/approval-rules/{ruleID}", h.removeRule)
	return r
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
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
	params := db.ListVisibleApprovalsParams{
		TeamID: teamID, Moderator: h.can(r, u, teamID, readEveryConversation), Viewer: u.ID,
		Statuses: wanted, PageSize: limit + 1,
	}
	if raw := r.URL.Query().Get("run"); raw != "" {
		run, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "run must be a uuid", http.StatusBadRequest)
			return
		}
		params.RootRunID = &run
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c approvalCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor, params.AfterCreatedAt, params.AfterID = true, c.CreatedAt, c.ID
	}

	rows, err := h.store.ListVisibleApprovals(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list requests", err)
		return
	}
	page, err := api.MapPage(rows, limit, func(a db.Approval) any {
		return approvalCursor{CreatedAt: a.CreatedAt, ID: a.ID}
	}, view)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page requests", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	a, ok := h.visible(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, view(a))
}

// decide answers a request the caller may see. Who may answer is the
// runtime's to say, from the caller's role as it stands now: anyone who
// decides the team's requests, or the person it was made for when they may
// approve their own.
func (h *handler) decide(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	found, ok := h.visible(w, r)
	if !ok {
		return
	}
	var req decisionRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	if req.Approve == nil {
		http.Error(w, "approve is required: true or false", http.StatusBadRequest)
		return
	}
	switch req.Scope {
	case "", "once", "run":
	default:
		http.Error(w, "scope must be once or run", http.StatusBadRequest)
		return
	}
	via := req.Via
	switch via {
	case "":
		via = "dashboard"
	case "dashboard", "cli":
	default:
		http.Error(w, "via must be dashboard or cli", http.StatusBadRequest)
		return
	}
	if req.Scope == "run" && !*req.Approve {
		http.Error(w, "a no is for this call only: leave scope out, or send once", http.StatusBadRequest)
		return
	}

	got, err := h.decider.Decide(r.Context(), Answer{
		ApprovalID: found.ID, TeamID: found.TeamID, By: u.ID, Approve: *req.Approve,
		Scope: req.Scope, Reason: req.Reason, Via: via,
		MayDecide: h.can(r, u, found.TeamID, decideAny), MaySelf: h.can(r, u, found.TeamID, decideOwn),
	})
	switch {
	case errors.Is(err, ErrNotAllowed):
		http.Error(w, "you may not answer this request", http.StatusForbidden)
	case errors.Is(err, ErrNotPending), errors.Is(err, ErrExpired):
		// The request as it stands: what was answered, or that it expired.
		api.WriteJSON(w, h.logger, http.StatusConflict, view(got))
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	case err != nil && got.ID != uuid.Nil:
		// Answered and recorded; only handing the run back failed, and the
		// sweep starts it within a minute.
		h.logger.WarnContext(r.Context(), "Answered, but the run was not handed back; the sweep will start it",
			"approval_id", got.ID, "error", err)
		api.WriteJSON(w, h.logger, http.StatusOK, view(got))
	case err != nil:
		api.Fail(w, h.logger, u, "failed to answer the request", err)
	default:
		api.WriteJSON(w, h.logger, http.StatusOK, view(got))
	}
}

func (h *handler) visible(w http.ResponseWriter, r *http.Request) (db.Approval, bool) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return db.Approval{}, false
	}
	id, err := uuid.Parse(r.PathValue("approvalID"))
	if err != nil {
		http.Error(w, "approvalID must be a uuid", http.StatusBadRequest)
		return db.Approval{}, false
	}
	a, err := h.store.GetVisibleApproval(r.Context(), db.GetVisibleApprovalParams{
		ID: id, TeamID: teamID, Moderator: h.can(r, u, teamID, readEveryConversation), Viewer: u.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Approval{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the request", err)
		return db.Approval{}, false
	}
	return a, true
}

func (h *handler) settings(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	row, err := h.store.GetTeamSettings(r.Context(), teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A team from before approvals that never saved: its agents are not asked.
		api.WriteJSON(w, h.logger, http.StatusOK, settings{Mode: defaultMode, TTLMinutes: defaultTTL})
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the approval settings", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, settings{Mode: row.ApprovalMode, TTLMinutes: row.ApprovalTtlMinutes})
}

func (h *handler) setSettings(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	var req settings
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	if !oneOf(req.Mode, modes) {
		http.Error(w, "mode must be one of "+strings.Join(modes, ", "), http.StatusBadRequest)
		return
	}
	if req.TTLMinutes < minTTL || req.TTLMinutes > maxTTL {
		http.Error(w, "ttl_minutes must be between 5 and 1440", http.StatusBadRequest)
		return
	}
	row, err := h.store.SetTeamApproval(r.Context(), db.SetTeamApprovalParams{
		TeamID: teamID, ApprovalMode: req.Mode, ApprovalTtlMinutes: req.TTLMinutes,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to save the approval settings", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, settings{Mode: row.ApprovalMode, TTLMinutes: row.ApprovalTtlMinutes})
}

func (h *handler) rules(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	rows, err := h.store.ListApprovalRules(r.Context(), teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list the approval rules", err)
		return
	}
	out := make([]rule, len(rows))
	for i, row := range rows {
		out[i] = ruleView(row)
	}
	api.WriteJSON(w, h.logger, http.StatusOK, api.Page[rule]{Items: out})
}

func (h *handler) addRule(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	var req ruleRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	target := strings.TrimSpace(req.Target)
	switch {
	case req.TargetKind != "tool" && req.TargetKind != "server":
		http.Error(w, "target_kind must be tool or server", http.StatusBadRequest)
		return
	case target == "" || len(target) > maxTarget:
		http.Error(w, "target must be between 1 and 200 characters", http.StatusBadRequest)
		return
	case req.Mode != "allow" && req.Mode != "ask" && req.Mode != "deny":
		http.Error(w, "mode must be allow, ask or deny", http.StatusBadRequest)
		return
	}
	row, err := h.store.CreateApprovalRule(r.Context(), db.CreateApprovalRuleParams{
		TeamID: teamID, TargetKind: req.TargetKind, Target: target, Mode: req.Mode, RequireOther: req.RequireOther,
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		http.Error(w, "that target already has a rule; remove it first", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to add the approval rule", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, ruleView(row))
}

func (h *handler) removeRule(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("ruleID"))
	if err != nil {
		http.Error(w, "ruleID must be a uuid", http.StatusBadRequest)
		return
	}
	n, err := h.store.DeleteApprovalRule(r.Context(), db.DeleteApprovalRuleParams{ID: id, TeamID: teamID})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to remove the approval rule", err)
		return
	}
	if n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// can is whether the caller holds an action in the team now. A check that
// fails reads as no: a refusal, never a grant.
func (h *handler) can(r *http.Request, u *auth.User, teamID uuid.UUID, action authz.Action) bool {
	ok, err := h.authz.Can(r.Context(), u, action, authz.Resource{TeamID: teamID})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "failed to check "+string(action), "error", err, "team_id", teamID)
		return false
	}
	return ok
}

func oneOf(s string, set []string) bool {
	for _, v := range set {
		if s == v {
			return true
		}
	}
	return false
}
