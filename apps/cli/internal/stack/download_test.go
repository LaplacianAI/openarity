package stack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

// A jar holding one txz holding a tree, which is the shape the Postgres
// artifacts actually have. Built here rather than committed: a fixture that
// large would dwarf the repository.
func postgresJar(t *testing.T) []byte {
	t.Helper()

	var tarred bytes.Buffer
	tw := tar.NewWriter(&tarred)
	for _, f := range []struct{ name, body string }{
		{"bin/postgres", "#!/bin/sh\nexit 0\n"},
		{"bin/initdb", "#!/bin/sh\nexit 0\n"},
		{"share/postgresql.conf.sample", "# sample\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body))}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}

	var compressed bytes.Buffer
	xw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("xz writer: %v", err)
	}
	if _, err := xw.Write(tarred.Bytes()); err != nil {
		t.Fatalf("xz write: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("closing xz: %v", err)
	}

	var jar bytes.Buffer
	zw := zip.NewWriter(&jar)
	// The inner name varies per platform — postgres-darwin-arm_64.txz,
	// postgres-linux-x86_64.txz — so nothing may depend on knowing it.
	w, err := zw.Create("postgres-someplatform-weird_64.txz")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(compressed.Bytes()); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return jar.Bytes()
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// serve returns a server handing out body at /artifact and its digest at
// /artifact.sha256, and a counter of how many times the body was fetched.
func serve(t *testing.T, body []byte) (*httptest.Server, *int) {
	t.Helper()

	fetches := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/artifact.sha256", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(digest(body) + "  artifact\n"))
	})
	mux.HandleFunc("/artifact", func(w http.ResponseWriter, r *http.Request) {
		fetches++
		http.ServeContent(w, r, "artifact", zeroTime, bytes.NewReader(body))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &fetches
}

func TestADownloadIsVerifiedAndExtracted(t *testing.T) {
	t.Parallel()

	body := postgresJar(t)
	server, _ := serve(t, body)
	dest := t.TempDir()

	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err != nil {
		t.Fatalf("Postgres() = %v", err)
	}

	for _, name := range []string{"bin/postgres", "bin/initdb", "share/postgresql.conf.sample"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was not extracted: %v", name, err)
		}
	}
}

// A binary that is not executable fails at exec time with a permission error
// several layers from the thing that is wrong, which LocalFinder already
// refuses. The archive carries the mode; the extractor has to honour it.
func TestExtractedBinariesKeepTheirExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the executable bit is not how Windows decides")
	}
	t.Parallel()

	server, _ := serve(t, postgresJar(t))
	dest := t.TempDir()

	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err != nil {
		t.Fatalf("Postgres() = %v", err)
	}

	info, err := os.Stat(filepath.Join(dest, "bin", "postgres"))
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("the extracted postgres has no executable bit")
	}
}

// The guard. A corrupted download that is silently accepted becomes a cluster
// that will not start, hours later, for a reason nothing connects to the
// download.
func TestAWrongChecksumIsRefusedAndLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	body := postgresJar(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/artifact.sha256", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 64) + "\n"))
	})
	mux.HandleFunc("/artifact", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "artifact", zeroTime, bytes.NewReader(body))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dest := t.TempDir()
	d := &Downloader{Client: server.Client()}

	err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest)
	if err == nil {
		t.Fatal("Postgres() with a wrong checksum = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("Postgres() = %q, want it to say what failed", err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused download left %d entries behind: %v", len(entries), entries)
	}
}

// A tar entry naming ../ or an absolute path writes outside the directory it
// was asked to write into. The archives are third-party, so this is not
// hypothetical politeness.
func TestAnArchiveCannotWriteOutsideItsDestination(t *testing.T) {
	t.Parallel()

	var tarred bytes.Buffer
	tw := tar.NewWriter(&tarred)
	body := "owned"
	if err := tw.WriteHeader(&tar.Header{Name: "../escaped", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}

	var compressed bytes.Buffer
	xw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("xz writer: %v", err)
	}
	if _, err := xw.Write(tarred.Bytes()); err != nil {
		t.Fatalf("xz write: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("closing xz: %v", err)
	}

	var jar bytes.Buffer
	zw := zip.NewWriter(&jar)
	w, err := zw.Create("postgres-evil.txz")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(compressed.Bytes()); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}

	server, _ := serve(t, jar.Bytes())
	parent := t.TempDir()
	dest := filepath.Join(parent, "into")

	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err == nil {
		t.Fatal("an archive escaping its destination was accepted")
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped")); err == nil {
		t.Error("the archive wrote a file outside its destination")
	}
}

// Downloading 68MB again because the command was run twice is the difference
// between a setup that resumes and one that starts over.
func TestAnAlreadyExtractedDownloadIsNotFetchedAgain(t *testing.T) {
	t.Parallel()

	server, fetches := serve(t, postgresJar(t))
	dest := t.TempDir()

	d := &Downloader{Client: server.Client()}
	for range 2 {
		if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err != nil {
			t.Fatalf("Postgres() = %v", err)
		}
	}
	if *fetches != 1 {
		t.Errorf("the artifact was fetched %d times, want 1", *fetches)
	}
}

func TestASingleBinaryIsDownloadedAndMadeExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the executable bit is not how Windows decides")
	}
	t.Parallel()

	body := []byte("#!/bin/sh\nexit 0\n")
	server, _ := serve(t, body)
	dest := filepath.Join(t.TempDir(), "dex")

	d := &Downloader{Client: server.Client()}
	if err := d.Binary(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err != nil {
		t.Fatalf("Binary() = %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("the downloaded binary has no executable bit")
	}
}

func TestAMissingArtifactIsReportedWithItsURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	d := &Downloader{Client: server.Client()}
	err := d.Binary(t.Context(), server.URL+"/nowhere", server.URL+"/nowhere.sha256", filepath.Join(t.TempDir(), "dex"))
	if err == nil {
		t.Fatal("Binary() on a 404 = nil, want an error")
	}
	if !strings.Contains(err.Error(), server.URL) {
		t.Errorf("Binary() = %q, want it to name the URL", err)
	}
}

// realDir is t.TempDir() with every symlink in it resolved, so a test asserts
// what the extractor does rather than what the platform's temporary directory
// happens to be.
func realDir(t *testing.T, dir string) string {
	t.Helper()

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving %s: %v", dir, err)
	}
	return real
}

// An entry, and what it holds. tar.Header carries no body, so a test that
// wants both has to pair them.
type entry struct {
	header tar.Header
	body   string
}

// jarOf builds the shape Postgres arrives in — a jar holding one txz holding
// a tree — from entries a test declares. The long-hand version of this is
// forty lines, which is why the two tests written before it inlined it and the
// third did not.
func jarOf(t *testing.T, entries ...entry) []byte {
	t.Helper()

	var tarred bytes.Buffer
	tw := tar.NewWriter(&tarred)
	for _, e := range entries {
		e.header.Size = int64(len(e.body))
		if err := tw.WriteHeader(&e.header); err != nil {
			t.Fatalf("tar header %s: %v", e.header.Name, err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatalf("tar body %s: %v", e.header.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}

	var compressed bytes.Buffer
	xw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("xz writer: %v", err)
	}
	if _, err := xw.Write(tarred.Bytes()); err != nil {
		t.Fatalf("xz write: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("closing xz: %v", err)
	}

	var jar bytes.Buffer
	zw := zip.NewWriter(&jar)
	w, err := zw.Create("postgres-someplatform-weird_64.txz")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(compressed.Bytes()); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return jar.Bytes()
}

// Two links and a file, none of which escapes on its own.
//
// `subdir/parent` points at `..`, which is the extraction root — inside it, so
// a check that resolves the link's *parent* and then cleans the target
// lexically allows it. `escape` then points at `subdir/parent/../..`, and the
// same lexical clean collapses `parent/..` back to `subdir`, so that target
// reads as the root too. On disk it is not: `subdir/parent` really is a
// symlink, so the kernel resolves the same string two directories above the
// root, and the file written through it lands there.
//
// This is the vulnerability CodeQL's own help text describes, and the shape of
// check above is what a machine-generated fix produced for it. The guard that
// holds is narrower and needs no resolution at all: a link may name something
// in its own directory and nothing else. Every symlink in every Postgres
// archive is a bare filename — 14 of them, all in lib/, all pointing at a
// sibling .so — so nothing legitimate is lost by refusing the rest.
func TestASymlinkChainCannotEscapeTheDestination(t *testing.T) {
	t.Parallel()

	jar := jarOf(t,
		entry{header: tar.Header{Name: "subdir", Typeflag: tar.TypeDir, Mode: 0o700}},
		entry{header: tar.Header{Name: "subdir/parent", Typeflag: tar.TypeSymlink, Linkname: ".."}},
		entry{header: tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "subdir/parent/../.."}},
		entry{header: tar.Header{Name: "escape/escaped", Typeflag: tar.TypeReg, Mode: 0o644}, body: "owned"},
	)

	// Three deep, so the two levels the chain climbs are still inside the
	// temporary directory and the test cannot write anywhere real.
	//
	// Resolved, because on macOS t.TempDir() sits under /var, which is itself
	// a symlink to /private/var. A check comparing an unresolved root against
	// a resolved target refuses every symlink there — including the real
	// lib/libpq.so — so an unresolved root would pass this test on macOS for a
	// reason that has nothing to do with the guard, and go on passing after
	// the guard was removed.
	top := realDir(t, t.TempDir())
	parent := filepath.Join(top, "one", "two")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("making the destination's parent: %v", err)
	}
	dest := filepath.Join(parent, "into")

	server, _ := serve(t, jar)
	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err == nil {
		t.Error("an archive whose symlinks escape its destination was accepted")
	}

	for _, dir := range []string{top, filepath.Join(top, "one"), parent} {
		if _, err := os.Stat(filepath.Join(dir, "escaped")); err == nil {
			t.Errorf("the archive wrote a file outside its destination, at %s", dir)
		}
	}
}

// The same chain with the file entry removed, which is what isolates the guard
// on the link's own name.
//
// Nothing is ever written through these links, so the check on a resolved
// parent directory never sees a bad parent and never fires. Only the refusal
// of a link that names anything but a sibling stops `escape` being created
// pointing two directories above the root — and an install directory holding
// a symlink to somewhere outside it is a finding whether or not this archive
// went on to use it.
func TestASymlinkThatEscapesIsRefusedEvenWithNothingWrittenThroughIt(t *testing.T) {
	t.Parallel()

	jar := jarOf(t,
		entry{header: tar.Header{Name: "subdir", Typeflag: tar.TypeDir, Mode: 0o700}},
		entry{header: tar.Header{Name: "subdir/parent", Typeflag: tar.TypeSymlink, Linkname: ".."}},
		entry{header: tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "subdir/parent/../.."}},
	)

	dest := filepath.Join(realDir(t, t.TempDir()), "into")
	server, _ := serve(t, jar)

	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err == nil {
		t.Error("an archive whose symlinks point outside its destination was accepted")
	}

	if link := filepath.Join(dest, "escape"); exists(link) {
		real, err := filepath.EvalSymlinks(link)
		t.Errorf("the archive left %s behind, resolving to %s (%v)", link, real, err)
	}
}

// The second guard, reached directly: no archive can trigger it once links are
// held to their own directory, so nothing else in this file covers it. It is
// what would catch a traversal arriving some other way — a future entry type,
// or a link the guard above is one day widened to allow.
func TestADirectoryReachedThroughALinkIsNotInsideTheRoot(t *testing.T) {
	t.Parallel()

	root := realDir(t, t.TempDir())
	outside := realDir(t, t.TempDir())

	into := &extraction{root: root, verified: map[string]bool{}}

	inside := filepath.Join(root, "real")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatalf("making %s: %v", inside, err)
	}
	if err := into.dirWithinRoot(inside); err != nil {
		t.Errorf("dirWithinRoot(%s) = %v, want a real directory inside the root accepted", inside, err)
	}
	if err := into.dirWithinRoot(root); err != nil {
		t.Errorf("dirWithinRoot(root) = %v, want the root itself accepted", err)
	}

	// Named inside the root, located outside it. The string says one thing and
	// the kernel says another, which is the whole reason this check resolves.
	link := filepath.Join(root, "looks-inside")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("linking %s: %v", link, err)
	}
	if err := into.dirWithinRoot(link); err == nil {
		t.Error("a directory reached through a link out of the root was accepted")
	}

	// The cache must not turn a refusal into an acceptance on the second ask,
	// nor an acceptance into work done twice.
	if err := into.dirWithinRoot(link); err == nil {
		t.Error("a second ask about the same escaping link was accepted")
	}
	if err := into.dirWithinRoot(inside); err != nil {
		t.Errorf("a second ask about a directory inside the root = %v", err)
	}
}

// The refusal above must not take the real archives with it. Every Linux
// Postgres build ships lib/libpq.so -> libpq.so.5.18 and thirteen more like
// it, and an extractor that dropped those would produce an install that
// cannot start.
func TestASymlinkToASiblingIsExtracted(t *testing.T) {
	t.Parallel()

	jar := jarOf(t,
		entry{header: tar.Header{Name: "lib", Typeflag: tar.TypeDir, Mode: 0o700}},
		entry{header: tar.Header{Name: "lib/libpq.so.5.18", Typeflag: tar.TypeReg, Mode: 0o644}, body: "not really an elf"},
		entry{header: tar.Header{Name: "lib/libpq.so", Typeflag: tar.TypeSymlink, Linkname: "libpq.so.5.18"}},
	)

	dest := filepath.Join(realDir(t, t.TempDir()), "into")
	server, _ := serve(t, jar)

	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err != nil {
		t.Fatalf("Postgres() = %v, want the archive accepted", err)
	}

	link := filepath.Join(dest, "lib", "libpq.so")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink(%s) = %v", link, err)
	}
	if target != "libpq.so.5.18" {
		t.Errorf("Readlink(%s) = %q, want the sibling it names", link, target)
	}

	// Followed, not merely present: a link recorded with the wrong target
	// reads the same until something opens it.
	if _, err := os.ReadFile(link); err != nil {
		t.Errorf("reading through the link = %v", err)
	}
}

// Maven Central rate-limits by address, and a CI runner shares one with
// everything else on that machine — three jobs pulling a 71MB jar produced a
// 429 on the checksum URL, which read like the artifact was missing. A person
// on a shared connection sees the same thing.
func TestARateLimitIsWaitedOutRatherThanReported(t *testing.T) {
	t.Parallel()

	body := []byte("#!/bin/sh\nexit 0\n")
	var seen int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&seen, 1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(digest(body)))
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	dest := filepath.Join(t.TempDir(), "dex")
	d := &Downloader{Client: server.Client(), Backoff: time.Millisecond}
	if err := d.Binary(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err != nil {
		t.Fatalf("Binary() = %v, want the download to survive two 429s", err)
	}
	if got := atomic.LoadInt32(&seen); got < 3 {
		t.Errorf("the server saw %d requests, want the two refusals and then the real one", got)
	}
}

// The other half of the same guard. A 404 is an answer: retrying it four times
// turns a wrong URL into half a minute of silence before the same message.
func TestAMissingArtifactIsNotRetried(t *testing.T) {
	t.Parallel()

	var seen int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&seen, 1)
		http.Error(w, "nothing here", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	d := &Downloader{Client: server.Client()}
	err := d.Binary(t.Context(), server.URL+"/nowhere", server.URL+"/nowhere.sha256", filepath.Join(t.TempDir(), "dex"))
	if err == nil {
		t.Fatal("Binary() on a 404 = nil, want an error")
	}
	if got := atomic.LoadInt32(&seen); got != 1 {
		t.Errorf("the server saw %d requests, want exactly one — a 404 is not a wait", got)
	}
}

// A rate limit that never lifts must end as an error rather than as a command
// that appears to hang.
func TestARateLimitThatNeverLiftsGivesUp(t *testing.T) {
	t.Parallel()

	var seen int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&seen, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	d := &Downloader{Client: server.Client(), Backoff: time.Millisecond}
	err := d.Binary(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", filepath.Join(t.TempDir(), "dex"))
	if err == nil {
		t.Fatal("Binary() against a permanent 429 = nil, want an error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("Binary() = %q, want it to name the status it gave up on", err)
	}
	if got := atomic.LoadInt32(&seen); got != fetchAttempts {
		t.Errorf("the server saw %d requests, want %d", got, fetchAttempts)
	}
}

// Retry-After is a promise from the server about when it will answer. Ignoring
// it and using the backoff instead is how a client that means to be polite
// hammers a service that asked it not to.
func TestRetryAfterIsHonouredAndCapped(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{header: "5", want: 5 * time.Second},
		{header: "3600", want: longestWait},
		{header: "-1", want: 0},
		{header: "Wed, 21 Oct 2026 07:28:00 GMT", want: 0},
		{header: "", want: 0},
	} {
		if got := retryAfter(tc.header); got != tc.want {
			t.Errorf("retryAfter(%q) = %s, want %s", tc.header, got, tc.want)
		}
	}
}

// A directory entry is created before anything can resolve where it landed,
// so the check on the entry's own path is the only thing standing between
// `../pwned` and a directory made outside the extraction root. The refusal
// that follows would be too late: the mkdir has already happened.
func TestADirectoryEntryCannotBeCreatedOutsideTheDestination(t *testing.T) {
	t.Parallel()

	jar := jarOf(t,
		entry{header: tar.Header{Name: "../pwned", Typeflag: tar.TypeDir, Mode: 0o700}},
	)

	parent := filepath.Join(realDir(t, t.TempDir()), "one")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("making %s: %v", parent, err)
	}
	dest := filepath.Join(parent, "into")

	server, _ := serve(t, jar)
	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err == nil {
		t.Error("an archive with a directory entry outside its destination was accepted")
	}

	if _, err := os.Stat(filepath.Join(parent, "pwned")); err == nil {
		t.Errorf("the archive made a directory outside its destination, at %s", filepath.Join(parent, "pwned"))
	}
}

// `tar -cf archive.tar .` writes an entry for the root itself. None of the
// Postgres archives carries one — checked, not assumed — but refusing an
// archive over a harmless "." would be a strange way to find that out if one
// ever did.
func TestAnEntryNamingTheRootItselfIsAccepted(t *testing.T) {
	t.Parallel()

	jar := jarOf(t,
		entry{header: tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0o700}},
		entry{header: tar.Header{Name: "./bin", Typeflag: tar.TypeDir, Mode: 0o700}},
		entry{header: tar.Header{Name: "./bin/postgres", Typeflag: tar.TypeReg, Mode: 0o755}, body: "not really a binary"},
	)

	dest := filepath.Join(realDir(t, t.TempDir()), "into")
	server, _ := serve(t, jar)

	d := &Downloader{Client: server.Client()}
	if err := d.Postgres(t.Context(), server.URL+"/artifact", server.URL+"/artifact.sha256", dest); err != nil {
		t.Fatalf("Postgres() = %v, want an archive with a root entry accepted", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "postgres")); err != nil {
		t.Errorf("the archive's contents did not arrive: %v", err)
	}
}

// serveNamed answers for one archive under its own name, because Unpack picks
// tar or zip from the extension — an artefact called "artifact" would always
// be read as a tar.
func serveNamed(t *testing.T, name string, body []byte) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/"+name+".sha256", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(digest(body) + "  " + name + "\n"))
	})
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, name, zeroTime, bytes.NewReader(body))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// A gzipped tar wrapping everything in one directory, which is how both uv and
// Node ship.
func tarball(t *testing.T, entries ...entry) []byte {
	t.Helper()

	var tarred bytes.Buffer
	tw := tar.NewWriter(&tarred)
	for _, e := range entries {
		e.header.Size = int64(len(e.body))
		if err := tw.WriteHeader(&e.header); err != nil {
			t.Fatalf("tar header %s: %v", e.header.Name, err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatalf("tar body %s: %v", e.header.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}

	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(tarred.Bytes()); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
	return out.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return out.Bytes()
}

// Node's tarball is node-v24.20.0-darwin-arm64/bin/node, and the caller wants
// bin/node. Stripping here is what stops the version being known twice — once
// in the URL and again in every path built from the result.
func TestUnpackStripsTheWrappingDirectory(t *testing.T) {
	t.Parallel()

	body := tarball(t,
		entry{header: tar.Header{Name: "node-v24.20.0-darwin-arm64", Typeflag: tar.TypeDir, Mode: 0o755}},
		entry{header: tar.Header{Name: "node-v24.20.0-darwin-arm64/bin", Typeflag: tar.TypeDir, Mode: 0o755}},
		entry{header: tar.Header{Name: "node-v24.20.0-darwin-arm64/bin/node", Typeflag: tar.TypeReg, Mode: 0o755}, body: "not really node"},
		entry{header: tar.Header{Name: "node-v24.20.0-darwin-arm64/README.md", Typeflag: tar.TypeReg, Mode: 0o644}, body: "hello"},
	)

	server := serveNamed(t, "node.tar.gz", body)
	dest := filepath.Join(realDir(t, t.TempDir()), "node")

	d := &Downloader{Client: server.Client()}
	err := d.Unpack(t.Context(), Archive{
		URL: server.URL + "/node.tar.gz", ChecksumURL: server.URL + "/node.tar.gz.sha256",
		Dest: dest, Strip: 1,
	})
	if err != nil {
		t.Fatalf("Unpack() = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "bin", "node")); err != nil {
		t.Errorf("bin/node is not where the caller was told it would be: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "node-v24.20.0-darwin-arm64")); err == nil {
		t.Error("the wrapping directory survived, so every path below it carries the version")
	}
}

// The executable bit is what the finder checks before it will run anything, so
// an unpacked runtime that lost it is a runtime that cannot start.
func TestUnpackKeepsTheExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the executable bit is not how Windows decides")
	}
	t.Parallel()

	body := tarball(t,
		entry{header: tar.Header{Name: "uv-aarch64-apple-darwin/uv", Typeflag: tar.TypeReg, Mode: 0o755}, body: "#!/bin/sh\nexit 0\n"},
	)

	server := serveNamed(t, "uv.tar.gz", body)
	dest := filepath.Join(realDir(t, t.TempDir()), "uv")

	d := &Downloader{Client: server.Client()}
	if err := d.Unpack(t.Context(), Archive{
		URL: server.URL + "/uv.tar.gz", ChecksumURL: server.URL + "/uv.tar.gz.sha256",
		Dest: dest, Strip: 1,
	}); err != nil {
		t.Fatalf("Unpack() = %v", err)
	}

	info, err := os.Stat(filepath.Join(dest, "uv"))
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("the unpacked binary has no executable bit")
	}
}

// Windows gets a zip from both publishers rather than a tar.
func TestUnpackReadsAZip(t *testing.T) {
	t.Parallel()

	body := zipOf(t, map[string]string{
		"node-v24.20.0-win-x64/node.exe": "not really node",
		"node-v24.20.0-win-x64/npm.cmd":  "not really npm",
		"node-v24.20.0-win-x64/LICENSE":  "a licence",
	})

	server := serveNamed(t, "node.zip", body)
	dest := filepath.Join(realDir(t, t.TempDir()), "node")

	d := &Downloader{Client: server.Client()}
	if err := d.Unpack(t.Context(), Archive{
		URL: server.URL + "/node.zip", ChecksumURL: server.URL + "/node.zip.sha256",
		Dest: dest, Strip: 1,
	}); err != nil {
		t.Fatalf("Unpack() = %v", err)
	}

	for _, name := range []string{"node.exe", "npm.cmd", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("%s is missing: %v", name, err)
		}
	}
}

// A zip escaping its destination is the same attack as a tar doing it, and the
// guard has to be on both paths rather than only the one that was written
// first.
func TestAZipCannotWriteOutsideItsDestination(t *testing.T) {
	t.Parallel()

	body := zipOf(t, map[string]string{"../escaped": "owned"})

	server := serveNamed(t, "evil.zip", body)
	parent := realDir(t, t.TempDir())
	dest := filepath.Join(parent, "into")

	d := &Downloader{Client: server.Client()}
	if err := d.Unpack(t.Context(), Archive{
		URL: server.URL + "/evil.zip", ChecksumURL: server.URL + "/evil.zip.sha256", Dest: dest,
	}); err == nil {
		t.Error("a zip escaping its destination was accepted")
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped")); err == nil {
		t.Error("the zip wrote a file outside its destination")
	}
}

// Node publishes one SHASUMS256.txt for a whole release. Taking the first
// field there verifies the darwin-arm64 tarball against the hash of whatever
// is listed first, which is to say against nothing.
func TestAChecksumIsFoundInAManifestCoveringManyFiles(t *testing.T) {
	t.Parallel()

	body := tarball(t, entry{header: tar.Header{Name: "node-v24.20.0-linux-x64/bin/node", Typeflag: tar.TypeReg, Mode: 0o755}, body: "not really node"})
	want := "node-v24.20.0-linux-x64.tar.gz"

	mux := http.NewServeMux()
	mux.HandleFunc("/SHASUMS256.txt", func(w http.ResponseWriter, _ *http.Request) {
		// The real file lists thirty-odd builds; the one we want is never
		// first, and the hashes of the others do not match this body.
		_, _ = w.Write([]byte(
			"0000000000000000000000000000000000000000000000000000000000000000  node-v24.20.0-aix-ppc64.tar.gz\n" +
				"1111111111111111111111111111111111111111111111111111111111111111  node-v24.20.0-darwin-arm64.tar.gz\n" +
				digest(body) + "  " + want + "\n" +
				"2222222222222222222222222222222222222222222222222222222222222222  node-v24.20.0-win-x64.zip\n"))
	})
	mux.HandleFunc("/"+want, func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, want, zeroTime, bytes.NewReader(body))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dest := filepath.Join(realDir(t, t.TempDir()), "node")
	d := &Downloader{Client: server.Client()}

	if err := d.Unpack(t.Context(), Archive{
		URL: server.URL + "/" + want, ChecksumURL: server.URL + "/SHASUMS256.txt",
		ChecksumName: want, Dest: dest, Strip: 1,
	}); err != nil {
		t.Fatalf("Unpack() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "node")); err != nil {
		t.Errorf("the archive did not arrive: %v", err)
	}
}

// A manifest that does not mention the file being fetched is a refusal, not a
// reason to carry on unverified.
func TestAChecksumMissingFromTheManifestIsRefused(t *testing.T) {
	t.Parallel()

	body := tarball(t, entry{header: tar.Header{Name: "x/y", Typeflag: tar.TypeReg, Mode: 0o644}, body: "hello"})

	mux := http.NewServeMux()
	mux.HandleFunc("/SHASUMS256.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  something-else.tar.gz\n"))
	})
	mux.HandleFunc("/wanted.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "wanted.tar.gz", zeroTime, bytes.NewReader(body))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	err := (&Downloader{Client: server.Client()}).Unpack(t.Context(), Archive{
		URL: server.URL + "/wanted.tar.gz", ChecksumURL: server.URL + "/SHASUMS256.txt",
		ChecksumName: "wanted.tar.gz", Dest: filepath.Join(t.TempDir(), "out"),
	})
	if err == nil {
		t.Fatal("Unpack() with no checksum for the file = nil, want a refusal")
	}
	// A missing entry and a wrong hash are both refusals, so naming the file
	// does not distinguish them — written that way, this passed against a
	// version that returned a zero hash and let the mismatch do the refusing.
	// The message is the thing being tested: "the manifest does not cover
	// this file" and "the file is not what the manifest says" send a person
	// to different places.
	if !strings.Contains(err.Error(), "lists no checksum") {
		t.Errorf("Unpack() = %q, want it to say the manifest does not cover the file", err)
	}
}

// An archive already unpacked is not fetched again. Node and its packages are
// hundreds of megabytes, so running setup twice must not pay for them twice.
func TestAnUnpackedArchiveIsNotFetchedAgain(t *testing.T) {
	t.Parallel()

	body := tarball(t, entry{header: tar.Header{Name: "w/bin/node", Typeflag: tar.TypeReg, Mode: 0o755}, body: "not really node"})
	server := serveNamed(t, "node.tar.gz", body)
	dest := filepath.Join(realDir(t, t.TempDir()), "node")

	d := &Downloader{Client: server.Client()}
	a := Archive{URL: server.URL + "/node.tar.gz", ChecksumURL: server.URL + "/node.tar.gz.sha256", Dest: dest, Strip: 1}

	if err := d.Unpack(t.Context(), a); err != nil {
		t.Fatalf("first Unpack() = %v", err)
	}

	// The server is closed, so a second fetch cannot succeed. If Unpack
	// returns anyway, it did not try.
	server.Close()
	if err := d.Unpack(t.Context(), a); err != nil {
		t.Errorf("second Unpack() = %v, want it to notice the archive is already there", err)
	}
}

// The zip counterpart of the directory case. A directory is created before
// anything can resolve where it landed, so the check on the entry's own path
// is the only thing between "../pwned" and a directory outside the
// destination — the refusal that follows would be too late.
//
// Written first as a file entry, where it passed with the guard removed:
// resolving the parent caught it instead, and the guard under test was never
// reached.
func TestAZipDirectoryEntryCannotEscapeItsDestination(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	if _, err := zw.Create("../pwned/"); err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}

	server := serveNamed(t, "evil.zip", out.Bytes())
	parent := realDir(t, t.TempDir())

	err := (&Downloader{Client: server.Client()}).Unpack(t.Context(), Archive{
		URL: server.URL + "/evil.zip", ChecksumURL: server.URL + "/evil.zip.sha256",
		Dest: filepath.Join(parent, "into"),
	})
	if err == nil {
		t.Error("a zip with a directory entry outside its destination was accepted")
	}
	if _, err := os.Stat(filepath.Join(parent, "pwned")); err == nil {
		t.Errorf("the zip made a directory outside its destination, at %s", filepath.Join(parent, "pwned"))
	}
}
