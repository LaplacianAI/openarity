package server

import "net/http"

func (s *Server) webhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	for _, r := range s.webhookRouters {
		r.Register(mux, nil)
	}
	return mux
}
