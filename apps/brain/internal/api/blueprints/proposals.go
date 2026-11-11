package blueprints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	// maxProposals bounds how often one builder run may propose, so a model
	// that keeps fixing and failing stops rather than looping.
	maxProposals = 5
	// maxProposalBytes is a proposal's blueprint as stored: skill files are
	// text the model wrote, so a megabyte is far more than one needs.
	maxProposalBytes = 1 << 20
)

// readEveryConversation is the permission that sees every direct session, as
// the runs API uses it.
const readEveryConversation = authz.Action("session:read_all")

type Proposals interface {
	CountRunProposals(ctx context.Context, runID uuid.UUID) (int64, error)
	GetProposalVisible(ctx context.Context, arg db.GetProposalVisibleParams) (db.Proposal, error)
	ListProposalsVisible(ctx context.Context, arg db.ListProposalsVisibleParams) ([]db.Proposal, error)
	ListTeamToolNames(ctx context.Context, teamID uuid.UUID) ([]db.ListTeamToolNamesRow, error)
}

// Workshop is what the builder agent reaches: the team read as a blueprint,
// and a proposal recorded. It writes nothing to the team; a person applies a
// proposal, as themselves.
type Workshop struct {
	store Store
	loops []string
}

func NewWorkshop(s Store, loops []string) *Workshop {
	return &Workshop{store: s, loops: loops}
}

// teamView is the team as the builder reads it.
type teamView struct {
	Blueprint Blueprint           `json:"blueprint"`
	Skills    []skillView         `json:"skills"`
	Tools     map[string][]string `json:"tools"`
}

type skillView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Team is the team's MCP servers and custom agents as a blueprint, its skills
// by name and description, and each server's tools by name.
func (ws *Workshop) Team(ctx context.Context, teamID uuid.UUID) (string, error) {
	t, err := load(ctx, ws.store, teamID)
	if err != nil {
		return "", err
	}
	bp, err := exportOf(t, nil, nil)
	if err != nil {
		return "", err
	}
	v := teamView{Blueprint: bp, Skills: []skillView{}, Tools: map[string][]string{}}
	for _, s := range t.sortedSkills() {
		v.Skills = append(v.Skills, skillView{Name: s.Name, Description: s.Description})
	}
	tools, err := ws.store.ListTeamToolNames(ctx, teamID)
	if err != nil {
		return "", err
	}
	for _, row := range tools {
		v.Tools[row.Server] = append(v.Tools[row.Server], row.Tool)
	}
	raw, err := json.Marshal(v)
	return string(raw), err
}

// Proposed is what a proposal came to: its id, and a report of its plan for
// the model to read.
type Proposed struct {
	ID       uuid.UUID
	Report   string
	Changes  int
	Refusals int
}

// ErrProposal is a proposal refused before it was recorded; the model reads
// the reason and may try again.
var ErrProposal = errors.New("proposal refused")

// Propose plans a blueprint against the team and records it with its plan.
// The plan is permission-free, saying what would change; who may apply it is
// decided when someone does.
func (ws *Workshop) Propose(ctx context.Context, run db.Run, raw json.RawMessage, summary string) (Proposed, error) {
	n, err := ws.store.CountRunProposals(ctx, run.ID)
	if err != nil {
		return Proposed{}, err
	}
	if n >= maxProposals {
		return Proposed{}, fmt.Errorf("%w: this run has proposed %d times, the most it may; say what is still wrong instead", ErrProposal, maxProposals)
	}
	if len(raw) > maxProposalBytes {
		return Proposed{}, fmt.Errorf("%w: the blueprint is over 1 MiB", ErrProposal)
	}
	bp, err := fromProposal(raw)
	if err != nil {
		return Proposed{}, fmt.Errorf("%w: %w", ErrProposal, err)
	}
	if len(bp.MCPServers) > maxItems || len(bp.Skills) > maxItems || len(bp.Agents) > maxItems {
		return Proposed{}, fmt.Errorf("%w: at most %d of each kind", ErrProposal, maxItems)
	}
	if len(bp.MCPServers)+len(bp.Skills)+len(bp.Agents) == 0 {
		return Proposed{}, fmt.Errorf("%w: the blueprint names nothing", ErrProposal)
	}

	t, err := load(ctx, ws.store, run.TeamID)
	if err != nil {
		return Proposed{}, err
	}
	p := planOf(t, bp, ws.loops, may{KindMCPServer: true, KindSkill: true, KindAgent: true})

	stored, err := json.Marshal(bp)
	if err != nil {
		return Proposed{}, err
	}
	plan, err := json.Marshal(p.Plan)
	if err != nil {
		return Proposed{}, err
	}
	var row db.Proposal
	err = ws.store.InTx(ctx, func(q Queries) error {
		if err := q.SupersedeProposals(ctx, run.SessionID); err != nil {
			return err
		}
		row, err = q.CreateProposal(ctx, db.CreateProposalParams{
			TeamID: run.TeamID, SessionID: run.SessionID, RunID: run.ID, Summary: summary, Blueprint: stored, Plan: plan,
		})
		return err
	})
	if err != nil {
		return Proposed{}, err
	}

	changes := 0
	for _, c := range p.Changes {
		if c.Action != ActionUnchanged {
			changes++
		}
	}
	return Proposed{ID: row.ID, Report: report(p.Plan), Changes: changes, Refusals: len(p.Refusals)}, nil
}

// proposalSkill is a skill as the builder writes it: text files, which a
// blueprint carries as bytes.
type proposalSkill struct {
	Files []struct {
		Path string `json:"path"`
		Text string `json:"text"`
	} `json:"files"`
}

func fromProposal(raw json.RawMessage) (Blueprint, error) {
	var in struct {
		MCPServers []serverSpec    `json:"mcp_servers"`
		Skills     []proposalSkill `json:"skills"`
		Agents     []agentSpec     `json:"agents"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Blueprint{}, fmt.Errorf("the blueprint does not read: %w", err)
	}
	bp := Blueprint{MCPServers: in.MCPServers, Agents: in.Agents}
	for _, s := range in.Skills {
		var spec skillSpec
		for _, f := range s.Files {
			spec.Files = append(spec.Files, skillFile{Path: f.Path, Data: []byte(f.Text)})
		}
		bp.Skills = append(bp.Skills, spec)
	}
	return bp, nil
}

// report is a plan in lines the model can act on.
func report(p Plan) string {
	var b strings.Builder
	for _, c := range p.Changes {
		switch c.Action {
		case ActionCreate:
			fmt.Fprintf(&b, "create %s %s\n", c.Kind, c.Name)
		case ActionUpdate:
			fmt.Fprintf(&b, "update %s %s: %s\n", c.Kind, c.Name, strings.Join(c.Fields, ", "))
		case ActionUnchanged:
			fmt.Fprintf(&b, "unchanged %s %s\n", c.Kind, c.Name)
		default:
			fmt.Fprintf(&b, "%s %s %s\n", c.Action, c.Kind, c.Name)
		}
	}
	for _, r := range p.Refusals {
		fmt.Fprintf(&b, "REFUSED %s %s: %s\n", r.Kind, r.Name, r.Message)
	}
	return b.String()
}

// proposal is the API's view: the stored plan for one no longer open, and for
// an open one the plan made now, as the reader, which is what applying it
// would do.
type proposal struct {
	ID        uuid.UUID       `json:"id"`
	SessionID uuid.UUID       `json:"session_id"`
	RunID     uuid.UUID       `json:"run_id"`
	Summary   string          `json:"summary"`
	State     string          `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
	AppliedAt *time.Time      `json:"applied_at"`
	Blueprint json.RawMessage `json:"blueprint,omitempty"`
	Plan      *Plan           `json:"plan,omitempty"`
}

func summaryOf(row db.Proposal) proposal {
	return proposal{
		ID: row.ID, SessionID: row.SessionID, RunID: row.RunID, Summary: row.Summary,
		State: row.State, CreatedAt: row.CreatedAt, AppliedAt: row.AppliedAt,
	}
}

type proposalCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (h *handler) listProposals(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListProposalsVisibleParams{
		TeamID: teamID, PageSize: limit + 1, Moderator: h.moderates(r, teamID), Viewer: u.ID,
	}
	if s := r.URL.Query().Get("session"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			http.Error(w, "session must be a session's id", http.StatusBadRequest)
			return
		}
		params.SessionID = &id
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c proposalCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor, params.AfterCreatedAt, params.AfterID = true, c.CreatedAt, c.ID
	}

	rows, err := h.store.ListProposalsVisible(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list proposals", err)
		return
	}
	out, err := api.MapPage(rows, limit, func(row db.Proposal) any {
		return proposalCursor{CreatedAt: row.CreatedAt, ID: row.ID}
	}, summaryOf)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page proposals", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) getProposal(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	row, teamID, ok := h.visibleProposal(w, r)
	if !ok {
		return
	}
	out := summaryOf(row)
	out.Blueprint = row.Blueprint

	if row.State != "open" {
		var p Plan
		if err := json.Unmarshal(row.Plan, &p); err != nil {
			api.Fail(w, h.logger, u, "failed to read the proposal's plan", err)
			return
		}
		out.Plan = &p
		api.WriteJSON(w, h.logger, http.StatusOK, out)
		return
	}

	bp, ok := h.blueprintOf(w, r, row)
	if !ok {
		return
	}
	p, ok := h.planned(w, r, u, teamID, bp)
	if !ok {
		return
	}
	out.Plan = &p.Plan
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

type applyProposal struct {
	PlanDigest string `json:"plan_digest"`
}

// applyProposal applies an open proposal as the caller: planned again with
// their permissions, written only if the plan is the one they were shown,
// and marked applied in the same transaction, so it is applied once.
func (h *handler) applyProposal(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	row, teamID, ok := h.visibleProposal(w, r)
	if !ok {
		return
	}
	var req applyProposal
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	if req.PlanDigest == "" {
		http.Error(w, "plan_digest is required: read the proposal, and apply the plan you were shown", http.StatusBadRequest)
		return
	}
	if row.State != "open" {
		http.Error(w, "this proposal is "+row.State+", so it cannot be applied", http.StatusConflict)
		return
	}

	bp, ok := h.blueprintOf(w, r, row)
	if !ok {
		return
	}
	p, ok := h.planned(w, r, u, teamID, bp)
	switch {
	case !ok:
		return
	case len(p.Refusals) > 0:
		api.WriteJSON(w, h.logger, http.StatusUnprocessableEntity, p.Plan)
		return
	case p.Digest != req.PlanDigest:
		api.WriteJSON(w, h.logger, http.StatusConflict, p.Plan)
		return
	}

	errGone := errors.New("the proposal was applied or superseded meanwhile")
	err := h.write(r.Context(), teamID, &p, func(q Queries) error {
		_, err := q.MarkProposalApplied(r.Context(), db.MarkProposalAppliedParams{ID: row.ID, AppliedBy: &u.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errGone
		}
		return err
	})
	if errors.Is(err, errGone) {
		http.Error(w, errGone.Error()+": nothing was applied", http.StatusConflict)
		return
	}
	if h.writeFailed(w, u, err) {
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, applied{Changes: p.Changes})
}

// writeFailed answers a failed apply the way the blueprint apply does: a
// name taken meanwhile is a conflict to plan again for.
func (h *handler) writeFailed(w http.ResponseWriter, u *auth.User, err error) bool {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return false
	case errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation:
		http.Error(w, "the team changed while this was applied: plan again", http.StatusConflict)
	default:
		api.Fail(w, h.logger, u, "failed to apply the proposal", err)
	}
	return true
}

func (h *handler) visibleProposal(w http.ResponseWriter, r *http.Request) (db.Proposal, uuid.UUID, bool) {
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return db.Proposal{}, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("proposalID"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Proposal{}, uuid.Nil, false
	}
	u := api.Caller(r)
	row, err := h.store.GetProposalVisible(r.Context(), db.GetProposalVisibleParams{
		ID: id, TeamID: teamID, Moderator: h.moderates(r, teamID), Viewer: u.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Proposal{}, uuid.Nil, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the proposal", err)
		return db.Proposal{}, uuid.Nil, false
	}
	return row, teamID, true
}

func (h *handler) blueprintOf(w http.ResponseWriter, r *http.Request, row db.Proposal) (Blueprint, bool) {
	var bp Blueprint
	if err := json.Unmarshal(row.Blueprint, &bp); err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to read the proposal's blueprint", err)
		return Blueprint{}, false
	}
	return bp, true
}

func (h *handler) moderates(r *http.Request, teamID uuid.UUID) bool {
	ok, err := h.authz.Can(r.Context(), api.Caller(r), readEveryConversation, authz.Resource{TeamID: teamID})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "failed to check "+string(readEveryConversation), "error", err, "team_id", teamID)
		return false
	}
	return ok
}
