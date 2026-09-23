package skill

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// zipOf builds an archive the way zip tools do. A name ending in / is a
// directory entry.
func zipOf(t *testing.T, entries ...Entry) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := w.Create(e.Path)
		if err != nil {
			t.Fatalf("create %q: %v", e.Path, err)
		}
		if _, err := f.Write(e.Data); err != nil {
			t.Fatalf("write %q: %v", e.Path, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

func mustReadZip(t *testing.T, data []byte) []Entry {
	t.Helper()

	entries, err := ReadZip(data)
	if err != nil {
		t.Fatalf("ReadZip: %v", err)
	}
	return entries
}

func zipRefused(t *testing.T, data []byte, want string) {
	t.Helper()

	_, err := ReadZip(data)
	if err == nil {
		t.Fatal("accepted")
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to say %q", err, want)
	}
}

func entryPaths(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

// Right-click, Compress, in Finder: a folder entry, the files, a .DS_Store and
// a __MACOSX shadow of everything. Only what the author wrote comes out, and
// what comes out is a skill.
func TestAZipMadeInFinderReadsAsTheSkillInside(t *testing.T) {
	t.Parallel()

	data := zipOf(t,
		entry("pdf/", ""),
		entry("pdf/SKILL.md", pdfManifest),
		entry("pdf/.DS_Store", "\x00\x00\x00\x01Bud1"),
		entry("pdf/scripts/", ""),
		entry("pdf/scripts/fill.py", "print('fill')\n"),
		entry("__MACOSX/pdf/._SKILL.md", "\x00\x05\x16\x07"),
		entry("__MACOSX/pdf/scripts/._fill.py", "\x00\x05\x16\x07"),
	)

	entries := mustReadZip(t, data)
	if got, want := entryPaths(entries), []string{"pdf/SKILL.md", "pdf/scripts/fill.py"}; !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if string(entries[1].Data) != "print('fill')\n" {
		t.Errorf("fill.py = %q", entries[1].Data)
	}

	dir, err := Assemble(entries)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if dir.Manifest.Name != "pdf" || !slices.Equal(paths(dir), []string{"scripts/fill.py"}) {
		t.Errorf("assembled %q with %v", dir.Manifest.Name, paths(dir))
	}
}

// `zip -r ../pdf.zip .` from inside the folder: no wrapping directory.
func TestAZipOfTheFolderContentsReadsAsIs(t *testing.T) {
	t.Parallel()

	entries := mustReadZip(t, zipOf(t, entry("SKILL.md", pdfManifest), entry("scripts/__init__.py", "")))
	if got := entryPaths(entries); !slices.Equal(got, []string{"SKILL.md", "scripts/__init__.py"}) {
		t.Errorf("entries = %v", got)
	}
}

// Only .DS_Store and __MACOSX are clutter. A file that merely resembles them
// is the author's.
func TestOnlyExactlyTheArchiversOwnFilesAreDropped(t *testing.T) {
	t.Parallel()

	entries := mustReadZip(t, zipOf(t,
		entry("SKILL.md", pdfManifest),
		entry("MACOSX/notes.md", ""),
		entry("docs/__MACOSX", ""),
		entry("x.DS_Store", ""),
	))
	if len(entries) != 4 {
		t.Errorf("entries = %v, want every one kept", entryPaths(entries))
	}
}

func TestSomethingThatIsNotAZipIsRefused(t *testing.T) {
	t.Parallel()

	for name, data := range map[string][]byte{
		"empty":     nil,
		"text":      []byte(pdfManifest),
		"truncated": zipOf(t, entry("SKILL.md", pdfManifest))[:40],
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			zipRefused(t, data, "not a zip archive")
		})
	}
}

// A symlink's bytes are its target. Stored, they are harmless; unpacked by
// whoever downloads the skill, they are a link to /etc/passwd.
func TestASymlinkInTheZipIsRefused(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("SKILL.md")
	_, _ = f.Write([]byte(pdfManifest))
	hdr := &zip.FileHeader{Name: "secrets"}
	hdr.SetMode(fs.ModeSymlink | 0o777)
	f, _ = w.CreateHeader(hdr)
	_, _ = f.Write([]byte("/etc/passwd"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	zipRefused(t, buf.Bytes(), `"secrets" is not a regular file`)
}

// The reader passes a traversal through by default; Assemble refuses it with
// a sentence naming the path.
func TestATraversalInTheZipIsRefusedByAssemble(t *testing.T) {
	t.Parallel()

	entries := mustReadZip(t, zipOf(t, entry("SKILL.md", pdfManifest), entry("../evil.sh", "rm -rf /")))
	if _, err := Assemble(entries); err == nil || !strings.Contains(err.Error(), `"../evil.sh" is not a relative path`) {
		t.Errorf("Assemble: %v, want the traversal named", err)
	}
}

// Under zipinsecurepath=0 the reader fails the whole archive on that same
// name. The refusal must still be the one naming the path, not "not a zip":
// what a deployment sets in GODEBUG must not change what the author is told.
//
// GODEBUG is the runtime's setting, not ours, so it cannot be injected; it is
// re-read when the environment changes, which is why this test is not
// parallel.
func TestATraversalIsNamedUnderTheStricterZipSetting(t *testing.T) {
	data := zipOf(t, entry("SKILL.md", pdfManifest), entry("../evil.sh", "rm -rf /"))
	t.Setenv("GODEBUG", "zipinsecurepath=0")

	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); !errors.Is(err, zip.ErrInsecurePath) {
		t.Fatalf("the setting did not take: NewReader err = %v", err)
	}

	entries := mustReadZip(t, data)
	if _, err := Assemble(entries); err == nil || !strings.Contains(err.Error(), `"../evil.sh"`) {
		t.Errorf("Assemble: %v, want the traversal named", err)
	}
}

// 64 MiB of zeros deflates to about 64 KiB. Reading it whole to measure it is
// exactly what a bomb wants; it must stop one byte past the limit.
//
// Not parallel: it measures the process's allocations, and a parallel test
// allocating beside it would be counted too.
func TestAZipBombIsStoppedWithoutBeingInflated(t *testing.T) {
	const bomb = 64 << 20

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("SKILL.md")
	_, _ = f.Write([]byte(pdfManifest))
	f, _ = w.Create("assets/zeros.bin")
	zeros := make([]byte, 1<<20)
	for range bomb >> 20 {
		_, _ = f.Write(zeros)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 1<<20 {
		t.Fatalf("the fixture is wrong: a %d-byte archive is not a bomb", buf.Len())
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := ReadZip(buf.Bytes())
	runtime.ReadMemStats(&after)

	if err == nil || !strings.Contains(err.Error(), `"assets/zeros.bin" is over 5 MiB`) {
		t.Errorf("err = %v, want the file named as over 5 MiB", err)
	}
	// Stopped costs a few times the limit, since io.ReadAll grows by doubling;
	// inflated costs at least the whole bomb. Half the bomb sits clear of both.
	if got := after.TotalAlloc - before.TotalAlloc; got > bomb/2 {
		t.Errorf("allocated %d MiB reading a %d MiB bomb; it was inflated, not stopped", got>>20, bomb>>20)
	}
}

// The size in the header is the sender's claim, and ReadZip never reads it.
// What stops a header that says 1 byte over an entry holding 6 MiB is
// archive/zip itself, which refuses to read past the declared size. This pins
// that: were it to stop, the entry would still meet our limit, but this is
// the test that would say the library changed under us.
func TestASizeHeaderThatLiesIsNotBelieved(t *testing.T) {
	t.Parallel()

	body := bytes.Repeat([]byte("a"), MaxFileBytes+(1<<20))

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("SKILL.md")
	_, _ = f.Write([]byte(pdfManifest))
	raw, err := w.CreateRaw(&zip.FileHeader{
		Name: "big.txt", Method: zip.Store,
		CompressedSize64: uint64(len(body)), UncompressedSize64: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write(body)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if entries, err := ReadZip(buf.Bytes()); err == nil {
		t.Errorf("accepted, with %d entries", len(entries))
	}
}

// The archive's checksum is checked when an entry is read to its end; a
// corrupted file is refused, not stored as whatever it decompressed to.
func TestACorruptedEntryIsRefused(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.CreateHeader(&zip.FileHeader{Name: "SKILL.md", Method: zip.Store})
	_, _ = f.Write([]byte(pdfManifest))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := buf.Bytes()
	at := bytes.Index(data, []byte("Fill PDF forms."))
	data[at] = 'K'

	zipRefused(t, data, `"SKILL.md" cannot be read`)
}

func TestAZipFileOverFiveMiBIsRefusedByName(t *testing.T) {
	t.Parallel()

	zipRefused(t, zipOf(t,
		entry("SKILL.md", pdfManifest),
		Entry{Path: "assets/big.bin", Data: make([]byte, MaxFileBytes+1)},
	), `"assets/big.bin" is over 5 MiB`)
}

// Files each within the limit can still add up. Past 20 MiB the refusal is
// about the skill, not about whichever file happened to be last.
func TestAZipOverTwentyMiBIsRefused(t *testing.T) {
	t.Parallel()

	zipRefused(t, zipOf(t, filling(MaxSkillBytes+1)...), "a skill is at most 20 MiB")
}

func TestAZipOfMoreThanTwoHundredFilesIsRefused(t *testing.T) {
	t.Parallel()

	zipRefused(t, zipOf(t, manyFiles(MaxFiles+1)...), "at most 200 files")
}

// Folders and clutter are not files. A Finder zip of a 200-file skill has
// dozens of both and must still fit.
func TestFoldersAndClutterDoNotCountTowardTheFiles(t *testing.T) {
	t.Parallel()

	var all []Entry
	for i, e := range manyFiles(MaxFiles) {
		all = append(all, e,
			entry(fmt.Sprintf("dir%03d/", i), ""),
			entry("__MACOSX/._"+e.Path, ""))
	}
	if got := len(mustReadZip(t, zipOf(t, all...))); got != MaxFiles {
		t.Errorf("read %d entries, want %d", got, MaxFiles)
	}
}

// Exactly at each limit is allowed; the refusals above are one past them.
func TestTheLimitsThemselvesAreAllowedInAZip(t *testing.T) {
	t.Parallel()

	for name, entries := range map[string][]Entry{
		"200 files":     manyFiles(MaxFiles),
		"20 MiB":        filling(MaxSkillBytes),
		"a 5 MiB file":  {entry("SKILL.md", pdfManifest), {Path: "big.bin", Data: make([]byte, MaxFileBytes)}},
		"an empty file": {entry("SKILL.md", pdfManifest), entry("empty.txt", "")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := mustReadZip(t, zipOf(t, entries...))
			if len(got) != len(entries) {
				t.Fatalf("read %d entries, want %d", len(got), len(entries))
			}
			for i := range got {
				if !bytes.Equal(got[i].Data, entries[i].Data) {
					t.Errorf("%s came back with different bytes", got[i].Path)
				}
			}
		})
	}
}

// 7-Zip can write BZip2 or LZMA, which archive/zip cannot open. The author is
// told which file, rather than a skill arriving without it.
func TestAnEntryCompressedAWayGoCannotReadIsRefused(t *testing.T) {
	t.Parallel()

	const bzip2 = 12

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("SKILL.md")
	_, _ = f.Write([]byte(pdfManifest))
	raw, err := w.CreateRaw(&zip.FileHeader{Name: "scripts/fill.py", Method: bzip2})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write([]byte("BZh91AY&SY"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	zipRefused(t, buf.Bytes(), `"scripts/fill.py" cannot be read from the zip`)
}

// Once the skill is full, the next entry is read one byte, not five MiB: the
// budget is what the skill has left, not what a file may be. Which limit the
// refusal names is how that shows — reading on to the file's limit would
// report the file instead.
func TestAFullSkillStopsAtTheFirstByteOfTheNextEntry(t *testing.T) {
	t.Parallel()

	entries := append(filling(MaxSkillBytes), Entry{Path: "one-more.bin", Data: make([]byte, MaxFileBytes+1)})
	_, err := ReadZip(zipOf(t, entries...))
	if err == nil || !strings.Contains(err.Error(), "a skill is at most 20 MiB") {
		t.Errorf("err = %v, want the skill's limit, reached before the file's", err)
	}
}
