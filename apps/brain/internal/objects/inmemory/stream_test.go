package inmemory

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

func TestAStreamedObjectRoundTrips(t *testing.T) {
	t.Parallel()

	s := New()
	if _, err := s.Open(t.Context(), "k"); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("Open of a missing key = %v, want ErrNotFound", err)
	}
	if err := asWriter(t, s).PutStream(t.Context(), "k", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	r, err := s.Open(t.Context(), "k")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, _ := io.ReadAll(r); string(got) != "hello" {
		t.Errorf("Open read %q", got)
	}
}
