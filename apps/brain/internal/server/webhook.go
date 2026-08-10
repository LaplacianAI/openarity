package server

import "net/http"

// webhookHandler is the public listener's route table: the health probe plus
// everything the gateway owns. The gateway mounts at "/" rather than at a
// shared prefix constant so this package never imports gateway — mux
// specificity keeps GET /healthz answered here, and every other request
// (including POST /healthz) falls through to the gateway's own mux.
func webhookHandler(gateway http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("/", gateway)
	return mux
}
