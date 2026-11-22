package reaper

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// idleRows lists n idle workspaces; nothing else is asked of it here.
type idleRows struct {
	RoomRows
	n   int
	err error
}

func (r idleRows) ListIdleWorkspaceRooms(context.Context, db.ListIdleWorkspaceRoomsParams) ([]db.SandboxRoom, error) {
	if r.err != nil {
		return nil, r.err
	}
	rows := make([]db.SandboxRoom, r.n)
	for i := range rows {
		rows[i] = db.SandboxRoom{ID: uuid.New(), OwnerKind: "session", OwnerID: uuid.New(), TeamID: uuid.New()}
	}
	return rows, nil
}

// hangingRooms is a provider that is down: a stop waits until it is given up.
type hangingRooms struct {
	RoomDeleter
	stops atomic.Int32
}

func (h *hangingRooms) Stop(ctx context.Context, _ keeper.Owner, _ uuid.UUID, _ func(context.Context) (bool, error)) error {
	h.stops.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

// A provider that is down costs the sweep its pass's time, not fifty stops'
// worth, so the tombstones behind it are claimed soon after.
func TestAPassOfStopsHasATimeOfItsOwn(t *testing.T) {
	h := &hangingRooms{}
	e := roomEffect{
		rows: idleRows{n: stopsPerSweep}, rooms: h, idle: time.Minute,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		stopPass: 50 * time.Millisecond,
	}
	start := time.Now()
	e.Prepare(t.Context())
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the pass took %v", took)
	}
	if n := h.stops.Load(); n >= stopsPerSweep {
		t.Fatalf("%d stops were tried against a provider that is down", n)
	}
}

// A list that fails is logged and the sweep goes on to its tombstones:
// Prepare has no error to stop it with.
func TestAnIdleListThatFailsIsLogged(t *testing.T) {
	var logs strings.Builder
	h := &hangingRooms{}
	e := roomEffect{
		rows: idleRows{err: errors.New("the database is down")}, rooms: h, idle: time.Minute,
		logger:   slog.New(slog.NewTextHandler(&logs, nil)),
		stopPass: time.Second,
	}
	e.Prepare(t.Context())
	if h.stops.Load() != 0 || !strings.Contains(logs.String(), "the database is down") {
		t.Fatalf("%d stops; logged %q", h.stops.Load(), logs.String())
	}
}
