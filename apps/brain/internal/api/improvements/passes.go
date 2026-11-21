package improvements

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// passView is one improver pass as an approver reads it: why it ran, what
// it was given, and what came of it.
type passView struct {
	ID           uuid.UUID  `json:"id"`
	Agent        ref        `json:"agent"`
	Trigger      string     `json:"trigger"`
	Signatures   []string   `json:"signatures"`
	Pruned       []string   `json:"pruned"`
	Status       string     `json:"status"`
	Report       string     `json:"report"`
	Proposed     int32      `json:"proposed"`
	ScreenedOut  int32      `json:"screened_out"`
	Model        string     `json:"model"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	Error        *string    `json:"error"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

type passCursor struct {
	StartedAt time.Time `json:"s"`
	ID        uuid.UUID `json:"i"`
}

// passes lists a team's improver passes, newest first. Approvers only: a
// report says what runs showed, and those may be in conversations other
// members cannot read.
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
	out := api.Page[passView]{Items: make([]passView, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, p := range page.Items {
		out.Items = append(out.Items, passView{
			ID: p.ID, Agent: ref{ID: p.AgentID, Name: n.of(n.agents, p.AgentID)}, Trigger: p.Trigger,
			Signatures: p.Signatures, Pruned: p.Pruned, Status: p.Status, Report: p.Report,
			Proposed: p.Proposed, ScreenedOut: p.ScreenedOut, Model: p.Model,
			InputTokens: p.InputTokens, OutputTokens: p.OutputTokens, Error: p.Error,
			StartedAt: p.StartedAt, FinishedAt: p.FinishedAt,
		})
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}
