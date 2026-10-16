package hub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
)

const (
	commit    = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	topFolder = "acme-skills-a1a1a1a/"
)

const pdfSKILL = "---\nname: pdf\ndescription: Fill PDF forms.\n---\n# PDF\n"

// localCert is one certificate for both 127.0.0.1 and localhost, so the fake
// API and the fake codeload can sit on different host names — which is what
// decides whether Go forwards Authorization on a redirect.
func localCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "hub test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// fakeGitHub answers as the real one was measured to: commits/{ref} with
// Accept application/vnd.github.sha is the bare SHA, anything else JSON; an
// unknown ref is 422; tarball/{ref} redirects to codeload on another host.
type fakeGitHub struct {
	api, codeload *httptest.Server
	pool          *x509.CertPool

	refs    map[string]string
	serve   func(w http.ResponseWriter)
	commits int // a non-zero status overrides the commits answer
	tarball int // and the tarball's

	mu           sync.Mutex
	apiURIs      []string
	apiAuth      []string
	codeloadAuth []string
}

func newFakeGitHub(t *testing.T, tarball []byte) *fakeGitHub {
	t.Helper()

	cert, pool := localCert(t)
	f := &fakeGitHub{
		pool: pool,
		refs: map[string]string{"main": commit, "HEAD": commit, "feature/forms": commit, commit: commit},
		serve: func(w http.ResponseWriter) {
			_, _ = w.Write(tarball)
		},
	}

	f.codeload = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.codeloadAuth = append(f.codeloadAuth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		f.serve(w)
	}))
	f.codeload.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	f.codeload.StartTLS()
	t.Cleanup(f.codeload.Close)
	codeloadPort := f.codeload.URL[strings.LastIndex(f.codeload.URL, ":"):]

	f.api = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.apiURIs = append(f.apiURIs, r.RequestURI)
		f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
		f.mu.Unlock()

		rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/repos/acme/skills/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch kind, ref, _ := strings.Cut(rest, "/"); kind {
		case "commits":
			if f.commits != 0 {
				w.WriteHeader(f.commits)
				return
			}
			unescaped, _ := url.PathUnescape(ref)
			sha, known := f.refs[unescaped]
			if !known {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			if r.Header.Get("Accept") != "application/vnd.github.sha" {
				_, _ = io.WriteString(w, `{"sha":"`+sha+`"}`)
				return
			}
			_, _ = io.WriteString(w, sha)
		case "tarball":
			if f.tarball != 0 {
				w.WriteHeader(f.tarball)
				return
			}
			http.Redirect(w, r, "https://localhost"+codeloadPort+"/acme/skills/legacy.tar.gz/"+ref, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	f.api.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	f.api.StartTLS()
	t.Cleanup(f.api.Close)

	return f
}

func (f *fakeGitHub) client(token string) GitHub {
	return GitHub{
		Client: NewClient(Policy{Hosts: []string{"127.0.0.1", "localhost"}, Allow: loopbackOnly, RootCAs: f.pool}),
		API:    f.api.URL,
		Token:  token,
	}
}

func (f *fakeGitHub) fetch(t *testing.T, source, token string) (string, []skill.Entry, error) {
	t.Helper()

	src, err := ParseGitHub(source)
	if err != nil {
		t.Fatalf("ParseGitHub(%q): %v", source, err)
	}
	return f.client(token).Fetch(t.Context(), src)
}

type tarEntry struct {
	name     string
	typeflag byte
	body     string
	link     string
}

func file(name, body string) tarEntry { return tarEntry{name: name, typeflag: tar.TypeReg, body: body} }
func folder(name string) tarEntry     { return tarEntry{name: name, typeflag: tar.TypeDir} }

// tarballOf is what codeload serves: a pax header naming the commit when one
// is given, then every entry, gzipped.
func tarballOf(t *testing.T, sha string, entries ...tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if sha != "" {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
			PAXRecords: map[string]string{"comment": sha},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o644, Linkname: e.link}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// aRepository is shaped like anthropics/skills: files at the root, a skills
// folder holding several skills, and a sibling whose name starts like pdf.
func aRepository(t *testing.T, sha string) []byte {
	t.Helper()

	return tarballOf(t, sha,
		folder(topFolder),
		file(topFolder+"README.md", "# skills"),
		folder(topFolder+"skills/"),
		folder(topFolder+"skills/pdf/"),
		file(topFolder+"skills/pdf/SKILL.md", pdfSKILL),
		folder(topFolder+"skills/pdf/scripts/"),
		file(topFolder+"skills/pdf/scripts/fill.py", "print('fill')\n"),
		folder(topFolder+"skills/pdf-extras/"),
		file(topFolder+"skills/pdf-extras/SKILL.md", "---\nname: pdf-extras\ndescription: d\n---\n"),
		folder(topFolder+"skills/docx/"),
		file(topFolder+"skills/docx/SKILL.md", "---\nname: docx\ndescription: d\n---\n"),
	)
}

func paths(entries []skill.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

// --- fetching ---

// Only the named folder comes back, its paths relative to it, and what comes
// back is a skill the one validator accepts.
func TestAFolderIsImportedAtTheResolvedCommit(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	sha, entries, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if sha != commit {
		t.Errorf("sha = %q, want %q", sha, commit)
	}
	if got, want := paths(entries), []string{"SKILL.md", "scripts/fill.py"}; !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if string(entries[1].Data) != "print('fill')\n" {
		t.Errorf("fill.py = %q", entries[1].Data)
	}
	if dir, err := skill.Assemble(entries); err != nil || dir.Manifest.Name != "pdf" {
		t.Errorf("Assemble: %v (%q)", err, dir.Manifest.Name)
	}
}

// A folder whose name merely starts like the one asked for is a different
// folder: skills/pdf is not skills/pdf-extras.
func TestAFolderNameIsMatchedWholeNotAsAPrefix(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	_, entries, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Path, "extras") || strings.HasPrefix(e.Path, "-") {
			t.Errorf("a sibling folder leaked in: %q", e.Path)
		}
	}
}

// With no folder, the repository's root is the skill.
func TestTheRepositoryRootCanBeTheSkill(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, tarballOf(t, commit,
		folder(topFolder),
		file(topFolder+"SKILL.md", pdfSKILL),
		file(topFolder+"forms.md", "# forms"),
	))
	_, entries, err := f.fetch(t, "github:acme/skills", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(entries); !slices.Equal(got, []string{"SKILL.md", "forms.md"}) {
		t.Errorf("entries = %v", got)
	}
}

// The tarball is asked for by the commit just resolved, never by the ref: a
// branch that moves between the two requests would otherwise import a commit
// other than the one recorded in source_sha.
func TestTheTarballIsFetchedByCommitNotByRef(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	if _, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", ""); err != nil {
		t.Fatal(err)
	}

	want := []string{"/repos/acme/skills/commits/main", "/repos/acme/skills/tarball/" + commit}
	if !slices.Equal(f.apiURIs, want) {
		t.Errorf("the API was asked for %v, want %v", f.apiURIs, want)
	}
}

// A branch with a slash is one path segment to the API, escaped.
func TestABranchWithASlashIsEscapedIntoOneSegment(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	if _, _, err := f.fetch(t, "github:acme/skills/skills/pdf@feature/forms", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.apiURIs) == 0 || f.apiURIs[0] != "/repos/acme/skills/commits/feature%2Fforms" {
		t.Errorf("commits asked for %v", f.apiURIs)
	}
}

// --- the token ---

// The token is for the API. Codeload is another host, and Go drops
// Authorization on a redirect that changes host: GitHub puts a short-lived
// token of its own in the redirect for a private repository.
func TestTheTokenGoesToTheAPIAndNeverToCodeload(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	if _, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "ghp_secret"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(f.apiAuth, []string{"Bearer ghp_secret", "Bearer ghp_secret"}) {
		t.Errorf("the API saw Authorization %q", f.apiAuth)
	}
	if len(f.codeloadAuth) != 1 || f.codeloadAuth[0] != "" {
		t.Errorf("codeload saw Authorization %q; the token left the API", f.codeloadAuth)
	}
}

func TestWithoutATokenNoAuthorizationIsSent(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	if _, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", ""); err != nil {
		t.Fatal(err)
	}
	for _, auth := range append(slices.Clone(f.apiAuth), f.codeloadAuth...) {
		if auth != "" {
			t.Errorf("Authorization %q sent with no token", auth)
		}
	}
}

// --- what GitHub answers ---

// Each status becomes a sentence the caller can act on. Not found — which
// GitHub also answers for a private repository the token cannot see — is
// ErrNotFound, so the handler can say 404; the rest are ours to report.
func TestEachGitHubStatusIsTranslated(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		commits, tarball int
		notFound         bool
		want             string
	}{
		"no such repository":  {commits: http.StatusNotFound, notFound: true, want: "no such repository or ref"},
		"no such ref":         {commits: http.StatusUnprocessableEntity, notFound: true, want: "no such repository or ref"},
		"a bad token":         {commits: http.StatusUnauthorized, want: "refused the request (401)"},
		"rate limited":        {commits: http.StatusForbidden, want: "refused the request (403)"},
		"too many":            {commits: http.StatusTooManyRequests, want: "refused the request (429)"},
		"an outage":           {commits: http.StatusBadGateway, want: "GitHub answered 502"},
		"the tarball is gone": {tarball: http.StatusNotFound, notFound: true, want: "no such repository or ref"},
		"the tarball fails":   {tarball: http.StatusInternalServerError, want: "GitHub answered 500"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFakeGitHub(t, aRepository(t, commit))
			f.commits, f.tarball = tc.commits, tc.tarball
			_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
			if errors.Is(err, ErrNotFound) != tc.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v", !tc.notFound, tc.notFound)
			}
		})
	}
}

func TestAnUnknownRefIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	if _, _, err := f.fetch(t, "github:acme/skills/skills/pdf@no-such-branch", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// Anything but a bare, lower-case, forty-digit SHA is not a commit, and is
// never used to build the next URL.
func TestAnAnswerThatIsNotACommitIsRefused(t *testing.T) {
	t.Parallel()

	for name, answer := range map[string]string{
		"JSON":        `{"sha":"` + commit + `"}`,
		"short":       commit[:39],
		"upper case":  strings.ToUpper(commit),
		"a path":      "../../tarball/" + commit[:26],
		"empty":       "",
		"with suffix": commit + "x",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFakeGitHub(t, aRepository(t, commit))
			f.refs["main"] = answer
			if answer == "" {
				f.refs["main"] = " "
			}
			_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
			if err == nil || !strings.Contains(err.Error(), "did not answer with a commit") {
				t.Errorf("err = %v", err)
			}
			if len(f.apiURIs) != 1 {
				t.Errorf("the API was asked %v; nothing may follow a bad answer", f.apiURIs)
			}
		})
	}
}

// A trailing newline is how curl and some proxies hand the SHA back; it is
// whitespace, not part of the commit.
func TestASHAWithATrailingNewlineIsTheCommit(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	f.refs["main"] = commit + "\n"
	if sha, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", ""); err != nil || sha != commit {
		t.Errorf("sha %q, err %v", sha, err)
	}
}

// --- the tarball ---

// GitHub's pax header names the commit the tarball is of. Any other commit is
// not what was resolved, and not what source_sha would record.
func TestATarballOfAnotherCommitIsRefused(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, strings.Repeat("b2", 20)))
	_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err == nil || !strings.Contains(err.Error(), "not of the commit that was resolved") {
		t.Errorf("err = %v", err)
	}
}

// The header is checked when it is there; a tarball without one is not
// refused for it.
func TestATarballWithoutTheCommitHeaderIsAccepted(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, ""))
	if _, entries, err := f.fetch(t, "github:acme/skills/skills/pdf@main", ""); err != nil || len(entries) != 2 {
		t.Errorf("entries %v, err %v", paths(entries), err)
	}
}

func TestAFolderTheCommitDoesNotHaveIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	_, _, err := f.fetch(t, "github:acme/skills/skills/xlsx@main", "")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), `no folder "skills/xlsx"`) {
		t.Errorf("err = %v", err)
	}
}

// A file that happens to have the folder's name is not the folder.
func TestAFileNamedLikeTheFolderIsNotTheFolder(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, tarballOf(t, commit,
		folder(topFolder),
		file(topFolder+"skills", "not a folder"),
	))
	_, _, err := f.fetch(t, "github:acme/skills/skills@main", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for a file named like the folder", err)
	}
}

// Inside the folder, only files and folders: a link's bytes are its target,
// and unpacked by whoever downloads the skill it is a link to anywhere.
// Outside the folder, a link is someone else's business and is skipped.
func TestALinkIsRefusedInsideTheFolderAndIgnoredOutsideIt(t *testing.T) {
	t.Parallel()

	for name, link := range map[string]tarEntry{
		"a symlink":  {name: topFolder + "skills/pdf/secrets", typeflag: tar.TypeSymlink, link: "/etc/passwd"},
		"a hardlink": {name: topFolder + "skills/pdf/copy", typeflag: tar.TypeLink, link: topFolder + "README.md"},
		"a fifo":     {name: topFolder + "skills/pdf/pipe", typeflag: tar.TypeFifo},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFakeGitHub(t, tarballOf(t, commit,
				folder(topFolder), folder(topFolder+"skills/pdf/"),
				file(topFolder+"skills/pdf/SKILL.md", pdfSKILL),
				link,
			))
			_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
			if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
				t.Errorf("err = %v", err)
			}
		})
	}

	t.Run("outside the folder", func(t *testing.T) {
		t.Parallel()

		f := newFakeGitHub(t, tarballOf(t, commit,
			folder(topFolder),
			tarEntry{name: topFolder + "docs/latest", typeflag: tar.TypeSymlink, link: "../README.md"},
			folder(topFolder+"skills/pdf/"),
			file(topFolder+"skills/pdf/SKILL.md", pdfSKILL),
		))
		if _, entries, err := f.fetch(t, "github:acme/skills/skills/pdf@main", ""); err != nil || len(entries) != 1 {
			t.Errorf("entries %v, err %v", paths(entries), err)
		}
	})
}

// A GitHub tarball is one folder. An entry beside it is not something GitHub
// produces, and is not guessed at.
func TestAnEntryOutsideTheRepositoryFolderIsRefused(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, tarballOf(t, commit,
		folder(topFolder),
		file(topFolder+"skills/pdf/SKILL.md", pdfSKILL),
		file("elsewhere/skills/pdf/evil.sh", "rm -rf /"),
	))
	_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err == nil || !strings.Contains(err.Error(), "not one repository folder") {
		t.Errorf("err = %v", err)
	}
}

// A name that climbs out is passed on as written, and refused by Assemble with
// the path named — the same refusal every other entry point gets.
func TestATraversalInTheTarballIsRefusedByAssemble(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, tarballOf(t, commit,
		folder(topFolder),
		file(topFolder+"skills/pdf/SKILL.md", pdfSKILL),
		file(topFolder+"skills/pdf/../../../evil.sh", "rm -rf /"),
	))
	_, entries, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := skill.Assemble(entries); err == nil || !strings.Contains(err.Error(), `"../../../evil.sh" is not a relative path`) {
		t.Errorf("Assemble: %v", err)
	}
}

// The folder's files are kept under the Collector's limits, named.
func TestAFileOverASkillsLimitIsRefusedByName(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, tarballOf(t, commit,
		folder(topFolder),
		file(topFolder+"skills/pdf/SKILL.md", pdfSKILL),
		file(topFolder+"skills/pdf/big.bin", strings.Repeat("x", skill.MaxFileBytes+1)),
	))
	_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err == nil || !strings.Contains(err.Error(), `"big.bin" is over 5 MiB`) {
		t.Errorf("err = %v", err)
	}
}

func TestATarballThatIsNotGzipIsRefused(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, []byte("<html>rate limited</html>"))
	_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err == nil || !strings.Contains(err.Error(), "tarball cannot be read") {
		t.Errorf("err = %v", err)
	}
}

// streamBig serves a repository holding one file of n zeros outside the skill,
// compressed at level, without ever holding it in memory.
func streamBig(level int, n int64) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		gz, _ := gzip.NewWriterLevel(w, level)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: topFolder + "skills/pdf/SKILL.md", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(pdfSKILL))})
		_, _ = io.WriteString(tw, pdfSKILL)
		_ = tw.WriteHeader(&tar.Header{Name: topFolder + "vendor/huge.bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: n})
		chunk := make([]byte, 1<<20)
		for left := n; left > 0; left -= int64(len(chunk)) {
			if _, err := tw.Write(chunk[:min(left, int64(len(chunk)))]); err != nil {
				return
			}
		}
		_ = tw.Close()
		_ = gz.Close()
	}
}

// A repository too large to download is refused as that, by name, rather than
// as a tarball that merely looks corrupt.
func TestARepositoryTooLargeToDownloadIsRefusedByName(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, nil)
	f.serve = streamBig(gzip.NoCompression, maxTarballBytes+(8<<20))
	_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err == nil || !strings.Contains(err.Error(), "over 64 MiB to download") {
		t.Errorf("err = %v", err)
	}
}

// A small download can still unpack enormously, and every byte of it is
// inflated to be skipped. Past the scan cap the import stops, by name.
func TestARepositoryTooLargeToUnpackIsRefusedByName(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, nil)
	f.serve = streamBig(gzip.BestSpeed, maxScannedBytes+(8<<20))
	_, _, err := f.fetch(t, "github:acme/skills/skills/pdf@main", "")
	if err == nil || !strings.Contains(err.Error(), "over 256 MiB unpacked") {
		t.Errorf("err = %v", err)
	}
}

// capped is what makes both refusals above say what they are.
func TestCappedReadsExactlyItsAllowanceThenFails(t *testing.T) {
	t.Parallel()

	stop := errors.New("stop")
	c := &capped{r: strings.NewReader("abcdef"), left: 4, err: stop}
	got, err := io.ReadAll(c)
	if string(got) != "abcd" || !errors.Is(err, stop) {
		t.Errorf("read %q, err %v; want abcd then the cap's error", got, err)
	}

	exact := &capped{r: strings.NewReader("abcd"), left: 8, err: stop}
	if got, err := io.ReadAll(exact); string(got) != "abcd" || err != nil {
		t.Errorf("under the cap: read %q, err %v", got, err)
	}
}

func TestACancelledImportStops(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	src, _ := ParseGitHub("github:acme/skills/skills/pdf@main")
	if _, _, err := f.client("").Fetch(ctx, src); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(f.apiURIs) != 0 {
		t.Errorf("a cancelled import reached the API: %v", f.apiURIs)
	}
}

// --- parsing a source ---

// What is stored is the source spelled out: no ref means HEAD, and it is
// written, so a sync re-resolves exactly what was asked for.
func TestASourceParsesAndIsRecordedWithItsRef(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]struct {
		src    GitHubSource
		stored string
	}{
		"github:anthropics/skills/skills/pdf@main": {GitHubSource{"anthropics", "skills", "skills/pdf", "main"}, "github:anthropics/skills/skills/pdf@main"},
		"github:anthropics/skills/skills/pdf":      {GitHubSource{"anthropics", "skills", "skills/pdf", "HEAD"}, "github:anthropics/skills/skills/pdf@HEAD"},
		"github:acme/pdf-skill":                    {GitHubSource{"acme", "pdf-skill", "", "HEAD"}, "github:acme/pdf-skill@HEAD"},
		"github:acme/skills/a/b/c@v1.2.0":          {GitHubSource{"acme", "skills", "a/b/c", "v1.2.0"}, "github:acme/skills/a/b/c@v1.2.0"},
		"github:acme/skills/x@feature/forms":       {GitHubSource{"acme", "skills", "x", "feature/forms"}, "github:acme/skills/x@feature/forms"},
		"github:acme/skills/x@" + commit:           {GitHubSource{"acme", "skills", "x", commit}, "github:acme/skills/x@" + commit},
		"github:Acme/.github/My Skill@main":        {GitHubSource{"Acme", ".github", "My Skill", "main"}, "github:Acme/.github/My Skill@main"},
	} {
		got, err := ParseGitHub(in)
		if err != nil {
			t.Errorf("ParseGitHub(%q): %v", in, err)
			continue
		}
		if got != want.src || got.String() != want.stored {
			t.Errorf("ParseGitHub(%q) = %+v stored as %q, want %+v as %q", in, got, got.String(), want.src, want.stored)
		}
		if again, err := ParseGitHub(got.String()); err != nil || again != got {
			t.Errorf("%q does not parse back to itself: %+v, %v", got.String(), again, err)
		}
	}
}

// Every part is checked before it reaches a URL. The ref most of all: `..`
// is not escaped by url.PathEscape, so commits/.. would ask the API for a
// different endpoint on an allowed host.
func TestASourceThatIsNotOneIsRefused(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"https://github.com/acme/skills":                   "github:owner/repo/path@ref",
		"github:acme":                                      "github:owner/repo/path@ref",
		"github:-acme/skills":                              `"-acme" is not a GitHub owner`,
		"github:" + strings.Repeat("a", 40) + "/x":         "is not a GitHub owner",
		"github:ac me/skills":                              "is not a GitHub owner",
		"github:acme/..":                                   `".." is not a GitHub repository`,
		"github:acme/.":                                    `"." is not a GitHub repository`,
		"github:acme/sk%ills":                              "is not a GitHub repository",
		"github:acme/skills/../../x":                       "is not a folder in a repository",
		"github:acme/skills/a/./b":                         "is not a folder in a repository",
		"github:acme/skills/a//b":                          "is not a folder in a repository",
		"github:acme/skills/a/b/":                          "is not a folder in a repository",
		"github:acme/skills/a\\b":                          "is not a folder in a repository",
		"github:acme/skills/a\nb":                          "is not a folder in a repository",
		"github:acme/skills/x@":                            "is not a branch, tag or commit",
		"github:acme/skills/x@..":                          "is not a branch, tag or commit",
		"github:acme/skills/x@a..b":                        "is not a branch, tag or commit",
		"github:acme/skills/x@main/":                       "is not a branch, tag or commit",
		"github:acme/skills/x@-rf":                         "is not a branch, tag or commit",
		"github:acme/skills/x@.hidden":                     "is not a branch, tag or commit",
		"github:acme/skills/x@a b":                         "is not a branch, tag or commit",
		"github:acme/skills/x@a%2Fb":                       "is not a branch, tag or commit",
		"github:acme/skills/x@a?b":                         "is not a branch, tag or commit",
		"github:acme/skills/x@" + strings.Repeat("r", 256): "is not a branch, tag or commit",
	} {
		_, err := ParseGitHub(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseGitHub(%q) = %v, want it to say %q", in, err, want)
		}
	}
}

// The limits are the edges of GitHub's own rules, and exactly at them parses.
func TestTheLongestOwnerRepositoryAndRefParse(t *testing.T) {
	t.Parallel()

	in := "github:" + strings.Repeat("o", 39) + "/" + strings.Repeat("r", 100) + "/x@" + strings.Repeat("f", 255)
	if _, err := ParseGitHub(in); err != nil {
		t.Errorf("ParseGitHub at the limits: %v", err)
	}
}

// An answer cut off mid-body is an error, not a shorter SHA.
func TestACommitAnswerCutOffIsAnError(t *testing.T) {
	t.Parallel()

	cert, pool := localCert(t)
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "40")
		_, _ = io.WriteString(w, commit[:10])
	}))
	api.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	api.StartTLS()
	t.Cleanup(api.Close)

	g := GitHub{Client: NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: pool}), API: api.URL}
	src, _ := ParseGitHub("github:acme/skills/skills/pdf@main")
	if _, _, err := g.Fetch(t.Context(), src); err == nil || !strings.Contains(err.Error(), "reading the commit") {
		t.Errorf("err = %v", err)
	}
}

// API comes from the composition root. A value that is not a URL is a wiring
// mistake, and fails the import rather than being guessed at.
func TestAnAPIBaseThatIsNotAURLFails(t *testing.T) {
	t.Parallel()

	g := GitHub{Client: NewClient(Policy{Hosts: GitHubHosts, Allow: Public}), API: "https://api github com"}
	src, _ := ParseGitHub("github:acme/skills/skills/pdf@main")
	if _, _, err := g.Fetch(t.Context(), src); err == nil {
		t.Error("an API base with spaces in its host was used")
	}
}
