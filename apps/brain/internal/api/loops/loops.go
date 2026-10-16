package loops

import (
	"log/slog"
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
)

type handler struct {
	logger *slog.Logger
	items  []loop
}

func New(logger *slog.Logger, loops []string) *api.Router {
	items := make([]loop, len(loops))
	for i, name := range loops {
		items[i] = loop{Name: name}
	}
	h := &handler{logger: logger, items: items}

	r := api.NewRouter("/teams")
	r.Get("/{id}/loops", h.list)
	return r
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := api.RequireTeam(w, r, h.logger); !ok {
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, api.Page[loop]{Items: h.items})
}
