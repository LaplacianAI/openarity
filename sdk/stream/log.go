package stream

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

// Start is the cursor before every event: following from it replays the whole
// conversation. Sequence numbers may be negative, which is how a server numbers
// history it had before it kept a stream, so the cursor before all of them is
// the smallest int64 rather than 0.
const Start int64 = math.MinInt64

// Log is a conversation's events, in sequence order.
type Log interface {
	// Since returns at most limit events whose sequence number is greater
	// than after, in order, and none when there are none yet.
	Since(ctx context.Context, after int64, limit int) ([]Envelope, error)
}

// Memory is a Log kept in memory, numbered from 1. Safe for concurrent use.
type Memory struct {
	mu     sync.Mutex
	events []Envelope
}

// Append adds an event to the end, now, and returns it numbered.
func (m *Memory) Append(runID string, e Event) Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	env := Envelope{Seq: int64(len(m.events)) + 1, At: time.Now().UTC(), RunID: runID, Event: e}
	m.events = append(m.events, env)
	return env
}

func (m *Memory) Since(_ context.Context, after int64, limit int) ([]Envelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := sort.Search(len(m.events), func(i int) bool { return m.events[i].Seq > after })
	end := len(m.events)
	if limit > 0 && i+limit < end {
		end = i + limit
	}
	return append([]Envelope(nil), m.events[i:end]...), nil
}
