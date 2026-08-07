package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/middleware"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

type Server struct {
	api     *http.Server
	webhook *http.Server
	logger  *slog.Logger
}

func New(cfg *config.Config, logger *slog.Logger) *Server {
	logRequests := middleware.LogRequests(logger)

	return &Server{
		api:     newHTTPServer(cfg.APIBind, logRequests(apiHandler())),
		webhook: newHTTPServer(cfg.WebhookBind, logRequests(webhookHandler())),
		logger:  logger,
	}
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func listen(s *http.Server) error {
	err := s.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Run(ctx context.Context) error {
	s.logger.Info("Listening on API bind", "bind", s.api.Addr)
	s.logger.Info("Listening on Webhook bind", "bind", s.webhook.Addr)

	errs := make(chan error, 2)

	go func() {
		errs <- listen(s.api)
	}()
	go func() {
		errs <- listen(s.webhook)
	}()

	select {
	case err := <-errs:
		return errors.Join(err, s.shutdown())
	case <-ctx.Done():
		return s.shutdown()
	}
}

func (s *Server) shutdown() error {
	s.logger.Info("Shutting down servers", "timeout", shutdownTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	return errors.Join(s.api.Shutdown(ctx), s.webhook.Shutdown(ctx))
}
