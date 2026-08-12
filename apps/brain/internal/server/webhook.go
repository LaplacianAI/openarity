package server

import "net/http"

// The gateway registers its own full route patterns beside the probes —
// there is no catch-all mount, so the probes stay method-scoped and a
// matching path with the wrong method is a 405 on both listeners. What the
// routes are is the gateway's business; TestWebhookHandlerRegistersTheGateway
// pins that they land on this mux with their URLs intact.
func (s *Server) webhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	s.gateway.Register(mux)
	return mux
}
