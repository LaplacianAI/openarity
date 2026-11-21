package improvements

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// passView is one improver pass as an approver reads it: why it ran, what
// it was given, and what came of it.
type passView struct {
	ID         uuid.UUID `json:"id"`
	Agent      ref       `json:"agent"`
	Trigger    string    `json:"trigger"`
	Signatures []string  `json:"signatures"`
	Pruned     []string  `json:"pruned"`
	Status     string    `json:"status"`
	// CutShort is a pass done before it finished: out of steps, tokens or
	// time, improvement turned off, or its agent gone.
	CutShort bool   `json:"cut_short"`
	Report   string `json:"report"`
	// ReportWithheld is a report drawn on a conversation the caller may not
	// read: it is not sent, and Report is empty.
	ReportWithheld bool       `json:"report_withheld"`
	Proposed       int32      `json:"proposed"`
	ScreenedOut    int32      `json:"screened_out"`
	Model          string     `json:"model"`
	InputTokens    int64      `json:"input_tokens"`
	OutputTokens   int64      `json:"output_tokens"`
	Error          *string    `json:"error"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

type passCursor struct {
	StartedAt time.Time `json:"s"`
	ID        uuid.UUID `json:"i"`
}

// passes lists a team's improver passes, newest first. Approvers only, and
// a pass's report only to one who may read every run it drew on: a report
// says what runs showed, and is not screened as a proposal is.
func (h *handler) passes(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	if !h.can(r, u, teamID, approveAction) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}
	params := db.ListImproverPassesParams{TeamID: teamID, PageSize: limit + 1}
	if raw := r.URL.Query().Get("agent"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "agent must be a uuid", http.StatusBadRequest)
			return
		}
		params.AgentID = &id
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c passCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor, params.AfterStartedAt, params.AfterID = true, c.StartedAt, c.ID
	}

	rows, err := h.reads.ListImproverPasses(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list improver passes", err)
		return
	}
	page, err := api.MapPage(rows, limit,
		func(row db.ImproverPass) any { return passCursor{StartedAt: row.StartedAt, ID: row.ID} },
		func(row db.ImproverPass) db.ImproverPass { return row })
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page improver passes", err)
		return
	}
	var agentIDs []uuid.UUID
	for _, p := range page.Items {
		agentIDs = append(agentIDs, p.AgentID)
	}
	n, err := h.names(r.Context(), teamID, agentIDs, nil, nil)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read agents' names", err)
		return
	}
	readable, err := h.readableRuns(r, u, teamID, page.Items)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read which runs are visible", err)
		return
	}
	out := api.Page[passView]{Items: make([]passView, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, p := range page.Items {
		report, withheld := p.Report, !readable(p.DrewOn)
		if withheld {
			report = ""
		}
		out.Items = append(out.Items, passView{
			ID: p.ID, Agent: ref{ID: p.AgentID, Name: n.Of(n.Agents, p.AgentID)}, Trigger: p.Trigger,
			Signatures: p.Signatures, Pruned: p.Pruned, Status: p.Status, CutShort: p.CutShort, Report: report, ReportWithheld: withheld,
			Proposed: p.Proposed, ScreenedOut: p.ScreenedOut, Model: p.Model,
			InputTokens: p.InputTokens, OutputTokens: p.OutputTokens, Error: p.Error,
			StartedAt: p.StartedAt, FinishedAt: p.FinishedAt,
		})
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

// readableRuns says, for a pass's runs, whether the caller may read them
// all: one read for the page. A moderator reads every conversation. A run
// since deleted is one nobody else can read, so its pass's report stays
// withheld from them: it fails closed.
func (h *handler) readableRuns(r *http.Request, u *auth.User, teamID uuid.UUID, passes []db.ImproverPass) (func([]uuid.UUID) bool, error) {
	if h.can(r, u, teamID, readEveryConvo) {
		return func([]uuid.UUID) bool { return true }, nil
	}
	var ids []uuid.UUID
	for _, p := range passes {
		ids = append(ids, p.DrewOn...)
	}
	if len(ids) == 0 {
		return func([]uuid.UUID) bool { return true }, nil
	}
	visible, err := h.reads.ListVisibleRuns(r.Context(), db.ListVisibleRunsParams{
		TeamID: teamID, Ids: ids, Moderator: false, Viewer: u.ID,
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[uuid.UUID]bool, len(visible))
	for _, v := range visible {
		seen[v.ID] = true
	}
	return func(runs []uuid.UUID) bool {
		for _, id := range runs {
			if !seen[id] {
				return false
			}
		}
		return true
	}, nil
}
