package server

import (
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/middleware"
)

func (s *Server) apiHandler() http.Handler {
	protected := http.NewServeMux()
	for _, r := range s.routers {
		r.Register(protected)
	}

	authenticated := middleware.Authenticate(s.verifier)(middleware.ResolveUser(s.resolver, s.logger)(protected))

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.healthz)
	root.HandleFunc("GET /readyz", s.readyz)
	root.Handle("/", authenticated)

	return root
}
