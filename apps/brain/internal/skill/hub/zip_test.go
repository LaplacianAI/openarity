package hub

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(f, files[name])
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipHost(t *testing.T, h http.HandlerFunc) (*server, Zips) {
	t.Helper()

	s := newServer(t, h)
	return s, Zips{Client: NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: s.roots()})}
}

// --- the address ---

// What is stored is the address as given, normalised by the parser and
// nothing else.
func TestAnHTTPSAddressIsAZipSource(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"https://hub.example.com/skills/pdf.zip":      "https://hub.example.com/skills/pdf.zip",
		"https://hub.example.com:8443/pdf.zip":        "https://hub.example.com:8443/pdf.zip",
		"HTTPS://hub.example.com/pdf.zip":             "https://hub.example.com/pdf.zip",
		"https://hub.example.com/skills/my%20pdf.zip": "https://hub.example.com/skills/my%20pdf.zip",
	} {
		got, err := ParseZipURL(in)
		if err != nil || got != want {
			t.Errorf("ParseZipURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// The address is written to Postgres and shown to anyone who can read the
// skill, so a credential in it would be a secret stored where secrets never
// go. A signed link keeps its token in the query, and expires besides.
func TestAnAddressThatCarriesACredentialIsRefused(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"https://deploy:hunter2@hub.example.com/pdf.zip":                                "user or password",
		"https://token@hub.example.com/pdf.zip":                                         "user or password",
		"https://bucket.s3.amazonaws.com/pdf.zip?X-Amz-Signature=abc&X-Amz-Expires=300": "query string",
		"https://hub.example.com/pdf.zip?":                                              "query string",
		"https://hub.example.com/download?skill=pdf":                                    "query string",
	} {
		_, err := ParseZipURL(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseZipURL(%q) = %v, want it to say %q", in, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the refusal repeats the password: %v", err)
		}
	}
}

func TestAnAddressThatIsNotHTTPSIsRefused(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"", "http://hub.example.com/pdf.zip", "ftp://hub.example.com/pdf.zip", "file:///etc/passwd",
		"hub.example.com/pdf.zip", "https:///pdf.zip", "https:hub.example.com/pdf.zip", "https://%zz/pdf.zip",
	} {
		if _, err := ParseZipURL(in); err == nil || !strings.Contains(err.Error(), "https:// address") {
			t.Errorf("ParseZipURL(%q) = %v", in, err)
		}
	}
	if _, err := ParseZipURL("https://hub.example.com/pdf.zip#top"); err == nil || !strings.Contains(err.Error(), "fragment") {
		t.Errorf("a fragment: %v", err)
	}
}

// --- fetching ---

// The sha256 is of the bytes as fetched: it is what a sync compares, so it
// must change exactly when the zip does.
func TestAZipIsFetchedWithTheHashOfItsBytes(t *testing.T) {
	t.Parallel()

	body := zipOf(t, map[string]string{"pdf/SKILL.md": pdfSKILL, "pdf/scripts/fill.py": "print('fill')\n"})
	s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })

	sha, entries, err := z.Fetch(t.Context(), s.URL+"/pdf.zip")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	sum := sha256.Sum256(body)
	if sha != hex.EncodeToString(sum[:]) {
		t.Errorf("sha = %s, want the sha256 of the zip", sha)
	}
	if got := paths(entries); !slices.Equal(got, []string{"pdf/SKILL.md", "pdf/scripts/fill.py"}) {
		t.Errorf("entries = %v", got)
	}
}

// Nothing there is ErrNotFound, so the handler can answer 404; a server's
// other failures are reported with its status.
func TestAZipHostsStatusIsTranslated(t *testing.T) {
	t.Parallel()

	for status, want := range map[int]string{
		http.StatusNotFound:            "nothing is at",
		http.StatusGone:                "nothing is at",
		http.StatusForbidden:           "answered 403",
		http.StatusInternalServerError: "answered 500",
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
			_, _, err := z.Fetch(t.Context(), s.URL+"/pdf.zip")
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to say %q", err, want)
			}
			wantNotFound := status == http.StatusNotFound || status == http.StatusGone
			if errors.Is(err, ErrNotFound) != wantNotFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v", !wantNotFound, wantNotFound)
			}
		})
	}
}

// The client's refusals come through as ErrRefused, which the handler turns
// into a 400 naming the host.
func TestAHostTheOperatorHasNotAllowedIsRefused(t *testing.T) {
	t.Parallel()

	s := newServer(t, ok)
	z := Zips{Client: NewClient(Policy{Hosts: []string{"hub.example.com"}, Allow: loopbackOnly, RootCAs: s.roots()})}

	if _, _, err := z.Fetch(t.Context(), s.URL+"/pdf.zip"); !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("an unlisted host was contacted %d times", n)
	}
}

// A declared length over the cap is refused before a byte of the body is
// read: the server here promises bytes it never sends, and the fetch does not
// wait for them.
func TestADeclaredLengthOverTheCapIsRefusedUnread(t *testing.T) {
	t.Parallel()

	s, z := zipHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(maxZipBytes+1))
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Sends nothing, until the client hangs up. A handler that waited on
		// anything else would hold httptest.Server.Close, and the test, forever.
		<-r.Context().Done()
	})
	_, _, err := z.Fetch(t.Context(), s.URL+"/pdf.zip")
	if err == nil || !strings.Contains(err.Error(), "over 21 MiB") {
		t.Errorf("err = %v", err)
	}
}

// A length that is not declared, or declared falsely, is no protection: the
// cap is enforced on what arrives.
func TestAnUndeclaredStreamPastTheCapIsRefused(t *testing.T) {
	t.Parallel()

	sent := 0
	s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 1<<20)
		for sent < maxZipBytes+(4<<20) {
			n, err := w.Write(chunk)
			sent += n
			if err != nil {
				return
			}
		}
	})
	_, _, err := z.Fetch(t.Context(), s.URL+"/pdf.zip")
	if err == nil || !strings.Contains(err.Error(), "over 21 MiB") {
		t.Errorf("err = %v", err)
	}
}

func TestAZipHostThatServesSomethingElseIsRefused(t *testing.T) {
	t.Parallel()

	s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>login</html>") })
	if _, _, err := z.Fetch(t.Context(), s.URL+"/pdf.zip"); err == nil || !strings.Contains(err.Error(), "not a zip archive") {
		t.Errorf("err = %v", err)
	}
}

// The zip's contents meet the same limits as an upload's, by the same reader.
func TestAFetchedZipMeetsTheSkillLimits(t *testing.T) {
	t.Parallel()

	body := zipOf(t, map[string]string{"SKILL.md": pdfSKILL, "assets/zeros.bin": string(make([]byte, 6<<20))})
	s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	if _, _, err := z.Fetch(t.Context(), s.URL+"/pdf.zip"); err == nil || !strings.Contains(err.Error(), `"assets/zeros.bin" is over 5 MiB`) {
		t.Errorf("err = %v", err)
	}
}

func TestACancelledZipFetchStops(t *testing.T) {
	t.Parallel()

	s, z := zipHost(t, ok)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := z.Fetch(ctx, s.URL+"/pdf.zip"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("a cancelled fetch reached the host %d times", n)
	}
}

// The address comes from ParseZipURL, so one that is not a URL at all is a
// wiring mistake and fails rather than being guessed at.
func TestAnAddressThatIsNotAURLFailsTheFetch(t *testing.T) {
	t.Parallel()

	z := Zips{Client: NewClient(Policy{Hosts: []string{"hub.example.com"}, Allow: Public})}
	if _, _, err := z.Fetch(t.Context(), "https://hub example com/pdf.zip"); err == nil {
		t.Error("an address with spaces in its host was fetched")
	}
}

// Exactly the allowance is allowed: a stream as long as the cap ends in EOF,
// not in the cap's error. Every other limit in the brain is inclusive, and a
// zip of exactly the upload limit uploads.
func TestAStreamExactlyAtTheCapIsAllowed(t *testing.T) {
	t.Parallel()

	stop := errors.New("stop")
	c := &capped{r: strings.NewReader("abcd"), left: 4, err: stop}
	if got, err := io.ReadAll(c); string(got) != "abcd" || err != nil {
		t.Errorf("read %q, err %v; want all four bytes and no error", got, err)
	}
}

// Once over, always over: a caller that reads again after the cap's error
// gets the error again, never a fresh allowance. The source is one byte over,
// so it is spent when the cap trips, and a retry that consulted it would see
// a clean EOF: a stream that was refused, reported as one that ended.
func TestAStreamPastTheCapStaysRefused(t *testing.T) {
	t.Parallel()

	stop := errors.New("stop")
	c := &capped{r: strings.NewReader("abc"), left: 2, err: stop}
	if _, err := io.ReadAll(c); !errors.Is(err, stop) {
		t.Fatalf("first read: %v", err)
	}
	buf := make([]byte, 8)
	if n, err := c.Read(buf); n != 0 || !errors.Is(err, stop) {
		t.Errorf("read again: %d bytes, err %v; want nothing and the cap's error", n, err)
	}
}

// streamOf sends n zero bytes with no Content-Length, so only the cap on what
// arrives can judge them.
func streamOf(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write(make([]byte, n))
	}
}

// The cap on the wire is inclusive, as the upload limit is: a zip of exactly
// maxZipBytes is read and judged as a zip, and one byte more is not read.
func TestAZipStreamIsCappedAtExactlyItsLimit(t *testing.T) {
	t.Parallel()

	for n, want := range map[int]string{
		maxZipBytes:     "not a zip archive",
		maxZipBytes + 1: "over 21 MiB",
	} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()

			s, z := zipHost(t, streamOf(n))
			if _, _, err := z.Fetch(t.Context(), s.URL+"/pdf.zip"); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to say %q", err, want)
			}
		})
	}
}
