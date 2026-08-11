package server

import "net/http"

// webhookPrefix is the subtree the gateway owns on the public listener.
// Mounting a prefix rather than "/" keeps the probes method-scoped on both
// listeners — a catch-all would send POST /healthz to the gateway and answer
// 404 where the API listener answers 405.
//
// This package still learns nothing about channels: the gateway is a plain
// http.Handler and registers its own full route patterns underneath. The one
// thing the two must agree on is this prefix, which
// TestWebhookHandlerRoutesTheGatewaySubtree pins.
const webhookPrefix = "/webhook/"

func (s *Server) webhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle(webhookPrefix, s.gateway)
	return mux
}
