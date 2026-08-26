package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

func asWriter(t *testing.T, s objects.Store) objects.Writer {
	t.Helper()

	w, ok := s.(objects.Writer)
	if !ok {
		t.Fatalf("%T does not implement objects.Writer", s)
	}
	return w
}

func newStore(t *testing.T) (objects.Store, string) {
	t.Helper()

	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatalf("New(%q): %v", root, err)
	}
	return s, root
}

const key = "teams/11111111-1111-1111-1111-111111111111/objects/abc"

func TestRoundTrips(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	if err := asWriter(t, s).Put(t.Context(), key, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Get = %q, want %q", got, "hello")
	}
}

// The key has directories in it that will not exist yet. A store that refuses
// the first write to a team is a store that never accepts one.
func TestPutCreatesTheParentDirectories(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	if err := asWriter(t, s).Put(t.Context(), key, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(key))); err != nil {
		t.Errorf("the object is not on disk where the key says: %v", err)
	}
}

func TestMissingKeyIsNotFound(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	if _, err := s.Get(t.Context(), key); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("err = %v, want objects.ErrNotFound", err)
	}
}

// A directory in place of an object is not a missing object and not a
// readable one. It has to be an error, and it must not read as ErrNotFound —
// answering "no such attachment" for a store that is misarranged hides it.
func TestADirectoryIsNotAnObject(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(key)), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := s.Get(t.Context(), key)
	if err == nil {
		t.Fatal("reading a directory as an object returned no error")
	}
	if errors.Is(err, objects.ErrNotFound) {
		t.Errorf("a directory was reported as a missing object: %v", err)
	}
}

func TestDeleteRemovesTheObject(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	if err := asWriter(t, s).Put(t.Context(), key, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := asWriter(t, s).Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(t.Context(), key); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want objects.ErrNotFound", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	if err := asWriter(t, s).Delete(t.Context(), key); err != nil {
		t.Errorf("Delete of a missing key: %v", err)
	}
}

// The one that matters. A key is read from a database row, and this adapter
// turns it into a filesystem path — so a key that escapes the root, names the
// root itself, or maps to a path some other key also maps to, has to be
// refused before anything touches the disk.
func TestUnsafeKeysAreRefusedAndTouchNothing(t *testing.T) {
	t.Parallel()

	for name, bad := range map[string]string{
		"climbs out":             "../escaped",
		"climbs out and back in": "teams/a/../../escaped",
		"absolute":               "/etc/passwd",
		"empty":                  "",
		"the root itself":        ".",
		"trailing slash":         "teams/a/objects/abc/",
		"doubled separator":      "teams//a/objects/abc",
		"a dot segment":          "teams/./a/objects/abc",
		"a NUL byte":             "teams/a/objects/a\x00b",
		"climbs out repeatedly":  "../../../../../../etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, root := newStore(t)
			sibling := filepath.Join(filepath.Dir(root), "sibling")
			if err := os.MkdirAll(sibling, 0o750); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			if err := asWriter(t, s).Put(t.Context(), bad, []byte("owned")); err == nil {
				t.Errorf("Put(%q) was accepted", bad)
			}
			if _, err := s.Get(t.Context(), bad); err == nil {
				t.Errorf("Get(%q) was accepted", bad)
			}
			if err := asWriter(t, s).Delete(t.Context(), bad); err == nil {
				t.Errorf("Delete(%q) was accepted", bad)
			}

			// Refusing is not enough — nothing may have been written on the
			// way to refusing, inside the root or outside it.
			if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
				t.Errorf("Put(%q) left %d entries under the root", bad, len(entries))
			}
			if entries, err := os.ReadDir(sibling); err == nil && len(entries) > 0 {
				t.Errorf("Put(%q) wrote outside the root", bad)
			}
		})
	}
}

// A backslash is a separator on Windows and an ordinary character here, so
// `..\escaped` is one filename rather than a climb out of the root. The brain
// runs on Linux, so the claim worth asserting is containment, not refusal —
// and asserting refusal would be asserting a property this build does not
// have. A Windows port would need filepath.IsLocal to do that work, which it
// would: it is separator-aware per platform.
func TestABackslashIsAFilenameNotATraversal(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	const backslash = `..\escaped`

	if err := asWriter(t, s).Put(t.Context(), backslash, []byte("contained")); err != nil {
		t.Fatalf("Put(%q): %v", backslash, err)
	}

	outside := filepath.Join(filepath.Dir(root), "escaped")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("Put(%q) wrote outside the root, at %s", backslash, outside)
	}
	if _, err := os.Stat(filepath.Join(root, backslash)); err != nil {
		t.Errorf("Put(%q) did not write inside the root: %v", backslash, err)
	}
}

// A refused key must be refused as a bad key, not as a missing object: the
// read path turns ErrNotFound into a 404, and a traversal attempt answered
// with 404 is indistinguishable from a typo in the logs.
func TestAnUnsafeKeyIsNotReportedAsMissing(t *testing.T) {
	t.Parallel()

	s, _ := newStore(t)
	_, err := s.Get(t.Context(), "../escaped")
	if errors.Is(err, objects.ErrNotFound) {
		t.Errorf("a traversal attempt was reported as a missing object: %v", err)
	}
}

// Overwriting has to leave the object readable throughout — no window where a
// concurrent reader sees a half-written file, and no temporary files left
// lying in the tree afterwards.
func TestPutOverwritesWithoutLeavingDebris(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	w := asWriter(t, s)

	for _, body := range []string{"first", "second", "third"} {
		if err := w.Put(t.Context(), key, []byte(body)); err != nil {
			t.Fatalf("Put(%q): %v", body, err)
		}
	}

	got, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "third" {
		t.Errorf("Get = %q, want %q", got, "third")
	}

	var found []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = append(found, strings.TrimPrefix(path, root))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("after three writes the tree holds %d files, want 1: %v", len(found), found)
	}
}

// The root has to exist and be a directory. Pointing this at a file, or at a
// path that cannot be created, is a configuration mistake worth failing at
// startup rather than on the first attachment.
func TestNewRejectsARootThatIsNotADirectory(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := New(file); err == nil {
		t.Error("New accepted a root that is a regular file")
	}
}

func TestNewCreatesAMissingRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "does", "not", "exist")
	if _, err := New(root); err != nil {
		t.Fatalf("New(%q): %v", root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("New did not create the root as a directory")
	}
}

// An object holds a signing secret's neighbours — attachments people sent
// privately. Group and other must not be able to read them off the volume.
func TestObjectsAreNotWorldReadable(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	if err := asWriter(t, s).Put(t.Context(), key, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(key)))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("object mode is %04o, want nothing for group or other", perm)
	}
}

// Atomicity, asked deterministically rather than by racing a reader.
//
// A hard link is a second name for the same inode. Put writes a temporary
// file and renames it over the destination, which unlinks the old inode and
// leaves the link holding the old bytes. An implementation that opened the
// destination and wrote through it would share the inode, and the link would
// show the new bytes — which is the same reason a concurrent reader would see
// a half-written file.
func TestPutReplacesTheFileRatherThanWritingThroughIt(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	w := asWriter(t, s)

	if err := w.Put(t.Context(), key, []byte("first")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	name := filepath.Join(root, filepath.FromSlash(key))
	link := name + ".link"
	if err := os.Link(name, link); err != nil {
		t.Fatalf("Link: %v", err)
	}

	if err := w.Put(t.Context(), key, []byte("second")); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	held, err := os.ReadFile(link)
	if err != nil {
		t.Fatalf("reading the link: %v", err)
	}
	if string(held) != "first" {
		t.Errorf("the old inode now holds %q — Put wrote through the destination "+
			"instead of replacing it, so a concurrent reader can see a partial object", held)
	}
}

// The temporary file only matters when the write fails: on success the rename
// moves it. Renaming onto a non-empty directory fails, which is a real state —
// it is what TestADirectoryIsNotAnObject describes — and the tree must not be
// left holding a .tmp- file that a later walk would count as an object.
func TestAFailedPutLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()

	s, root := newStore(t)
	occupied := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Join(occupied, "child"), dirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := asWriter(t, s).Put(t.Context(), key, []byte("hello")); err == nil {
		t.Fatal("Put over a non-empty directory returned no error")
	}

	var debris []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-") {
			debris = append(debris, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	if len(debris) > 0 {
		t.Errorf("a failed Put left %d temporary file(s): %v", len(debris), debris)
	}
}
