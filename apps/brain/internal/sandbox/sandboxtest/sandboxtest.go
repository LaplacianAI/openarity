// Package sandboxtest is a sandbox provider for tests: Rooms whose rooms are
// real HTTP servers. Each answers /healthz and /mcp as the shim in apps/sandbox
// does, and demands the bearer token it was created with, so a test of the
// code that makes rooms drives real requests rather than a stub. Only tests
// import it.
//
// The apps share no code, so this is a small fake of the shim, not the shim:
// it is as faithful as the contract the brain relies on, which is the three
// things above.
package sandboxtest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// ProviderHeader is the header every room's endpoint carries, as a real
// provider's carries its own token.
const ProviderHeader = "X-Sandboxtest-Provider"

// Rooms is a provider whose rooms are httptest servers.
type Rooms struct {
	// TokenEnv is the variable a room reads its bearer token from.
	TokenEnv string

	mu      sync.Mutex
	rooms   map[string]*room
	seq     int
	created []sandbox.Spec
	deleted []string
	starts  [][]string

	// CreateErr, StartErr and DeleteErr fail those calls.
	CreateErr, StartErr, DeleteErr error
	// NeverHealthy makes rooms that are started and never answer /healthz.
	NeverHealthy bool
	// CreateDelay holds Create, so concurrent callers overlap.
	CreateDelay time.Duration

	t *testing.T
}

type room struct {
	id      string
	token   string
	argv    []string
	started bool
	srv     *httptest.Server
	rooms   *Rooms
}

var _ sandbox.Rooms = (*Rooms)(nil)

// New is a provider. Its rooms stop with the test.
func New(t *testing.T, tokenEnv string) *Rooms {
	t.Helper()
	r := &Rooms{TokenEnv: tokenEnv, rooms: map[string]*room{}, t: t}
	t.Cleanup(func() {
		for _, id := range r.IDs() {
			r.Lose(id)
		}
	})
	return r
}

func (r *Rooms) Create(ctx context.Context, s sandbox.Spec) (string, error) {
	if r.CreateDelay > 0 {
		select {
		case <-time.After(r.CreateDelay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.CreateErr != nil {
		return "", r.CreateErr
	}
	r.seq++
	rm := &room{id: fmt.Sprintf("room-%d", r.seq), token: s.Env[r.TokenEnv], rooms: r}
	rm.srv = httptest.NewServer(rm)
	r.rooms[rm.id] = rm
	r.created = append(r.created, s)
	return rm.id, nil
}

func (r *Rooms) Start(_ context.Context, id string, argv []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.StartErr != nil {
		return r.StartErr
	}
	rm, ok := r.rooms[id]
	if !ok {
		return sandbox.ErrNotFound
	}
	rm.started, rm.argv = true, argv
	r.starts = append(r.starts, argv)
	return nil
}

func (r *Rooms) URL(_ context.Context, id string, _ int) (sandbox.Endpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rm, ok := r.rooms[id]
	if !ok {
		return sandbox.Endpoint{}, sandbox.ErrNotFound
	}
	return sandbox.Endpoint{URL: rm.srv.URL, Header: http.Header{ProviderHeader: {"1"}}}, nil
}

func (r *Rooms) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	if r.DeleteErr != nil {
		err := r.DeleteErr
		r.mu.Unlock()
		return err
	}
	rm, ok := r.rooms[id]
	if ok {
		delete(r.rooms, id)
		r.deleted = append(r.deleted, id)
	}
	r.mu.Unlock()

	// Closed with the lock released: Close waits for requests in flight, and
	// a request takes the lock.
	if ok {
		rm.srv.Close()
	}
	return nil
}

// ServeHTTP is the room: what the shim answers.
func (rm *room) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rm.rooms.mu.Lock()
	up := rm.started && !rm.rooms.NeverHealthy
	token := rm.token
	rm.rooms.mu.Unlock()

	switch req.URL.Path {
	case "/healthz":
		if !up {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/mcp":
		if req.Header.Get("Authorization") != "Bearer "+token || token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, req)
	}
}

// Live is how many rooms exist.
func (r *Rooms) Live() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rooms)
}

// Created is every spec a room was made with, in order.
func (r *Rooms) Created() []sandbox.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sandbox.Spec(nil), r.created...)
}

// Starts is the argv of every command started, in order.
func (r *Rooms) Starts() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.starts...)
}

// Deleted is the id of every room deleted, in order.
func (r *Rooms) Deleted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.deleted...)
}

// Has is whether a room exists.
func (r *Rooms) Has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.rooms[id]
	return ok
}

// Lose removes a room as a provider loses one: without a Delete from the brain.
func (r *Rooms) Lose(id string) {
	r.mu.Lock()
	rm, ok := r.rooms[id]
	delete(r.rooms, id)
	r.mu.Unlock()
	if ok {
		rm.srv.Close()
	}
}

// Break makes a room's command exit, as a crashed server does: the room is
// there and does not answer.
func (r *Rooms) Break(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rm, ok := r.rooms[id]; ok {
		rm.started = false
	}
}

// IDs are the ids of the rooms that exist, oldest first is not promised.
func (r *Rooms) IDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.rooms))
	for id := range r.rooms {
		out = append(out, id)
	}
	return out
}
