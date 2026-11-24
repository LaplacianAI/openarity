package runtime

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sized answers every request with the number of bytes its path names.
func sized(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		_, _ = w.Write(bytes.Repeat([]byte("x"), n))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// open sends method for n bytes through c, and returns the answer unread.
func open(t *testing.T, c *http.Client, srv *httptest.Server, method string, n int) *http.Response {
	t.Helper()
	// A read that waits when it should not fails the test, not the package.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+"/"+strconv.Itoa(n), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %d: %v", method, n, err)
	}
	return resp
}

// An answer is read to maxAnswerBytes and no further: one byte past it fails
// the read, naming the cap.
func TestAnAnswerPastTheCapFails(t *testing.T) {
	t.Parallel()
	srv := sized(t)
	c := cappedClient(&http.Client{}, make(chan struct{}, 1))

	at := open(t, c, srv, http.MethodPost, maxAnswerBytes)
	if b, err := io.ReadAll(at.Body); err != nil || len(b) != maxAnswerBytes {
		t.Errorf("at the cap: read %d, err %v; want all of it", len(b), err)
	}
	_ = at.Body.Close()

	past := open(t, c, srv, http.MethodPost, maxAnswerBytes+1)
	defer past.Body.Close()
	if _, err := io.ReadAll(past.Body); err == nil || !strings.Contains(err.Error(), "over 40 MiB") {
		t.Errorf("past the cap: err %v, want the cap named", err)
	}
}

// One large POST answer is read at a time: a second waits while the first
// is open and goes once it closes; a small one and a GET stream never wait.
func TestALargeAnswerWaitsForTheWorkersSlot(t *testing.T) {
	t.Parallel()
	srv := sized(t)
	large := make(chan struct{}, 1)
	c := cappedClient(&http.Client{}, large)

	// Read past the line and not to its end, so it holds the slot.
	first := open(t, c, srv, http.MethodPost, 2*largeAnswer)
	if _, err := io.ReadFull(first.Body, make([]byte, largeAnswer+1)); err != nil {
		t.Fatal(err)
	}
	if len(large) != 1 {
		t.Fatal("the first large answer read without the slot")
	}

	exact := open(t, c, srv, http.MethodPost, largeAnswer)
	if b, err := io.ReadAll(exact.Body); err != nil || len(b) != largeAnswer {
		t.Errorf("an answer of exactly largeAnswer: read %d, err %v", len(b), err)
	}
	_ = exact.Body.Close()
	get := open(t, c, srv, http.MethodGet, 2*largeAnswer)
	if b, err := io.ReadAll(get.Body); err != nil || len(b) != 2*largeAnswer {
		t.Errorf("a GET past largeAnswer: read %d, err %v; want it read without the slot", len(b), err)
	}
	_ = get.Body.Close()

	second := open(t, c, srv, http.MethodPost, 2*largeAnswer)
	t.Cleanup(func() { _ = second.Body.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(second.Body)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the second large answer was read while the first held the slot (err %v)", err)
	case <-time.After(100 * time.Millisecond):
	}

	_ = first.Body.Close()
	_ = first.Body.Close() // gives the slot back once, not twice
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the second: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second large answer never got the slot")
	}
	// The second was read to its end, which gave the slot back unclosed.
	if len(large) != 0 {
		t.Errorf("the slot holds %d after the second was read to its end, want 0", len(large))
	}
}

// An answer read to its end gives the slot back before it is closed, and a
// read after its end never takes it again.
func TestAnAnswerReadToItsEndGivesTheSlotBack(t *testing.T) {
	t.Parallel()
	srv := sized(t)
	large := make(chan struct{}, 1)
	c := cappedClient(&http.Client{}, large)

	resp := open(t, c, srv, http.MethodPost, 2*largeAnswer)
	t.Cleanup(func() { _ = resp.Body.Close() })
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if len(large) != 0 {
		t.Fatal("an answer read to its end still holds the slot")
	}
	large <- struct{}{} // another answer takes it
	if n, err := resp.Body.Read(make([]byte, 8)); n != 0 || err != io.EOF {
		t.Errorf("a read after the end: %d, %v; want EOF at once, not a wait for the slot", n, err)
	}
}

// A client with no slot, as a Builder not made by NewBuilder has, reads a
// large answer rather than waiting for ever.
func TestNoSlotIsNoWait(t *testing.T) {
	t.Parallel()
	srv := sized(t)
	resp := open(t, cappedClient(&http.Client{}, nil), srv, http.MethodPost, 2*largeAnswer)
	t.Cleanup(func() { _ = resp.Body.Close() })
	if b, err := io.ReadAll(resp.Body); err != nil || len(b) != 2*largeAnswer {
		t.Errorf("read %d, err %v; want all of it", len(b), err)
	}
}

// A large answer waiting for the slot gives up when its request ends.
func TestAWaitingAnswerGivesUpWithItsRequest(t *testing.T) {
	t.Parallel()
	srv := sized(t)
	large := make(chan struct{}, 1)
	large <- struct{}{} // held by another answer
	c := cappedClient(&http.Client{}, large)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/"+strconv.Itoa(2*largeAnswer), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("read the large answer without the slot")
	}
	if len(large) != 1 {
		t.Errorf("the slot holds %d, want only the other answer's", len(large))
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A read that would cross largeAnswer stops at it while another answer holds
// the slot, however large the buffer: no byte past the line is read without
// the slot.
func TestNoBytePastTheLineIsReadWithoutTheSlot(t *testing.T) {
	t.Parallel()
	large := make(chan struct{}, 1)
	large <- struct{}{} // held by another answer
	c := &capped{large: large, next: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(make([]byte, 2*largeAnswer)))}, nil
	})}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mcp.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 2*largeAnswer)
	if n, err := resp.Body.Read(buf); n > largeAnswer || err != nil {
		t.Fatalf("read %d bytes, err %v; want at most %d without the slot", n, err, largeAnswer)
	}
	if n, err := io.ReadFull(resp.Body, buf); n != 0 || err == nil {
		t.Errorf("read %d more bytes, err %v; want none until the slot is free", n, err)
	}
}
