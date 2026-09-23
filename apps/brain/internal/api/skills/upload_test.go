package skills

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"slices"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
)

const uploadManifest = "---\nname: pdf\ndescription: Fill PDF forms.\n---\n# PDF\n"

type part struct{ field, filename, data string }

// multipartBody writes the parts as a browser does, each filename exactly as
// given, including its folders.
func multipartBody(t *testing.T, parts ...part) (string, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		params := map[string]string{"name": p.field}
		if p.filename != "" {
			params["filename"] = p.filename
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", mime.FormatMediaType("form-data", params))
		pw, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = pw.Write([]byte(p.data))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return w.FormDataContentType(), &buf
}

func zipBody(t *testing.T, files ...part) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		fw, err := w.Create(f.filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(f.data))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

type uploadResult struct {
	res *http.Response
	dir skill.Directory
	ok  bool
}

func upload(t *testing.T, contentType string, body io.Reader) uploadResult {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/teams/x/skills", body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	dir, ok := readUpload(rec, req)
	return uploadResult{res: rec.Result(), dir: dir, ok: ok}
}

func (u uploadResult) refused(t *testing.T, status int, want string) {
	t.Helper()

	if u.ok {
		t.Fatalf("accepted %q", u.dir.Manifest.Name)
	}
	body, _ := io.ReadAll(u.res.Body)
	if u.res.StatusCode != status {
		t.Errorf("status = %d, want %d (%s)", u.res.StatusCode, status, body)
	}
	if !strings.Contains(string(body), want) {
		t.Errorf("body = %q, want it to say %q", body, want)
	}
}

func (u uploadResult) files() []string {
	out := make([]string, len(u.dir.Files))
	for i, f := range u.dir.Files {
		out[i] = f.Path
	}
	return out
}

// What Chrome sends for <input type=file webkitdirectory>: one part per file,
// each filename the path relative to the chosen folder, the folder included.
func TestAFolderUploadedFromABrowserIsTheSkill(t *testing.T) {
	t.Parallel()

	ct, body := multipartBody(t,
		part{"files", "pdf/SKILL.md", uploadManifest},
		part{"files", "pdf/scripts/fill.py", "print('fill')\n"},
		part{"files", "pdf/references/FORMS.md", "# Forms\n"},
	)
	u := upload(t, ct, body)
	if !u.ok {
		t.Fatalf("refused: %d", u.res.StatusCode)
	}
	if u.dir.Manifest.Name != "pdf" {
		t.Errorf("name = %q", u.dir.Manifest.Name)
	}
	if got, want := u.files(), []string{"references/FORMS.md", "scripts/fill.py"}; !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// The editor and the CLI send only the SKILL.md.
func TestASkillOfOnlyItsManifestIsAccepted(t *testing.T) {
	t.Parallel()

	ct, body := multipartBody(t, part{"files", "SKILL.md", uploadManifest})
	if u := upload(t, ct, body); !u.ok || u.dir.Manifest.Body != "# PDF\n" || len(u.dir.Files) != 0 {
		t.Errorf("ok = %v, dir = %+v", u.ok, u.dir)
	}
}

func TestAZipUploadIsTheSkillInside(t *testing.T) {
	t.Parallel()

	body := zipBody(t,
		part{filename: "pdf/SKILL.md", data: uploadManifest},
		part{filename: "pdf/scripts/fill.py", data: "print('fill')\n"},
	)
	u := upload(t, "application/zip", body)
	if !u.ok || u.dir.Manifest.Name != "pdf" || !slices.Equal(u.files(), []string{"scripts/fill.py"}) {
		t.Errorf("ok = %v, name %q, files %v", u.ok, u.dir.Manifest.Name, u.files())
	}
}

// Media types are case-insensitive, and a parameter the sender adds is theirs.
func TestTheContentTypeIsReadAsAMediaType(t *testing.T) {
	t.Parallel()

	body := zipBody(t, part{filename: "SKILL.md", data: uploadManifest})
	if u := upload(t, "Application/ZIP; charset=binary", body); !u.ok {
		t.Errorf("refused: %d", u.res.StatusCode)
	}
}

// Part.FileName would have turned this into "evil" and stored it. The path
// the author sent is the one refused, and named.
func TestATraversalInAPartsFilenameIsRefusedAsSent(t *testing.T) {
	t.Parallel()

	ct, body := multipartBody(t,
		part{"files", "SKILL.md", uploadManifest},
		part{"files", "../evil.sh", "rm -rf /"},
	)
	upload(t, ct, body).refused(t, http.StatusBadRequest, `"../evil.sh" is not a relative path`)
}

// Two files with one name in different folders are two files. Reading the
// base name would make them one path sent twice.
func TestFilesWithOneNameInTwoFoldersAreBothKept(t *testing.T) {
	t.Parallel()

	ct, body := multipartBody(t,
		part{"files", "SKILL.md", uploadManifest},
		part{"files", "forms/README.md", "forms"},
		part{"files", "scripts/README.md", "scripts"},
	)
	u := upload(t, ct, body)
	if want := []string{"forms/README.md", "scripts/README.md"}; !u.ok || !slices.Equal(u.files(), want) {
		t.Errorf("ok = %v, files = %v, want %v", u.ok, u.files(), want)
	}
}

// A part that is not a file in the files field is a client that has
// misunderstood the form; accepting it would store its value as a file named
// after nothing.
func TestAPartThatIsNotAFileInTheFilesFieldIsRefused(t *testing.T) {
	t.Parallel()

	for name, extra := range map[string]part{
		"another field": {"file", "scripts/fill.py", "x"},
		"no filename":   {"files", "", "x"},
		"a plain field": {"name", "", "pdf"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ct, body := multipartBody(t, part{"files", "SKILL.md", uploadManifest}, extra)
			upload(t, ct, body).refused(t, http.StatusBadRequest, `every part must be a file in the "files" field`)
		})
	}
}

// Anything but the two forms is refused before a byte of the body is read,
// and says which two it wants.
func TestAnotherContentTypeIsUnsupported(t *testing.T) {
	t.Parallel()

	for _, ct := range []string{"", "application/json", "text/plain", "multipart/mixed; boundary=x", "not a media type;;"} {
		t.Run(ct, func(t *testing.T) {
			t.Parallel()

			body := &endless{}
			upload(t, ct, body).refused(t, http.StatusUnsupportedMediaType, "multipart/form-data or application/zip")
			if body.taken != 0 {
				t.Errorf("read %d bytes of a body it was never going to accept", body.taken)
			}
		})
	}
}

// A body past the cap is refused as too large, whichever form it claims, and
// is read no further than the cap. For multipart the sender has to be sly:
// one endless line is refused after 4 KiB by the reader itself, but short
// lines before the first boundary are preamble, skipped for as long as they
// come — the cap is the only thing that stops them.
func TestABodyPastTheCapIsTooLarge(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ ct, pattern string }{
		"zip":                  {"application/zip", "a"},
		"a multipart preamble": {"multipart/form-data; boundary=never-arrives", "preamble\r\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := &endless{pattern: tc.pattern}
			upload(t, tc.ct, body).refused(t, http.StatusRequestEntityTooLarge, "at most 21 MiB")
			if body.taken > maxUploadBytes+64<<10 {
				t.Errorf("read %d bytes of a body capped at %d", body.taken, maxUploadBytes)
			}
		})
	}
}

// One endless line is refused by the multipart reader long before the cap.
func TestAnEndlessMultipartLineIsRefusedEarly(t *testing.T) {
	t.Parallel()

	body := &endless{pattern: "a"}
	upload(t, "multipart/form-data; boundary=never-arrives", body).refused(t, http.StatusBadRequest, "not valid multipart")
	if body.taken > 1<<20 {
		t.Errorf("read %d bytes of one line with no boundary", body.taken)
	}
}

// Over a skill's limits is a 400, not a 413: the request arrived whole, and
// what it holds is not a skill we store. Each limit is the collector's.
func TestContentOverASkillsLimitsIsABadRequest(t *testing.T) {
	t.Parallel()

	big := make([]byte, skill.MaxFileBytes+1)
	_, _ = rand.Read(big)

	t.Run("a file in a multipart upload", func(t *testing.T) {
		t.Parallel()
		ct, body := multipartBody(t, part{"files", "SKILL.md", uploadManifest}, part{"files", "assets/big.bin", string(big)})
		upload(t, ct, body).refused(t, http.StatusBadRequest, `"assets/big.bin" is over 5 MiB`)
	})

	t.Run("a bomb in a zip", func(t *testing.T) {
		t.Parallel()
		body := zipBody(t, part{filename: "SKILL.md", data: uploadManifest},
			part{filename: "assets/zeros.bin", data: string(make([]byte, 32<<20))})
		if body.Len() > 1<<20 {
			t.Fatalf("the fixture is wrong: %d bytes is not a bomb", body.Len())
		}
		upload(t, "application/zip", body).refused(t, http.StatusBadRequest, `"assets/zeros.bin" is over 5 MiB`)
	})
}

func TestABodyThatIsNotWhatItClaimsIsABadRequest(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ ct, body, want string }{
		"not a zip":   {"application/zip", uploadManifest, "not a zip archive"},
		"no boundary": {"multipart/form-data", "--x\r\n\r\nx\r\n--x--\r\n", "not valid multipart"},
		"a truncated part": {
			"multipart/form-data; boundary=x",
			"--x\r\nContent-Disposition: form-data; name=\"files\"; filename=\"SKILL.md\"\r\n\r\n---\nname: pdf",
			`"SKILL.md" cannot be read`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			upload(t, tc.ct, strings.NewReader(tc.body)).refused(t, http.StatusBadRequest, tc.want)
		})
	}
}

// Whatever arrived, it is refused by the same validator as every other entry
// point, with its sentence.
func TestAnUploadThatIsNotASkillIsRefusedByAssemble(t *testing.T) {
	t.Parallel()

	ct, body := multipartBody(t, part{"files", "README.md", "# not a skill"})
	upload(t, ct, body).refused(t, http.StatusBadRequest, "SKILL.md must be at the top of the skill")

	ct, body = multipartBody(t, part{"files", "SKILL.md", "---\nname: PDF\ndescription: d\n---\n"})
	upload(t, ct, body).refused(t, http.StatusBadRequest, "lower-case")
}

// endless is a sender that never stops, repeating its pattern, and counts
// what was taken from it.
type endless struct {
	pattern string
	taken   int
	at      int
}

func (e *endless) Read(p []byte) (int, error) {
	if e.pattern == "" {
		e.pattern = "a"
	}
	for i := range p {
		p[i] = e.pattern[e.at%len(e.pattern)]
		e.at++
	}
	e.taken += len(p)
	return len(p), nil
}

// A boundary the body never uses leaves no parts at all: the whole body is
// preamble. No parts is no SKILL.md, and that is the sentence.
func TestABodyWithNoPartsHasNoManifest(t *testing.T) {
	t.Parallel()

	upload(t, "multipart/form-data; boundary=y", strings.NewReader("--x\r\n\r\nx\r\n--x--\r\n")).
		refused(t, http.StatusBadRequest, "SKILL.md must be at the top of the skill")
}

// partPath checks the parse error and the filename both, and no test can tell
// them apart: mime.ParseMediaType returns no parameters when it fails, so a
// malformed disposition never has a filename either. This pins that, so the
// day it stops holding the parse error is the only check left and a test says
// so.
func TestAMalformedDispositionHasNoFilename(t *testing.T) {
	t.Parallel()

	const malformed = `form-data; name="files"; filename="a.md"; =broken`
	if _, params, err := mime.ParseMediaType(malformed); err == nil || params["filename"] != "" {
		t.Fatalf("ParseMediaType(%q) = %v, %v; want an error and no filename", malformed, params, err)
	}

	body := "--x\r\nContent-Disposition: " + malformed + "\r\n\r\nx\r\n--x--\r\n"
	upload(t, "multipart/form-data; boundary=x", strings.NewReader(body)).
		refused(t, http.StatusBadRequest, `every part must be a file in the "files" field`)
}
