// Command sandbox runs the parts of Openarity's sandboxes, one role per
// process. The shim is the first: it runs inside a sandbox and serves the
// MCP server there, which speaks stdio, to the brain over HTTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/api"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/docker"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/guard"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/rooms"
	"github.com/LaplacianAI/openarity/apps/sandbox/internal/shim"
)

// startup bounds the server's start and its initialize; npx from an
// installed package takes a few seconds.
const startup = time.Minute

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, env func(string) string, stderr io.Writer) error {
	cmd, err := parse(args, env)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The shim's logs and the server's stderr share it, from two goroutines.
	stderr = &lockedWriter{w: stderr}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))

	switch cmd.role {
	case roleShim:
		return runShim(ctx, cmd, logger, stderr)
	case roleServe:
		return runServe(ctx, cmd, logger)
	default:
		return fmt.Errorf("unknown role %q", cmd.role)
	}
}

// runShim serves until the server exits, which ends the sandbox as failed,
// or until the shim is told to stop.
func runShim(ctx context.Context, cmd command, logger *slog.Logger, stderr io.Writer) error {
	child, err := shim.Start(ctx, cmd.argv, stderr)
	if err != nil {
		return err
	}
	starting, cancel := context.WithTimeout(ctx, startup)
	err = child.Initialize(starting)
	cancel()
	if err != nil {
		return fmt.Errorf("initializing the MCP server: %w", err)
	}

	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cmd.listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           shim.NewHandler(child, cmd.token, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	logger.Info("shim serving", "addr", l.Addr().String())

	var end error
	select {
	case <-ctx.Done():
	case <-child.Done():
		end = fmt.Errorf("the MCP server exited: %w", child.Err())
	case err := <-served:
		end = err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return end
}

// runServe is sandboxd: rooms on the daemon at the socket, until told to stop.
// Rooms outlive it; the next start finds them again from the daemon.
func runServe(ctx context.Context, cmd command, logger *slog.Logger) error {
	s := cmd.serve
	engine := docker.New(s.socket)
	if err := engine.Ping(ctx); err != nil {
		return fmt.Errorf("the Docker daemon at %s: %w", s.socket, err)
	}
	g := guard.New(logger)
	svc := rooms.New(engine, g, logger, s.limits, s.self, []byte(s.token))
	if err := svc.Reconcile(ctx); err != nil {
		return fmt.Errorf("finding the rooms again: %w", err)
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cmd.listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           api.New(svc, s.token, s.publicURL, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	logger.Info("sandboxd serving", "addr", l.Addr().String())

	var end error
	select {
	case <-ctx.Done():
	case end = <-served:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if errors.Is(end, http.ErrServerClosed) {
		end = nil
	}
	return end
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
