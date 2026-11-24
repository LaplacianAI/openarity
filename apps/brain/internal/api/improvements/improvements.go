// Package improvements serves the proposed changes to a team's agents: the
// list, one proposal with its diff, and a person's decision on it.
package improvements

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/proposals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	approveAction  = authz.Action("improvement:approve")
	agentWrite     = authz.Action("agent:write")
	skillWrite     = authz.Action("skill:write")
	readEveryConvo = authz.Action("session:read_all")
)

var statuses = []string{"waiting", "applied", "rejected", "reverted", "withdrawn", flagged}

// flagged asks for the applied changes whose watch flagged them. It is not a
// stored status, so it stands alone.
const flagged = "flagged"

// hidden are the statuses only someone who may approve sees: they quote runs
// that may be in conversations other members cannot read.
var hidden = []string{"waiting", "rejected", "withdrawn"}

// Reads is the database the list and the view read.
type Reads interface {
	GetImprovementProposal(ctx context.Context, arg db.GetImprovementProposalParams) (db.ImprovementProposal, error)
	ListImprovementProposals(ctx context.Context, arg db.ListImprovementProposalsParams) ([]db.ImprovementProposal, error)
	ListAgentsHoldingSkill(ctx context.Context, skillID uuid.UUID) ([]db.ListAgentsHoldingSkillRow, error)
	ListTeamAgentNames(ctx context.Context, arg db.ListTeamAgentNamesParams) ([]db.ListTeamAgentNamesRow, error)
	ListTeamSkillNames(ctx context.Context, arg db.ListTeamSkillNamesParams) ([]db.ListTeamSkillNamesRow, error)
	ListTeamServerNames(ctx context.Context, arg db.ListTeamServerNamesParams) ([]db.ListTeamServerNamesRow, error)
	ListVisibleRuns(ctx context.Context, arg db.ListVisibleRunsParams) ([]db.ListVisibleRunsRow, error)
	ListImproverPasses(ctx context.Context, arg db.ListImproverPassesParams) ([]db.ImproverPass, error)
	GetImprovementWatch(ctx context.Context, proposalID uuid.UUID) (db.ImprovementWatch, error)
	ListImprovementWatches(ctx context.Context, ids []uuid.UUID) ([]db.ImprovementWatch, error)
	ListPassPruneProposals(ctx context.Context, passIDs []uuid.UUID) ([]db.ListPassPruneProposalsRow, error)
	ListProposalReplays(ctx context.Context, proposalIDs []uuid.UUID) ([]db.ListProposalReplaysRow, error)
	GetProposalReplay(ctx context.Context, arg db.GetProposalReplayParams) (db.GetProposalReplayRow, error)
}

type handler struct {
	logger  *slog.Logger
	reads   Reads
	authz   api.Authorizer
	decider proposals.Decider
}

func New(logger *slog.Logger, reads Reads, a api.Authorizer, d proposals.Decider) *api.Router {
	h := &handler{logger: logger, reads: reads, authz: a, decider: d}

	r := api.NewRouter("/teams")
	r.Get("/{id}/improvements", h.list)
	r.Get("/{id}/improvements/passes", h.passes)
	r.Get("/{id}/improvements/{proposalID}", h.get)
	r.Get("/{id}/improvements/{proposalID}/replays/{runID}", h.replay)
	r.Post("/{id}/improvements/{proposalID}/decision", h.decide)
	return r
}

type ref struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type runRef struct {
	ID       uuid.UUID `json:"id"`
	Readable bool      `json:"readable"`
	// Where the run can be read, given only when the caller may open it.
	SessionID *uuid.UUID `json:"session_id,omitempty"`
}

type view struct {
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Agent      ref       `json:"agent"`
	Skill      *ref      `json:"skill"`
	Why        string    `json:"why"`
	Hypothesis string    `json:"hypothesis"`
	Scope      string    `json:"scope"`
	Signature  *string   `json:"signature"`
	// Goal is quality, for what went wrong, or efficiency, for a good run
	// made cheaper or quicker; Saves names cost, time or both for the latter.
	Goal     string             `json:"goal"`
	Saves    []string           `json:"saves"`
	Marks    []string           `json:"marks"`
	Reaches  []ref              `json:"reaches"`
	Changes  []proposals.Change `json:"changes"`
	Runs     []runRef           `json:"runs"`
	Outsider bool               `json:"outsider"`
	// The improver pass that wrote it; null for one written otherwise.
	PassID     *uuid.UUID `json:"pass_id"`
	CreatedAt  time.Time  `json:"created_at"`
	DecidedBy  *uuid.UUID `json:"decided_by"`
	DecidedAt  *time.Time `json:"decided_at"`
	RevertedBy *uuid.UUID `json:"reverted_by"`
	RevertedAt *time.Time `json:"reverted_at"`
	// How the change went once applied; null until it is.
	Watch *watchView `json:"watch"`
	// How it fared on past runs before it was decided.
	Replay *replayView `json:"replay"`
}

type cursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	approver := h.can(r, u, teamID, approveAction)

	asked, ok := api.OneOf(w, r, "status", statuses)
	if !ok {
		return
	}
	if len(asked) == 0 {
		asked = []string{"applied"}
		if approver {
			asked = []string{"waiting"}
		}
	}
	if !approver && slices.ContainsFunc(asked, func(s string) bool { return slices.Contains(hidden, s) }) {
		http.Error(w, "forbidden", http.StatusForbidden)
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
	params := db.ListImprovementProposalsParams{TeamID: teamID, Statuses: asked, Q: q, PageSize: limit + 1}
	if slices.Contains(asked, flagged) {
		if len(asked) > 1 {
			http.Error(w, "status flagged cannot be combined with another status", http.StatusBadRequest)
			return
		}
		params.Statuses, params.Flagged = []string{"applied"}, true
	}
	if raw := r.URL.Query().Get("agent"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "agent must be a uuid", http.StatusBadRequest)
			return
		}
		params.AgentID = &id
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c cursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor, params.AfterCreatedAt, params.AfterID = true, c.CreatedAt, c.ID
	}

	rows, err := h.reads.ListImprovementProposals(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list improvements", err)
		return
	}
	page, err := api.MapPage(rows, limit,
		func(row db.ImprovementProposal) any { return cursor{CreatedAt: row.CreatedAt, ID: row.ID} },
		func(row db.ImprovementProposal) db.ImprovementProposal { return row })
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page improvements", err)
		return
	}

	// The page's watches in one read, rather than one a proposal.
	ids := make([]uuid.UUID, len(page.Items))
	for i, row := range page.Items {
		ids[i] = row.ID
	}
	watches, err := h.reads.ListImprovementWatches(r.Context(), ids)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the improvements' watches", err)
		return
	}
	byID := make(map[uuid.UUID]db.ImprovementWatch, len(watches))
	for _, w := range watches {
		byID[w.ProposalID] = w
	}
	tried, err := h.replays(r, u, teamID, page.Items)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the improvements' tries on past runs", err)
		return
	}

	out := api.Page[view]{Items: make([]view, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, row := range page.Items {
		v, err := h.view(r, u, row)
		if err != nil {
			api.Fail(w, h.logger, u, "failed to read an improvement", err)
			return
		}
		if watch, ok := byID[row.ID]; ok {
			v.Watch = watchViewOf(watch)
		}
		v.Replay = tried[row.ID]
		out.Items = append(out.Items, v)
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	row, ok := h.find(w, r, u)
	if !ok {
		return
	}
	v, err := h.view(r, u, row)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the improvement", err)
		return
	}
	if err := h.withWatch(r, &v); err != nil {
		api.Fail(w, h.logger, u, "failed to read the improvement's watch", err)
		return
	}
	if err := h.withReplay(r, u, &v, row); err != nil {
		api.Fail(w, h.logger, u, "failed to read the improvement's tries on past runs", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, v)
}

type decision struct {
	Action string `json:"action"`
}

func (h *handler) decide(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	var req decision
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	if !slices.Contains([]string{proposals.ActionApprove, proposals.ActionReject, proposals.ActionRevert, proposals.ActionClear}, req.Action) {
		http.Error(w, "action must be approve, reject, revert or clear", http.StatusBadRequest)
		return
	}

	row, ok := h.find(w, r, u)
	if !ok {
		return
	}
	// Making or undoing a change needs what making it by hand needs, beside
	// the approve permission the route already checked. Rejecting writes
	// nothing to the agent or skill, so approve alone decides it, as it does
	// clearing a flag.
	var needs []authz.Action
	if req.Action != proposals.ActionReject && req.Action != proposals.ActionClear {
		needs = writesFor(proposals.Kind(row.Kind))
	}
	for _, need := range needs {
		if !h.can(r, u, teamID, need) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	out, err := h.decider.Decide(r.Context(), teamID, row.ID, u.ID, req.Action)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case errors.Is(err, proposals.ErrChanged):
		http.Error(w, changedMessage(targetWord(row), req.Action), http.StatusConflict)
		return
	case proposals.WrongState(err), proposals.Refused(err):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		api.Fail(w, h.logger, u, "failed to decide the improvement", err)
		return
	}

	h.logger.InfoContext(r.Context(), "Improvement decided",
		"team_id", teamID, "improvement_id", row.ID, "kind", row.Kind, "action", req.Action, "user_id", u.ID)
	v, err := h.view(r, u, out)
	if err != nil {
		// The decision is committed; failing the response now would say it was not.
		h.logger.ErrorContext(r.Context(), "Failed to read the decided improvement",
			"improvement_id", out.ID, "error", err)
		api.WriteJSON(w, h.logger, http.StatusOK, map[string]any{"id": out.ID, "status": out.Status})
		return
	}
	// Committed either way: a watch that cannot be read is left out, not failed.
	if err := h.withWatch(r, &v); err != nil {
		h.logger.ErrorContext(r.Context(), "Failed to read the decided improvement's watch", "improvement_id", out.ID, "error", err)
	}
	api.WriteJSON(w, h.logger, http.StatusOK, v)
}

// changedMessage says why a decision on a target changed since was refused,
// and what is left to do.
func changedMessage(target, action string) string {
	if action == proposals.ActionRevert {
		return "the " + target + " changed since this was applied, so it cannot be put back automatically; undo the change by hand"
	}
	return "the " + target + " changed since this was proposed, so it cannot be applied; reject it"
}

func writesFor(k proposals.Kind) []authz.Action {
	switch k {
	case proposals.KindSkillEdit:
		return []authz.Action{skillWrite}
	case proposals.KindSkillNew:
		return []authz.Action{agentWrite, skillWrite}
	case proposals.KindInstructions, proposals.KindDescription, proposals.KindSkillGrant, proposals.KindSkillDrop, proposals.KindServerGrant, proposals.KindServerDrop,
		proposals.KindToolsNarrow, proposals.KindBuiltinTool, proposals.KindToolNote, proposals.KindLimits, proposals.KindDelegation, proposals.KindPattern:
		return []authz.Action{agentWrite}
	}
	// A kind this does not know needs both, which is the most a change can.
	return []authz.Action{agentWrite, skillWrite}
}

func targetWord(row db.ImprovementProposal) string {
	if row.SkillID != nil {
		return "skill"
	}
	return "agent"
}

// find reads a proposal the caller may see: a hidden status, to someone who
// may not approve, is as absent as another team's.
func (h *handler) find(w http.ResponseWriter, r *http.Request, u *auth.User) (db.ImprovementProposal, bool) {
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return db.ImprovementProposal{}, false
	}
	id, err := uuid.Parse(r.PathValue("proposalID"))
	if err != nil {
		http.Error(w, "proposalID must be a uuid", http.StatusBadRequest)
		return db.ImprovementProposal{}, false
	}
	row, err := h.reads.GetImprovementProposal(r.Context(), db.GetImprovementProposalParams{ID: id, TeamID: teamID})
	if errors.Is(err, pgx.ErrNoRows) ||
		(err == nil && slices.Contains(hidden, row.Status) && !h.can(r, u, teamID, approveAction)) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.ImprovementProposal{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the improvement", err)
		return db.ImprovementProposal{}, false
	}
	return row, true
}

// withWatch adds a proposal's watch to its view; none until it is applied.
func (h *handler) withWatch(r *http.Request, v *view) error {
	watch, err := h.reads.GetImprovementWatch(r.Context(), v.ID)
	switch {
	case err == nil:
		v.Watch = watchViewOf(watch)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	return nil
}

// view is a proposal as the API shows it, without its watch: a page reads
// its watches at once, and get reads the one.
func (h *handler) view(r *http.Request, u *auth.User, row db.ImprovementProposal) (view, error) {
	ctx := r.Context()
	p, err := proposals.Decode(row)
	if err != nil {
		return view{}, err
	}

	agentIDs, skillIDs, serverIDs := proposals.IDs(p)
	agentIDs = append(agentIDs, row.AgentID)
	if row.SkillID != nil {
		skillIDs = append(skillIDs, *row.SkillID)
	}
	n, err := h.names(ctx, row.TeamID, agentIDs, skillIDs, serverIDs)
	if err != nil {
		return view{}, err
	}

	v := view{
		ID: row.ID, Kind: row.Kind, Status: row.Status,
		Agent: ref{ID: row.AgentID, Name: n.Agent(row.AgentID)},
		Why:   row.Why, Hypothesis: row.Hypothesis, Scope: row.Scope, Signature: row.Signature,
		Goal: row.Goal, Saves: row.Saves,
		Marks: proposals.Marks(proposals.Kind(row.Kind), p.Before, p.After), Reaches: []ref{}, Changes: proposals.Changes(p, n),
		Runs: []runRef{}, Outsider: row.Outsider, PassID: row.PassID, CreatedAt: row.CreatedAt,
		DecidedBy: row.DecidedBy, DecidedAt: row.DecidedAt, RevertedBy: row.RevertedBy, RevertedAt: row.RevertedAt,
	}
	if v.Marks == nil {
		v.Marks = []string{}
	}
	if v.Saves == nil {
		v.Saves = []string{}
	}
	if row.SkillID != nil {
		v.Skill = &ref{ID: *row.SkillID, Name: n.Skill(*row.SkillID)}
		holders, err := h.reads.ListAgentsHoldingSkill(ctx, *row.SkillID)
		if err != nil {
			return view{}, err
		}
		for _, a := range holders {
			v.Reaches = append(v.Reaches, ref{ID: a.ID, Name: a.Name})
		}
	}

	if len(row.RunIds) > 0 {
		visible, err := h.reads.ListVisibleRuns(ctx, db.ListVisibleRunsParams{
			TeamID: row.TeamID, Ids: row.RunIds,
			Moderator: h.can(r, u, row.TeamID, readEveryConvo), Viewer: u.ID,
		})
		if err != nil {
			return view{}, err
		}
		for _, id := range row.RunIds {
			ref := runRef{ID: id}
			if i := slices.IndexFunc(visible, func(r db.ListVisibleRunsRow) bool { return r.ID == id }); i >= 0 {
				ref.Readable, ref.SessionID = true, &visible[i].SessionID
			}
			v.Runs = append(v.Runs, ref)
		}
	}
	return v, nil
}

func (h *handler) names(ctx context.Context, teamID uuid.UUID, agentIDs, skillIDs, serverIDs []uuid.UUID) (proposals.Names, error) {
	n := proposals.Names{Agents: map[uuid.UUID]string{}, Skills: map[uuid.UUID]string{}, Servers: map[uuid.UUID]string{}}
	as, err := h.reads.ListTeamAgentNames(ctx, db.ListTeamAgentNamesParams{TeamID: teamID, Ids: agentIDs})
	if err != nil {
		return proposals.Names{}, err
	}
	for _, a := range as {
		n.Agents[a.ID] = a.Name
	}
	ss, err := h.reads.ListTeamSkillNames(ctx, db.ListTeamSkillNamesParams{TeamID: teamID, Ids: skillIDs})
	if err != nil {
		return proposals.Names{}, err
	}
	for _, s := range ss {
		n.Skills[s.ID] = s.Name
	}
	ms, err := h.reads.ListTeamServerNames(ctx, db.ListTeamServerNamesParams{TeamID: teamID, Ids: serverIDs})
	if err != nil {
		return proposals.Names{}, err
	}
	for _, m := range ms {
		n.Servers[m.ID] = m.Name
	}
	return n, nil
}

// can is whether the caller holds an action in the team now. A check that
// fails reads as no.
func (h *handler) can(r *http.Request, u *auth.User, teamID uuid.UUID, action authz.Action) bool {
	ok, err := h.authz.Can(r.Context(), u, action, authz.Resource{TeamID: teamID})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "failed to check "+string(action), "error", err, "team_id", teamID)
		return false
	}
	return ok
}
