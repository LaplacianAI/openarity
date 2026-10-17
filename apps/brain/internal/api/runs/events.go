package runs

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	pollEvery      = 500 * time.Millisecond
	keepAliveEvery = 15 * time.Second
	writeDeadline  = 30 * time.Second
	eventsPage     = 100
)

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	row, params, ok := h.visible(w, r)
	if !ok {
		return
	}
	after, ok := resumeFrom(w, r)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	if !send(rc, w, ": following run "+row.ID.String()+"\n\n") {
		return
	}

	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	alive := time.NewTicker(keepAliveEvery)
	defer alive.Stop()

	ended := false
	for {
		page, err := h.store.ListRunEvents(r.Context(), db.ListRunEventsParams{
			RunID: row.ID, After: after, PageSize: eventsPage,
		})
		if err != nil {
			h.logger.ErrorContext(r.Context(), "Reading run events", "run_id", row.ID, "error", err)
			return
		}
		for _, e := range page {
			if !send(rc, w, fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Kind, e.Payload)) {
				return
			}
			after = e.Seq
		}
		if len(page) == eventsPage {
			continue
		}
		if ended {
			send(rc, w, fmt.Sprintf("event: end\ndata: {\"status\":%q}\n\n", row.Status))
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-alive.C:
			if !send(rc, w, ": keep-alive\n\n") {
				return
			}
		case <-poll.C:
		}

		row, err = h.store.GetRunVisible(r.Context(), params)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "Re-reading a followed run", "run_id", params.ID, "error", err)
			return
		}
		ended = row.FinishedAt != nil
	}
}

func resumeFrom(w http.ResponseWriter, r *http.Request) (int32, bool) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0, true
	}

	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		http.Error(w, "Last-Event-ID or after must be a non-negative event number", http.StatusBadRequest)
		return 0, false
	}
	return int32(n), true
}

func send(rc *http.ResponseController, w http.ResponseWriter, frame string) bool {
	err := rc.SetWriteDeadline(time.Now().Add(writeDeadline))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	if _, err := io.WriteString(w, frame); err != nil {
		return false
	}
	return rc.Flush() == nil
}
