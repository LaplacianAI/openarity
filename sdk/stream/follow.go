package stream

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Follower serves a Log as server-sent events: everything after a cursor, then
// each new event as the log has it, until the client goes away. The zero value
// of each field is its default.
type Follower struct {
	Log Log

	// Poll is how often the log is asked for more once it has none: 500ms.
	Poll time.Duration
	// KeepAlive is how long a quiet stream waits before sending a comment, so
	// a proxy does not close it as idle: 15s.
	KeepAlive time.Duration
	// WriteTimeout is how long one frame may take to reach a slow client
	// before the stream gives it up: 30s.
	WriteTimeout time.Duration
	// Page is the most events read from the log at once: 100.
	Page int
}

// Follow writes the stream to w, starting after the cursor, until r's context
// ends or a write fails, and then returns nil. It returns the log's error when
// reading fails while the client is still there. Headers are written at once,
// so a failure after that can only end the stream.
func (f Follower) Follow(w http.ResponseWriter, r *http.Request, after int64) error {
	poll := or(f.Poll, 500*time.Millisecond)
	alive := or(f.KeepAlive, 15*time.Second)
	page := f.Page
	if page <= 0 {
		page = 100
	}
	out := frames{rc: http.NewResponseController(w), w: w, timeout: or(f.WriteTimeout, 30*time.Second)}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if !out.send(": following\n\n") {
		return nil
	}

	ctx := r.Context()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	quiet := time.NewTimer(alive)
	defer quiet.Stop()

	for {
		envs, err := f.Log.Since(ctx, after, page)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("stream: reading after %d: %w", after, err)
		}
		for _, env := range envs {
			frame, err := Frame(env)
			if err != nil {
				return err
			}
			if !out.send(frame) {
				return nil
			}
			after = env.Seq
		}
		if len(envs) > 0 {
			quiet.Reset(alive)
		}
		if len(envs) == page {
			continue
		}

		select {
		case <-ctx.Done():
			return nil
		case <-quiet.C:
			if !out.send(": keep-alive\n\n") {
				return nil
			}
			quiet.Reset(alive)
		case <-tick.C:
		}
	}
}

// Frame is one envelope as a server-sent event, ending in its blank line.
func Frame(env Envelope) (string, error) {
	kind, data, err := Encode(env)
	if err != nil {
		return "", err
	}
	// One data line: encoding/json escapes every newline inside a string, and
	// a JSON object has no other place for one.
	return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", env.Seq, kind, data), nil
}

// ErrBadCursor is a Last-Event-ID, or an after parameter, that is not a
// sequence number.
var ErrBadCursor = errors.New("stream: Last-Event-ID or after must be an event's sequence number")

// Resume is where a request asks to follow from: its Last-Event-ID header,
// which a browser's EventSource sends on reconnecting, else its after query
// parameter, else Start.
func Resume(r *http.Request) (int64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return Start, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, ErrBadCursor
	}
	return n, nil
}

type frames struct {
	rc      *http.ResponseController
	w       io.Writer
	timeout time.Duration
}

func (f frames) send(frame string) bool {
	err := f.rc.SetWriteDeadline(time.Now().Add(f.timeout))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	if _, err := io.WriteString(f.w, frame); err != nil {
		return false
	}
	return f.rc.Flush() == nil
}

func or(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}
