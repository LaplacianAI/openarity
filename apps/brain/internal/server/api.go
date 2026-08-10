package server

import (
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/middleware"
)

func (s *Server) apiHandler() http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("GET /whoami", s.whoami)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.healthz)
	root.HandleFunc("GET /readyz", s.readyz)
	root.Handle("/", middleware.Authenticate(s.verifier)(protected))

	return root
}
