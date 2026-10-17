package runs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// frame is one server-sent event as a client reads it.
type frame struct {
	id, event, data string
}

// frames parses an event stream, skipping comments.
func frames(body string) []frame {
	var out []frame
	for _, block := range strings.Split(body, "\n\n") {
		var f frame
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if f.event != "" {
			out = append(out, f)
		}
	}
	return out
}

func ids(fs []frame) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		if f.id != "" {
			out = append(out, f.id)
		}
	}
	return out
}

func (w *world) events(id uuid.UUID, kinds ...string) {
	for i, k := range kinds {
		w.store.events[id] = append(w.store.events[id], db.RunEvent{
			RunID: id, Seq: int32(i + 1), Kind: k, Payload: []byte(fmt.Sprintf(`{"n": %d}`, i+1)),
		})
	}
}

// finish marks the run ended and writes its finished event, as a worker does:
// the row first, the event a statement later.
func (w *world) finish(id uuid.UUID) {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()

	r := w.store.runs[id]
	now := time.Now()
	r.Status, r.FinishedAt = "succeeded", &now
	w.store.runs[id] = r
	seq := int32(len(w.store.events[id]) + 1)
	w.store.events[id] = append(w.store.events[id], db.RunEvent{
		RunID: id, Seq: seq, Kind: "finished", Payload: []byte(`{"status": "succeeded"}`),
	})
}

func TestEachEventIsAFrameNamedByItsSequence(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "succeeded")
	w.events(r.ID, "started", "step", "finished")

	rec := w.call(t, w.me, http.MethodGet, w.one(r.ID)+"/events", nil)

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	got := frames(rec.Body.String())
	want := []frame{
		{"1", "started", `{"n": 1}`},
		{"2", "step", `{"n": 2}`},
		{"3", "finished", `{"n": 3}`},
		{"", "end", `{"status":"succeeded"}`},
	}
	if !slices.Equal(got, want) {
		t.Errorf("frames\n got %+v\nwant %+v", got, want)
	}
}

func TestTheEventStreamResumesAfterLastEventID(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "succeeded")
	w.events(r.ID, "started", "step", "usage", "step", "finished")

	for name, tc := range map[string]struct {
		query  string
		header []string
		want   []string
	}{
		"Last-Event-ID":             {"", []string{"Last-Event-ID", "2"}, []string{"3", "4", "5"}},
		"after":                     {"?after=3", nil, []string{"4", "5"}},
		"the header over the query": {"?after=1", []string{"Last-Event-ID", "4"}, []string{"5"}},
		"from the start":            {"", nil, []string{"1", "2", "3", "4", "5"}},
		"after the last":            {"?after=5", nil, []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := w.call(t, w.me, http.MethodGet, w.one(r.ID)+"/events"+tc.query, nil, tc.header...)
			if got := ids(frames(rec.Body.String())); !slices.Equal(got, tc.want) {
				t.Errorf("ids %v, want %v", got, tc.want)
			}
		})
	}
}

func TestABadResumePointIs400(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "succeeded")

	for _, tc := range [][]string{
		{"?after=nope"}, {"?after=-1"}, {"", "Last-Event-ID", "x"}, {"?after=99999999999"},
	} {
		rec := w.call(t, w.me, http.MethodGet, w.one(r.ID)+"/events"+tc[0], nil, tc[1:]...)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", tc, rec.Code)
		}
	}
}

func TestTheEventStreamEndsAfterTheRunFinishes(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "running")
	w.events(r.ID, "started", "step")
	go func() {
		time.Sleep(700 * time.Millisecond)
		w.finish(r.ID)
	}()

	rec := w.call(t, w.me, http.MethodGet, w.one(r.ID)+"/events", nil)

	got := frames(rec.Body.String())
	if len(got) != 4 || got[2].event != "finished" || got[3].event != "end" || got[3].data != `{"status":"succeeded"}` {
		t.Errorf("frames %+v, want started, step, finished, end", got)
	}
}

// A run cancelled before any worker took it never has a finished event; the
// stream ends on its status.
func TestAStreamOfARunCancelledWhileQueuedEnds(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "cancelled")

	rec := w.call(t, w.me, http.MethodGet, w.one(r.ID)+"/events", nil)

	if got := frames(rec.Body.String()); len(got) != 1 || got[0].data != `{"status":"cancelled"}` {
		t.Errorf("frames %+v, want only the end, cancelled", got)
	}
}

func TestTheStreamStopsWhenTheClientGoesAway(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "running")

	ctx, leave := context.WithCancel(auth.WithUser(t.Context(), w.me))
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, w.one(r.ID)+"/events", http.NoBody)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.mux.ServeHTTP(httptest.NewRecorder(), req)
	}()
	time.Sleep(300 * time.Millisecond)
	leave()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler was still streaming 3s after its client left")
	}
}

// The server's WriteTimeout bounds a whole response, and a run takes minutes.
// The stream pushes its own deadline forward before every write.
func TestAnEventStreamOutlivesTheServersWriteTimeout(t *testing.T) {
	w := newWorld(t)
	r := w.run(w.session("group", nil), "running")
	w.events(r.ID, "started")

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		w.mux.ServeHTTP(rw, req.WithContext(auth.WithUser(req.Context(), w.me)))
	}))
	server.Config.WriteTimeout = 500 * time.Millisecond
	server.Start()
	t.Cleanup(server.Close)

	go func() {
		for i := range 4 {
			time.Sleep(400 * time.Millisecond)
			w.store.mu.Lock()
			w.store.events[r.ID] = append(w.store.events[r.ID], db.RunEvent{
				RunID: r.ID, Seq: int32(i + 2), Kind: "step", Payload: []byte(`{}`),
			})
			w.store.mu.Unlock()
		}
		w.finish(r.ID)
	}()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+w.one(r.ID)+"/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var body strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		body.WriteString(scanner.Text() + "\n")
	}
	if !strings.Contains(body.String(), "event: end") {
		t.Errorf("the stream ended without its end (%v) after:\n%s", scanner.Err(), body.String())
	}
	if n := len(frames(body.String())); n != 7 {
		t.Errorf("%d frames, want started, four steps, finished and end", n)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}
