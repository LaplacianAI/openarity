package shim

import (
	"testing"
	"time"
)

func TestASessionIdleForAnHourIsGone(t *testing.T) {
	now := time.Unix(0, 0)
	s := newSessions(func() time.Time { return now })
	idle, busy := s.start(), s.start()
	s.sent(busy, "1", 5)

	now = now.Add(idleSession - time.Second)
	if !s.live(busy) {
		t.Fatal("a session used within the hour is gone")
	}
	now = now.Add(2 * time.Second)

	if s.live(idle) {
		t.Error("a session idle past the hour is still live")
	}
	if !s.live(busy) {
		t.Error("the session used a moment ago is gone")
	}
	if _, ok := s.server(idle, "1"); ok {
		t.Error("an expired session still maps a request")
	}
	if n, ok := s.server(busy, "1"); !ok || n != 5 {
		t.Errorf("busy maps request 1 to %d, %v", n, ok)
	}
	if s.end(idle) {
		t.Error("an expired session was ended as if live")
	}
}

func TestStartingASessionSweepsTheExpired(t *testing.T) {
	now := time.Unix(0, 0)
	s := newSessions(func() time.Time { return now })
	s.start()
	now = now.Add(idleSession + time.Second)
	s.start()
	if n := len(s.open); n != 1 {
		t.Errorf("%d sessions held, want 1", n)
	}
}

func TestARequestIsForgottenWhenDone(t *testing.T) {
	s := newSessions(time.Now)
	id := s.start()
	s.sent(id, `"a"`, 3)
	s.done(id, `"a"`)
	if _, ok := s.server(id, `"a"`); ok {
		t.Error("a finished request still maps to the server's id")
	}
	s.sent("unknown", "1", 1) // must not panic or open a session
	s.done("unknown", "1")
	if s.live("unknown") {
		t.Error("recording a request opened a session")
	}
}
