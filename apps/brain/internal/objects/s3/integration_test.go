package s3

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

// The stub in s3_test.go encodes what this package believes an S3 API does.
// These tests are what check that belief against a real one. They skip unless
// a store is reachable:
//
//	cd deployment && make objects
//	# then export what it prints
//
// Skipping rather than failing keeps `make test` useful with nothing running,
// the same contract the Postgres and OpenBao tests keep.
func liveS3(t *testing.T) objects.Store {
	t.Helper()

	endpoint := os.Getenv("BRAIN_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("BRAIN_TEST_S3_ENDPOINT is not set")
	}

	bucket := os.Getenv("BRAIN_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "openarity"
	}

	s, err := New(Config{
		Endpoint:  endpoint,
		Region:    "us-east-1",
		Bucket:    bucket,
		AccessKey: os.Getenv("BRAIN_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("BRAIN_TEST_S3_SECRET_KEY"),
	})
	if err != nil {
		t.Fatalf("New against %s: %v", endpoint, err)
	}
	return s
}

// A key nobody else is using, so these can run in parallel against a shared
// store and against a store somebody is also using by hand.
func liveKey() string {
	return objects.TeamPrefix(uuid.New()) + "objects/" + uuid.NewString()
}

func TestRoundTripAgainstRealObjectStore(t *testing.T) {
	t.Parallel()

	s := liveS3(t)
	w := asWriter(t, s)
	k := liveKey()

	if err := w.Put(t.Context(), k, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// WithoutCancel so the cleanup still runs when the test's context is
	// already done — otherwise a failure leaves the object behind.
	t.Cleanup(func() { _ = w.Delete(context.WithoutCancel(t.Context()), k) })

	got, err := s.Get(t.Context(), k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Get = %q, want %q", got, "hello")
	}
}

// The mapping the stub asserts, against a store that decides for itself what
// an absent key looks like. This is the half a stub cannot prove: AWS returns
// a typed NoSuchKey, and other implementations return a bare 404.
func TestMissingKeyIsNotFoundAgainstRealObjectStore(t *testing.T) {
	t.Parallel()

	if _, err := liveS3(t).Get(t.Context(), liveKey()); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("err = %v, want objects.ErrNotFound", err)
	}
}

func TestDeleteRemovesTheObjectAgainstRealObjectStore(t *testing.T) {
	t.Parallel()

	s := liveS3(t)
	w := asWriter(t, s)
	k := liveKey()

	if err := w.Put(t.Context(), k, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := w.Delete(t.Context(), k); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(t.Context(), k); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want objects.ErrNotFound", err)
	}
}

// Deleting something that was never there must not be an error, the contract
// all three adapters keep. Worth asserting against a real store because it is
// the store's behaviour being relied on, not ours.
func TestDeleteIsIdempotentAgainstRealObjectStore(t *testing.T) {
	t.Parallel()

	if err := asWriter(t, liveS3(t)).Delete(t.Context(), liveKey()); err != nil {
		t.Errorf("Delete of a missing key: %v", err)
	}
}

// Overwriting replaces rather than appends or fails.
func TestPutOverwritesAgainstRealObjectStore(t *testing.T) {
	t.Parallel()

	s := liveS3(t)
	w := asWriter(t, s)
	k := liveKey()
	t.Cleanup(func() { _ = w.Delete(context.WithoutCancel(t.Context()), k) })

	for _, body := range []string{"first", "second"} {
		if err := w.Put(t.Context(), k, []byte(body)); err != nil {
			t.Fatalf("Put(%q): %v", body, err)
		}
	}

	got, err := s.Get(t.Context(), k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("Get = %q, want %q", got, "second")
	}
}

// Bytes, not text. An attachment is a photograph or a PDF, and a store or an
// adapter that mangles a NUL, a high byte or a newline would pass every test
// above.
func TestBinaryContentSurvivesARoundTrip(t *testing.T) {
	t.Parallel()

	s := liveS3(t)
	w := asWriter(t, s)
	k := liveKey()
	t.Cleanup(func() { _ = w.Delete(context.WithoutCancel(t.Context()), k) })

	body := make([]byte, 256)
	for i := range body {
		body[i] = byte(i)
	}

	if err := w.Put(t.Context(), k, body); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(t.Context(), k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("Get returned %d bytes, want %d", len(got), len(body))
	}
	for i := range body {
		if got[i] != body[i] {
			t.Fatalf("byte %d is %#x, want %#x", i, got[i], body[i])
		}
	}
}
