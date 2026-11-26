package filesystem

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

// A streamed write reads back whole, streamed or not.
func TestAStreamedObjectRoundTrips(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	want := bytes.Repeat([]byte("abc"), 1<<20)
	if err := asWriter(t, s).PutStream(t.Context(), key, bytes.NewReader(want)); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	r, err := s.Open(t.Context(), key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, want) {
		t.Errorf("Open read %d bytes, %v", len(got), err)
	}
}

// A missing object opens as ErrNotFound, and an unsafe key is refused.
func TestOpenOfAMissingOrUnsafeKey(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	if _, err := s.Open(t.Context(), key); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("Open of a missing key = %v, want ErrNotFound", err)
	}
	if _, err := s.Open(t.Context(), "../escape"); !errors.Is(err, errUnsafeKey) {
		t.Errorf("Open of an unsafe key = %v, want errUnsafeKey", err)
	}
}

// A reader that fails mid-way leaves the object as it was, and no temporary
// file beside it.
func TestAFailedStreamLeavesTheOldObjectAndNoDebris(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	w := asWriter(t, s)
	if err := w.Put(t.Context(), key, []byte("before")); err != nil {
		t.Fatal(err)
	}
	broke := errors.New("the room hung up")
	r := io.MultiReader(bytes.NewReader(make([]byte, 1<<20)), failing{broke})
	if err := w.PutStream(t.Context(), key, r); !errors.Is(err, broke) {
		t.Fatalf("PutStream = %v, want %v", err, broke)
	}
	if got, err := s.Get(t.Context(), key); err != nil || string(got) != "before" {
		t.Errorf("after a failed stream the object is %q, %v", got, err)
	}
	var debris []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-") {
			debris = append(debris, path)
		}
		return nil
	})
	if len(debris) > 0 {
		t.Errorf("a failed stream left %v", debris)
	}
}

type failing struct{ err error }

func (f failing) Read([]byte) (int, error) { return 0, f.err }
