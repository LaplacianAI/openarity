package server

import (
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/middleware"
)

func (s *Server) apiHandler() http.Handler {
	root := http.NewServeMux()
	protected := http.NewServeMux()

	for _, r := range s.routers {
		if r.Public() {
			r.Register(root, s.guard)
			continue
		}
		r.Register(protected, s.guard)
	}

	authenticated := middleware.Authenticate(s.verifier)(middleware.ResolveUser(s.resolver, s.logger)(protected))

	root.HandleFunc("GET /healthz", s.healthz)
	root.HandleFunc("GET /readyz", s.readyz)
	root.Handle("/", authenticated)

	return root
}
