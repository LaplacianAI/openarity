package shim

import (
	"crypto/rand"
	"sync"
	"time"
)

// idleSession is how long a session lasts with nothing sent on it. A run
// that pauses longer opens a new one; its tools are the same.
const idleSession = time.Hour

// sessions are the callers sharing the server: discovery, and each run that
// calls one of its tools. Each keeps the requests it has in flight, so a
// cancellation it sends names the server's id, not its own.
type sessions struct {
	mu   sync.Mutex
	open map[string]*session
	now  func() time.Time
}

type session struct {
	last     time.Time
	inflight map[string]int64 // the caller's request id → the server's
}

func newSessions(now func() time.Time) *sessions {
	return &sessions{open: map[string]*session{}, now: now}
}

func (s *sessions) start() string {
	id := rand.Text()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	s.open[id] = &session{last: s.now(), inflight: map[string]int64{}}
	return id
}

// live reports whether id is a session still open, and keeps it open.
func (s *sessions) live(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.touch(id) != nil
}

func (s *sessions) end(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.touch(id) == nil {
		return false
	}
	delete(s.open, id)
	return true
}

// sent records that the caller's request is the server's id; done forgets it.
func (s *sessions) sent(id, request string, server int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.open[id]; ss != nil {
		ss.inflight[request] = server
	}
}

func (s *sessions) done(id, request string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.open[id]; ss != nil {
		delete(ss.inflight, request)
	}
}

// server is the server's id for the caller's request, while it is in flight.
func (s *sessions) server(id, request string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.open[id]
	if ss == nil {
		return 0, false
	}
	n, ok := ss.inflight[request]
	return n, ok
}

func (s *sessions) touch(id string) *session {
	ss := s.open[id]
	if ss == nil {
		return nil
	}
	if s.now().Sub(ss.last) > idleSession {
		delete(s.open, id)
		return nil
	}
	ss.last = s.now()
	return ss
}

func (s *sessions) sweep() {
	for id, ss := range s.open {
		if s.now().Sub(ss.last) > idleSession {
			delete(s.open, id)
		}
	}
}
