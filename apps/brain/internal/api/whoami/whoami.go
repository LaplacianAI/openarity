package whoami

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

type whoamiResponse struct {
	ID      uuid.UUID    `json:"id"`
	Kind    string       `json:"kind"`
	Issuer  string       `json:"issuer,omitempty"`
	Subject string       `json:"subject"`
	Email   *string      `json:"email,omitempty"`
	Teams   []whoamiTeam `json:"teams"`
}

type whoamiTeam struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role"`
}

type handler struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *api.Router {
	h := &handler{logger: logger}

	r := api.NewRouter("/whoami")
	r.Get("", h.whoami)
	return r
}

func (h *handler) whoami(w http.ResponseWriter, r *http.Request) {
	p, okPrincipal := auth.PrincipalFrom(r.Context())
	u, okUser := auth.UserFrom(r.Context())
	if !okPrincipal || !okUser {
		h.logger.Error("whoami ran without a principal or user — check the middleware order")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	teams := make([]whoamiTeam, len(u.Teams))
	for i, m := range u.Teams {
		teams[i] = whoamiTeam{ID: m.TeamID, Name: m.Name, Role: m.Role}
	}

	api.WriteJSON(w, h.logger, http.StatusOK, whoamiResponse{
		ID:      u.ID,
		Kind:    string(p.Kind),
		Issuer:  u.Issuer,
		Subject: u.Subject,
		Email:   u.Email,
		Teams:   teams,
	})
}
