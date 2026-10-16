package skill

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const pdfManifest = "---\nname: pdf\ndescription: Fill PDF forms.\n---\nRun scripts/fill.py.\n"

func entry(path, data string) Entry {
	return Entry{Path: path, Data: []byte(data)}
}

func mustAssemble(t *testing.T, entries ...Entry) Directory {
	t.Helper()

	dir, err := Assemble(entries)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return dir
}

func refused(t *testing.T, want string, entries ...Entry) {
	t.Helper()

	_, err := Assemble(entries)
	if err == nil {
		t.Fatal("accepted")
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to say %q", err, want)
	}
}

func paths(dir Directory) []string {
	out := make([]string, len(dir.Files))
	for i, f := range dir.Files {
		out[i] = f.Path
	}
	return out
}

// What the editor and an import send: SKILL.md at the top, files beside it.
func TestASkillWithItsManifestAtTheTopIsStoredAsSent(t *testing.T) {
	t.Parallel()

	dir := mustAssemble(t,
		entry("SKILL.md", pdfManifest),
		entry("scripts/fill.py", "print('hi')\n"),
	)
	if dir.Manifest.Name != "pdf" || dir.Manifest.Body != "Run scripts/fill.py.\n" {
		t.Errorf("manifest = %+v", dir.Manifest)
	}
	if got := paths(dir); !slices.Equal(got, []string{"scripts/fill.py"}) {
		t.Errorf("files = %v, want the one beside SKILL.md and not SKILL.md itself", got)
	}
}

// What compressing a folder produces: everything inside one directory named as
// the skill. The folder is stripped, so the same skill stores the same paths
// however it arrived.
func TestAFolderNamedAsTheSkillIsStripped(t *testing.T) {
	t.Parallel()

	dir := mustAssemble(t,
		entry("pdf/SKILL.md", pdfManifest),
		entry("pdf/references/FORMS.md", "# Forms\n"),
	)
	if got := paths(dir); !slices.Equal(got, []string{"references/FORMS.md"}) {
		t.Errorf("files = %v, want the folder stripped", got)
	}
}

// The spec says the directory is named as the skill. A folder that says one
// thing and a manifest that says another is a mistake the author should hear
// about, not one we settle by picking.
func TestAFolderNamedOtherThanTheSkillIsRefused(t *testing.T) {
	t.Parallel()

	refused(t, `the directory is named "pdf-tools" but its SKILL.md names the skill "pdf"`,
		entry("pdf-tools/SKILL.md", pdfManifest),
		entry("pdf-tools/fill.py", ""),
	)
}

func TestFilesComeBackInPathOrderWhateverOrderTheyArrived(t *testing.T) {
	t.Parallel()

	dir := mustAssemble(t,
		entry("scripts/z.py", ""),
		entry("SKILL.md", pdfManifest),
		entry("assets/logo.png", ""),
		entry("references/a.md", ""),
	)
	want := []string{"assets/logo.png", "references/a.md", "scripts/z.py"}
	if got := paths(dir); !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// The hash is what the graph compares to skip re-embedding, and what a
// download is checked against; it must be of these bytes, not of anything
// near them.
func TestEachFileCarriesItsBytesAndTheirHash(t *testing.T) {
	t.Parallel()

	dir := mustAssemble(t,
		entry("SKILL.md", pdfManifest),
		entry("a.txt", "alpha"),
		entry("b.txt", "beta"),
	)
	for _, f := range dir.Files {
		if f.SHA256 != sha256.Sum256(f.Data) {
			t.Errorf("%s: hash is not of its own bytes", f.Path)
		}
	}
	if string(dir.Files[0].Data) != "alpha" || string(dir.Files[1].Data) != "beta" {
		t.Errorf("bytes moved between files: %q, %q", dir.Files[0].Data, dir.Files[1].Data)
	}
}

// The type is what the bytes are, never what the name claims. The read path
// serves it under nosniff, which is only safe if it was sniffed.
func TestAFilesTypeComesFromItsBytesNotItsName(t *testing.T) {
	t.Parallel()

	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	dir := mustAssemble(t,
		entry("SKILL.md", pdfManifest),
		entry("looks-like.png", "<script>alert(1)</script>"),
		entry("looks-like.txt", png),
	)
	for _, f := range dir.Files {
		want := map[string]string{
			"looks-like.png": "text/html; charset=utf-8",
			"looks-like.txt": "image/png",
		}[f.Path]
		if f.MediaType != want {
			t.Errorf("%s: media type %q, want %q", f.Path, f.MediaType, want)
		}
	}
}

func TestAnEmptyFileIsAFile(t *testing.T) {
	t.Parallel()

	dir := mustAssemble(t, entry("SKILL.md", pdfManifest), entry("scripts/__init__.py", ""))
	if len(dir.Files) != 1 || len(dir.Files[0].Data) != 0 || dir.Files[0].MediaType == "" {
		t.Errorf("files = %+v", dir.Files)
	}
}

// A manifest the parser refuses is refused here with the parser's sentence:
// one validator, so no entry point stores what another would reject.
func TestAManifestTheParserRefusesRefusesTheSkill(t *testing.T) {
	t.Parallel()

	refused(t, "unknown field \"summary\"",
		entry("SKILL.md", "---\nname: pdf\nsummary: d\n---\n"))
}

// Every one of these is a path that either escapes the skill, would be read
// differently by another tool, or that Postgres would refuse with a 500.
// Nothing is cleaned, so each must be refused rather than repaired.
func TestAPathThatIsNotCleanAndInsideIsRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ path, want string }{
		"empty":                 {"", "not a relative path"},
		"absolute":              {"/etc/passwd", "not a relative path"},
		"parent":                {"../escape.md", "not a relative path"},
		"parent in the middle":  {"scripts/../../escape.md", "not a relative path"},
		"current":               {"./fill.py", "not a relative path"},
		"current in the middle": {"scripts/./fill.py", "not a relative path"},
		"empty segment":         {"scripts//fill.py", "not a relative path"},
		"trailing slash":        {"scripts/", "not a relative path"},
		"backslash":             {`scripts\fill.py`, "backslash"},
		"windows traversal":     {`..\escape.md`, "backslash"},
		"NUL":                   {"fill\x00.py", "control character"},
		"newline":               {"fill\n.py", "control character"},
		"DEL":                   {"fill\x7f.py", "control character"},
		"not UTF-8":             {"fill\xff.py", "not valid UTF-8"},
		"over 1024 bytes":       {strings.Repeat("a", 1025), "over 1024 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refused(t, tc.want, entry("SKILL.md", pdfManifest), entry(tc.path, "x"))
		})
	}
}

// Segments that merely contain dots are ordinary names.
func TestDotsInsideANameAreNotTraversal(t *testing.T) {
	t.Parallel()

	dir := mustAssemble(t,
		entry("SKILL.md", pdfManifest),
		entry(".env.example", ""),
		entry("scripts/..hidden", ""),
		entry("v1.2/notes...md", ""),
		entry("références/é.md", ""),
		entry(strings.Repeat("a", 1024), ""),
	)
	if len(dir.Files) != 5 {
		t.Errorf("files = %v", paths(dir))
	}
}

// A map would have merged these silently and kept whichever came last.
func TestAPathSentTwiceIsRefused(t *testing.T) {
	t.Parallel()

	refused(t, `"scripts/fill.py" is sent twice`,
		entry("SKILL.md", pdfManifest),
		entry("scripts/fill.py", "one"),
		entry("scripts/fill.py", "two"),
	)
}

func TestTheManifestSentTwiceIsRefused(t *testing.T) {
	t.Parallel()

	refused(t, `"SKILL.md" is sent twice`, entry("SKILL.md", pdfManifest), entry("SKILL.md", pdfManifest))
}

// Where SKILL.md is decides where the skill is, and anything ambiguous is
// refused with the same sentence rather than guessed at.
func TestASkillWhoseManifestIsNotWhereTheSpecSaysIsRefused(t *testing.T) {
	t.Parallel()

	for name, entries := range map[string][]Entry{
		"nothing":                   nil,
		"no manifest":               {entry("fill.py", "")},
		"a lower-case manifest":     {entry("skill.md", pdfManifest)},
		"two folders deep":          {entry("skills/pdf/SKILL.md", pdfManifest)},
		"a folder with no manifest": {entry("pdf/fill.py", "")},
		"two folders": {
			entry("pdf/SKILL.md", pdfManifest), entry("docx/SKILL.md", pdfManifest),
		},
		"a file beside the folder": {
			entry("pdf/SKILL.md", pdfManifest), entry("README.md", ""),
		},
		"a file named as the folder": {
			entry("pdf/SKILL.md", pdfManifest), entry("pdf", ""),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Assemble(entries)
			if !errors.Is(err, errNoManifest) {
				t.Errorf("err = %v, want %v", err, errNoManifest)
			}
		})
	}
}

// A second SKILL.md means a collection of skills sent as one. Storing it as a
// file would make a skill nobody can load, and would hide the one they meant.
func TestASecondManifestDeeperInIsRefused(t *testing.T) {
	t.Parallel()

	refused(t, `"extras/SKILL.md" is a second SKILL.md`,
		entry("SKILL.md", pdfManifest), entry("extras/SKILL.md", pdfManifest))
	refused(t, `"pdf/extras/SKILL.md" is a second SKILL.md`,
		entry("pdf/SKILL.md", pdfManifest), entry("pdf/extras/SKILL.md", pdfManifest))
}

func TestAFileOverFiveMiBIsRefusedByName(t *testing.T) {
	t.Parallel()

	refused(t, `"assets/big.bin" is over 5 MiB`,
		entry("SKILL.md", pdfManifest),
		Entry{Path: "assets/big.bin", Data: make([]byte, MaxFileBytes+1)},
	)
}

func TestMoreThanTwoHundredFilesIsRefused(t *testing.T) {
	t.Parallel()

	refused(t, "at most 200 files", manyFiles(MaxFiles+1)...)
}

// A zip bomb is the zip reader's to stop, but a directory of legal files can
// still add up; the total is counted across every entry, SKILL.md included.
func TestASkillOverTwentyMiBIsRefused(t *testing.T) {
	t.Parallel()

	refused(t, "at most 20 MiB", filling(MaxSkillBytes+1)...)
}

// Exactly at each limit is allowed; every refusal above is one past it.
func TestTheLimitsThemselvesAreAllowedForADirectory(t *testing.T) {
	t.Parallel()

	if dir := mustAssemble(t, manyFiles(MaxFiles)...); len(dir.Files) != MaxFiles-1 {
		t.Errorf("200 entries gave %d files beside SKILL.md, want 199", len(dir.Files))
	}
	mustAssemble(t, filling(MaxSkillBytes)...)
	mustAssemble(t,
		entry("SKILL.md", pdfManifest),
		Entry{Path: "assets/big.bin", Data: make([]byte, MaxFileBytes)},
	)
}

// manyFiles is SKILL.md and enough empty files to make n entries.
func manyFiles(n int) []Entry {
	entries := []Entry{entry("SKILL.md", pdfManifest)}
	for i := 1; i < n; i++ {
		entries = append(entries, entry(fmt.Sprintf("f%03d.txt", i), ""))
	}
	return entries
}

// filling is SKILL.md and files of at most 5 MiB making exactly total bytes.
func filling(total int) []Entry {
	entries := []Entry{entry("SKILL.md", pdfManifest)}
	left := total - len(pdfManifest)
	for i := 0; left > 0; i++ {
		n := min(left, MaxFileBytes)
		entries = append(entries, Entry{Path: fmt.Sprintf("part%d.bin", i), Data: make([]byte, n)})
		left -= n
	}
	return entries
}
