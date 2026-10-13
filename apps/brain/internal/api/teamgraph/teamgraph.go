package teamgraph

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
)

const (
	defaultLimit = 500
	maxLimit     = 2000
)

type Viewer interface {
	View(ctx context.Context, team uuid.UUID, limit int) (graph.Snapshot, bool, error)
}

type handler struct {
	logger *slog.Logger
	viewer Viewer
}

type node struct {
	ID          uuid.UUID `json:"id"`
	Label       string    `json:"label"`
	Name        string    `json:"name,omitempty"`
	Description string    `json:"description,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Model       string    `json:"model,omitempty"`
}

type edge struct {
	Type string    `json:"type"`
	From uuid.UUID `json:"from"`
	To   uuid.UUID `json:"to"`
	Only []string  `json:"only,omitempty"`
}

type view struct {
	Nodes     []node `json:"nodes"`
	Edges     []edge `json:"edges"`
	Truncated bool   `json:"truncated"`
}

func New(logger *slog.Logger, v Viewer) *api.Router {
	h := &handler{logger: logger, viewer: v}

	r := api.NewRouter("/teams")
	r.Get("/{id}/graph", h.get)
	return r
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	team, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			http.Error(w, "limit must be a whole number from 1 to 2000", http.StatusBadRequest)
			return
		}
		limit = n
	}

	if h.viewer == nil {
		http.Error(w, "the graph is not enabled on this brain", http.StatusServiceUnavailable)
		return
	}

	snap, truncated, err := h.viewer.View(r.Context(), team, limit)
	if err != nil {
		h.logger.WarnContext(r.Context(), "graph view failed", "team_id", team, "error", err)
		http.Error(w, "the graph store is unavailable", http.StatusServiceUnavailable)
		return
	}

	out := view{Nodes: make([]node, 0, len(snap.Nodes)), Edges: make([]edge, 0, len(snap.Edges)), Truncated: truncated}
	for _, n := range snap.Nodes {
		v := node{ID: n.ID, Label: string(n.Label), Model: n.Model}
		v.Name, _ = n.Props["name"].(string)
		v.Description, _ = n.Props["description"].(string)
		v.Kind, _ = n.Props["kind"].(string)
		out.Nodes = append(out.Nodes, v)
	}
	for _, e := range snap.Edges {
		v := edge{Type: string(e.Type), From: e.From.ID, To: e.To.ID}
		if only, ok := e.Props["only"].([]any); ok {
			for _, tool := range only {
				if s, ok := tool.(string); ok {
					v.Only = append(v.Only, s)
				}
			}
		}
		out.Edges = append(out.Edges, v)
	}

	api.WriteJSON(w, h.logger, http.StatusOK, out)
}
