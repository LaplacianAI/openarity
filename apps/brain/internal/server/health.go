package server

import (
	"context"
	"net/http"
	"time"
)

const readyTimeout = 2 * time.Second

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()

	for _, c := range s.checks {
		if err := c.Pinger.Ping(ctx); err != nil {
			s.logger.Warn("not ready", "dependency", c.Name, "error", err)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
	}

	_, _ = w.Write([]byte("ready\n"))
}
