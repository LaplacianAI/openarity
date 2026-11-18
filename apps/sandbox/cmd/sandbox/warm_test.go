package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A warm-up that cannot finish must not keep sandboxd from serving: here the
// daemon never answers the pull, and /healthz still does.
func TestStartupWarmUpDoesNotBlockServing(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "wm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	pulling := make(chan struct{}, 1)
	daemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
		case r.URL.Path == "/v1.44/networks":
			_, _ = io.WriteString(w, "[]")
		case strings.HasSuffix(r.URL.Path, "/json"):
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			select {
			case pulling <- struct{}{}:
			default:
			}
			<-r.Context().Done() // the registry never answers
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = daemon.Serve(l) }()
	t.Cleanup(func() { _ = daemon.Close() })

	// reserve a port
	pl, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pl.Addr().String()
	_ = pl.Close()

	c, err := parse([]string{"serve", "--listen", addr}, envOf(serveEnv(map[string]string{
		"SANDBOXD_DOCKER_SOCKET": sock,
		"SANDBOXD_PULL":          "ghcr.io/laplacianai/openarity-sandbox:1",
	})))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, c, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	select {
	case <-pulling:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm-up never asked the daemon to pull")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandboxd does not serve while it warms: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sandboxd did not stop")
	}
}
