package server

import (
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

type whoamiResponse struct {
	Kind    string `json:"kind"`
	Issuer  string `json:"issuer,omitempty"`
	Subject string `json:"subject"`
	Email   string `json:"email,omitempty"`
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		s.logger.Error("whoami ran without a principal — the route is not behind the auth middleware")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, http.StatusOK, whoamiResponse{
		Kind:    string(p.Kind),
		Issuer:  p.Issuer,
		Subject: p.Subject,
		Email:   p.Email,
	})
}
