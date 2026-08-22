package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
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

type Pinger interface {
	Ping(ctx context.Context) error
}

type Router interface {
	Register(mux *http.ServeMux, g api.RouteGuard)
	Public() bool
}

type Deps struct {
	Checks         []Check
	Verifier       auth.Verifier
	Resolver       middleware.Resolver
	Guard          api.RouteGuard
	WebhookRouters []Router
}

type Check struct {
	Name   string
	Pinger Pinger
}

type Server struct {
	api            *http.Server
	webhook        *http.Server
	logger         *slog.Logger
	verifier       auth.Verifier
	resolver       middleware.Resolver
	guard          api.RouteGuard
	routers        []Router
	webhookRouters []Router
	checks         []Check
}

func New(cfg *config.Config, logger *slog.Logger, deps Deps, routers ...Router) *Server {
	if len(deps.Checks) == 0 {
		panic("server.New: Deps.Checks is empty; readiness would always pass")
	}
	if deps.Guard == nil {
		panic("server.New: Deps.Guard is nil; Register would panic on the first route")
	}
	for _, r := range deps.WebhookRouters {
		if !r.Public() {
			panic("server.New: webhook router is not public; the webhook listener has no guard")
		}
	}
	s := &Server{
		logger:         logger,
		checks:         deps.Checks,
		verifier:       deps.Verifier,
		resolver:       deps.Resolver,
		guard:          deps.Guard,
		routers:        routers,
		webhookRouters: deps.WebhookRouters,
	}
	logRequests := middleware.LogRequests(logger)
	s.api = newHTTPServer(cfg.APIBind, logRequests(s.apiHandler()))
	s.webhook = newHTTPServer(cfg.WebhookBind, logRequests(s.webhookHandler()))
	return s
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

	var first error
	pending := 2
	select {
	case first = <-errs:
		pending = 1
	case <-ctx.Done():
	}

	err := errors.Join(first, s.shutdown())
	for range pending {
		<-errs
	}
	return err
}

func (s *Server) shutdown() error {
	s.logger.Info("Shutting down servers", "timeout", shutdownTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	return errors.Join(s.api.Shutdown(ctx), s.webhook.Shutdown(ctx))
}
