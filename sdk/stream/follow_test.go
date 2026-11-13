package stream

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// follow serves the log and opens the stream, after a cursor when one is
// given, returning its body and headers. The body is closed with the test.
func follow(t *testing.T, f Follower, after string) (io.Reader, http.Header) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, err := Resume(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := f.Follow(w, r, from); err != nil {
			t.Errorf("Follow: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res.Body, res.Header
}

func first(r io.Reader, _ http.Header) io.Reader { return r }

func next(t *testing.T, r *Reader) Envelope {
	t.Helper()
	got := make(chan Envelope, 1)
	failed := make(chan error, 1)
	go func() {
		env, err := r.Next()
		if err != nil {
			failed <- err
			return
		}
		got <- env
	}()
	select {
	case env := <-got:
		return env
	case err := <-failed:
		t.Fatalf("reading: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5s")
	}
	return Envelope{}
}

func TestFollowSendsWhatIsThereThenWhatComes(t *testing.T) {
	var m Memory
	m.Append("r", StepStarted{Step: 1})
	body, header := follow(t, Follower{Log: &m, Poll: 5 * time.Millisecond}, "")
	if got := header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type %q", got)
	}
	r := NewReader(body)
	if env := next(t, r); env.Seq != 1 {
		t.Fatalf("first event %+v", env)
	}
	m.Append("r", RunFinished{Status: StatusSucceeded})
	if env := next(t, r); env.Seq != 2 || env.Event.Kind() != KindRunFinished {
		t.Fatalf("second event %+v", env)
	}
}

func TestFollowResumesAfterTheLastEventSeen(t *testing.T) {
	var m Memory
	for i := range 4 {
		m.Append("r", StepStarted{Step: i})
	}
	r := NewReader(first(follow(t, Follower{Log: &m}, "2")))
	if env := next(t, r); env.Seq != 3 {
		t.Fatalf("resumed at %d, want 3", env.Seq)
	}
}

// A full page means more may be waiting: the next is read at once, not after
// a poll. With a poll of an hour, only that keeps the events coming.
func TestFollowReadsAFullPageAgainWithoutWaiting(t *testing.T) {
	var m Memory
	for range 5 {
		m.Append("r", StepStarted{})
	}
	r := NewReader(first(follow(t, Follower{Log: &m, Page: 2, Poll: time.Hour}, "")))
	for want := int64(1); want <= 5; want++ {
		if env := next(t, r); env.Seq != want {
			t.Fatalf("got %d, want %d", env.Seq, want)
		}
	}
}

func TestFollowKeepsAQuietStreamAlive(t *testing.T) {
	var m Memory
	body, _ := follow(t, Follower{Log: &m, KeepAlive: 10 * time.Millisecond, Poll: time.Hour}, "")
	sc := bufio.NewScanner(body)
	seen := make(chan bool, 1)
	go func() {
		for sc.Scan() {
			if sc.Text() == ": keep-alive" {
				seen <- true
				return
			}
		}
	}()
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("no keep-alive within 5s")
	}
}

type failing struct{ calls int }

func (f *failing) Since(context.Context, int64, int) ([]Envelope, error) {
	f.calls++
	return nil, errors.New("the database went away")
}

func TestFollowReturnsTheLogsError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	err := Follower{Log: &failing{}}.Follow(rec, req, Start)
	if err == nil || !strings.Contains(err.Error(), "the database went away") {
		t.Fatalf("got %v", err)
	}
}

// A log failing because the client left is the client leaving, not an error.
func TestFollowEndsQuietlyWhenTheClientLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	done := make(chan error, 1)
	go func() {
		done <- Follower{Log: &Memory{}, Poll: time.Millisecond}.Follow(httptest.NewRecorder(), req, Start)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("got %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Follow did not return when its client left")
	}

	cancelled, stop := context.WithCancel(t.Context())
	stop()
	err := Follower{Log: &failing{}}.Follow(httptest.NewRecorder(), httptest.NewRequestWithContext(cancelled, http.MethodGet, "/", nil), Start)
	if err != nil {
		t.Fatalf("a log failing after the client left: got %v, want nil", err)
	}
}

type brokenWriter struct {
	httptest.ResponseRecorder
	after int
}

func (b *brokenWriter) Write(p []byte) (int, error) {
	if b.after == 0 {
		return 0, errors.New("broken pipe")
	}
	b.after--
	return b.ResponseRecorder.Write(p)
}

// ResponseRecorder has its own WriteString, which io.WriteString would
// prefer over the failing Write above.
func (b *brokenWriter) WriteString(s string) (int, error) { return b.Write([]byte(s)) }

func TestFollowStopsAtTheFirstWriteThatFails(t *testing.T) {
	var m Memory
	m.Append("r", StepStarted{})
	m.Append("r", StepStarted{})
	for after := range 2 {
		w := &brokenWriter{ResponseRecorder: *httptest.NewRecorder(), after: after}
		log := &counting{Log: &m}
		err := Follower{Log: log, Poll: time.Hour}.Follow(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil), Start)
		if err != nil {
			t.Fatalf("after %d writes: got %v, want nil", after, err)
		}
		if after == 0 && log.calls != 0 {
			t.Fatalf("read the log %d times for a client that was never there", log.calls)
		}
	}
}

type counting struct {
	Log
	calls int
}

func (c *counting) Since(ctx context.Context, after int64, limit int) ([]Envelope, error) {
	c.calls++
	return c.Log.Since(ctx, after, limit)
}

// An event the frame cannot be written for ends the stream with an error,
// rather than a frame that would split into two.
func TestFollowRefusesAnEventItCannotFrame(t *testing.T) {
	var m Memory
	m.Append("r", Unknown{Of: "bad\nkind", Data: []byte(`{}`)})
	// Bounded: a Follow that framed the event would wait for more, and the
	// test should fail by name rather than by the suite's timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := Follower{Log: &m}.Follow(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil), Start)
	if err == nil {
		t.Fatal("followed an event with a newline in its kind")
	}
}

func TestResume(t *testing.T) {
	cases := []struct {
		header, query string
		want          int64
		bad           bool
	}{
		{"", "", Start, false},
		{"12", "", 12, false},
		{"-40", "", -40, false},
		{"", "7", 7, false},
		{"3", "9", 3, false},
		{"x", "", 0, true},
		{"", "1.5", 0, true},
		{strconv.FormatUint(1<<63, 10), "", 0, true},
	}
	for _, c := range cases {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?after="+c.query, nil)
		if c.query == "" {
			req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		}
		if c.header != "" {
			req.Header.Set("Last-Event-ID", c.header)
		}
		got, err := Resume(req)
		if c.bad {
			if !errors.Is(err, ErrBadCursor) {
				t.Errorf("Resume(%q, %q): got %v, want ErrBadCursor", c.header, c.query, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Resume(%q, %q) = %d, %v; want %d", c.header, c.query, got, err, c.want)
		}
	}
}

func TestTheZeroFollowerHasItsDefaults(t *testing.T) {
	if or(0, time.Second) != time.Second || or(-1, time.Second) != time.Second || or(time.Minute, time.Second) != time.Minute {
		t.Fatal("or does not fall back on a duration that is not positive")
	}
}
