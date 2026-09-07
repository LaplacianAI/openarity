package stack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
