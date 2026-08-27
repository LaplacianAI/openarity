package inmemory

import (
	"errors"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

// New returns the read-only interface, the same way secrets.NewOpenBao does,
// so that the wiring has to ask for write access deliberately. These tests
// need it, so they ask.
func asWriter(t *testing.T, s objects.Store) objects.Writer {
	t.Helper()

	w, ok := s.(objects.Writer)
	if !ok {
		t.Fatalf("%T does not implement objects.Writer", s)
	}
	return w
}

func TestRoundTrips(t *testing.T) {
	t.Parallel()

	s := New()
	if err := asWriter(t, s).Put(t.Context(), "k", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(t.Context(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Get = %q, want %q", got, "hello")
	}
}

func TestMissingKeyIsNotFound(t *testing.T) {
	t.Parallel()

	if _, err := New().Get(t.Context(), "nope"); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("err = %v, want objects.ErrNotFound", err)
	}
}

// Deleting is how a disconnected channel's files stop existing, so it has to
// actually remove the object rather than report success and leave it.
func TestDeleteRemovesTheObject(t *testing.T) {
	t.Parallel()

	s := New()
	if err := asWriter(t, s).Put(t.Context(), "k", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := asWriter(t, s).Delete(t.Context(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(t.Context(), "k"); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want objects.ErrNotFound", err)
	}
}

// Deleting something that was never there is not an error. Disconnecting a
// channel twice, or retrying a half-finished cleanup, must not fail on the
// second attempt — and the S3 API answers the same way.
func TestDeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	if err := asWriter(t, New()).Delete(t.Context(), "never-existed"); err != nil {
		t.Errorf("Delete of a missing key: %v", err)
	}
}

// The store must not hand back a slice the caller can write through, or one
// caller's mutation becomes every later reader's value. The same applies in
// the other direction: a caller reusing its buffer after Put must not change
// what was stored.
func TestDoesNotShareItsBuffers(t *testing.T) {
	t.Parallel()

	s := New()
	body := []byte("hello")
	if err := asWriter(t, s).Put(t.Context(), "k", body); err != nil {
		t.Fatalf("Put: %v", err)
	}

	body[0] = 'J'
	got, err := s.Get(t.Context(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("mutating the caller's buffer changed the stored object: %q", got)
	}

	got[0] = 'J'
	again, err := s.Get(t.Context(), "k")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if string(again) != "hello" {
		t.Errorf("mutating a returned slice changed the stored object: %q", again)
	}
}
