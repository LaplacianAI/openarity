package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func githubHub(f *fakeGitHub, token func(context.Context) (string, error)) Hub {
	return Hub{GitHub: f.client(""), Token: token}
}

func tokens(values ...string) (func(context.Context) (string, error), *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context) (string, error) {
		n := calls.Add(1)
		return values[min(int(n), len(values))-1], nil
	}, &calls
}

// --- which source ---

// What comes back is exactly what the skills row stores: the kind the
// source CHECK allows, the ref as it will be fetched again by a sync, and the
// commit the ref named at the time.
func TestAGitHubSourceIsImportedAsGitHub(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	imp, err := githubHub(f, nil).Fetch(t.Context(), "github:acme/skills/skills/pdf")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if imp.Kind != "github" || imp.Ref != "github:acme/skills/skills/pdf@HEAD" || imp.SHA != commit {
		t.Errorf("import = %q %q %q; want github, the ref with @HEAD, and the commit", imp.Kind, imp.Ref, imp.SHA)
	}
	if got := paths(imp.Entries); !slices.Equal(got, []string{"SKILL.md", "scripts/fill.py"}) {
		t.Errorf("entries = %v", got)
	}
}

func TestAnHTTPSSourceIsImportedAsAZip(t *testing.T) {
	t.Parallel()

	body := zipOf(t, map[string]string{"pdf/SKILL.md": pdfSKILL})
	s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	address := s.URL + "/pdf.zip"

	imp, err := Hub{Zips: z}.Fetch(t.Context(), "HTTPS"+strings.TrimPrefix(address, "https"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if imp.Kind != "url" || imp.Ref != address || len(imp.SHA) != 64 {
		t.Errorf("import = %q %q %q; want url, the normalised address, and a sha256", imp.Kind, imp.Ref, imp.SHA)
	}
	if got := paths(imp.Entries); !slices.Equal(got, []string{"pdf/SKILL.md"}) {
		t.Errorf("entries = %v", got)
	}
}

// Anything that is neither form is refused with a sentence naming both, and
// nothing is fetched.
func TestASourceOfNeitherFormIsInvalid(t *testing.T) {
	t.Parallel()

	s, z := zipHost(t, ok)
	h := Hub{Zips: z}
	for _, in := range []string{
		"", "gitlab:acme/skills/pdf", "GitHub:acme/skills/pdf", " github:acme/skills/pdf",
		"http://hub.example.com/pdf.zip", "ftp://hub.example.com/pdf.zip", "pdf.zip", "https:/x",
	} {
		_, err := h.Fetch(t.Context(), in)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "github:owner/repo/path@ref") {
			t.Errorf("Fetch(%q) = %v, want ErrInvalid naming both forms", in, err)
		}
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("an invalid source reached a host %d times", n)
	}
}

// Each parser's refusal is kept, sentence and all, and marked invalid rather
// than unavailable: it is the caller's to fix, and nothing was fetched.
func TestAMalformedSourceIsInvalidAndFetchesNothing(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	s, z := zipHost(t, ok)
	h := Hub{GitHub: f.client(""), Zips: z}

	for in, want := range map[string]string{
		"github:acme/skills/../pdf@main":                   "not a folder in a repository",
		"github:acme":                                      "github:owner/repo/path@ref",
		"github:acme/skills/pdf@..":                        "not a branch, tag or commit",
		"https://hub.example.com/pdf.zip?X-Amz-Signature=": "query string",
		"https://user:pw@hub.example.com/pdf.zip":          "user or password",
	} {
		_, err := h.Fetch(t.Context(), in)
		if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), want) {
			t.Errorf("Fetch(%q) = %v, want ErrInvalid saying %q", in, err, want)
		}
	}
	if len(f.apiURIs) != 0 || s.hits.Load() != 0 {
		t.Errorf("a malformed source was fetched: %v, %d", f.apiURIs, s.hits.Load())
	}
}

// --- the token ---

// The token is asked for on every import, so rotating it in the secret store
// takes effect on the next one without a restart.
func TestTheTokenIsReadOnEveryImport(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	token, calls := tokens("first", "second")
	h := githubHub(f, token)

	for range 2 {
		if _, err := h.Fetch(t.Context(), "github:acme/skills/skills/pdf@main"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("the token was read %d times for two imports", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.apiAuth) != 4 || f.apiAuth[0] != "Bearer first" || f.apiAuth[3] != "Bearer second" {
		t.Errorf("Authorization sent = %q", f.apiAuth)
	}
}

// A token that cannot be read stops the import before GitHub is asked, so an
// unauthenticated request never spends the anonymous rate limit or sees a
// private repository as missing.
func TestATokenThatCannotBeReadStopsTheImport(t *testing.T) {
	t.Parallel()

	f := newFakeGitHub(t, aRepository(t, commit))
	sealed := errors.New("secret store sealed")
	h := githubHub(f, func(context.Context) (string, error) { return "", sealed })

	_, err := h.Fetch(t.Context(), "github:acme/skills/skills/pdf@main")
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, sealed) || !strings.Contains(err.Error(), "GitHub token") {
		t.Errorf("err = %v, want ErrUnavailable wrapping the store's error", err)
	}
	if len(f.apiURIs) != 0 {
		t.Errorf("GitHub was asked without the token: %v", f.apiURIs)
	}
}

// The GitHub token is GitHub's: a zip import never reads it, so it cannot
// leak to the operator's zip hosts and a sealed store does not stop them.
func TestAZipImportNeverReadsTheGitHubToken(t *testing.T) {
	t.Parallel()

	body := zipOf(t, map[string]string{"SKILL.md": pdfSKILL})
	s, z := zipHost(t, func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			t.Errorf("the zip host was sent Authorization %q", a)
		}
		_, _ = w.Write(body)
	})
	token, calls := tokens("secret")

	if _, err := (Hub{Zips: z, Token: token}).Fetch(t.Context(), s.URL+"/pdf.zip"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("a zip import read the GitHub token %d times", n)
	}
}

// --- whose failure it is ---

// The handler answers 502 for ErrUnavailable and 4xx for the rest, so every
// failure has to land on the right side: the source failing to answer, or
// answering with something the brain will not take.
func TestAGitHubFailureIsMarkedByWhoseItIs(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		commits, tarball int
		unavailable      bool
	}{
		"no such repository": {commits: http.StatusNotFound},
		"no such ref":        {commits: http.StatusUnprocessableEntity},
		"a bad token":        {commits: http.StatusUnauthorized, unavailable: true},
		"rate limited":       {commits: http.StatusForbidden, unavailable: true},
		"too many":           {commits: http.StatusTooManyRequests, unavailable: true},
		"an outage":          {commits: http.StatusBadGateway, unavailable: true},
		"the tarball fails":  {tarball: http.StatusInternalServerError, unavailable: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFakeGitHub(t, aRepository(t, commit))
			f.commits, f.tarball = tc.commits, tc.tarball
			_, err := githubHub(f, nil).Fetch(t.Context(), "github:acme/skills/skills/pdf@main")
			if err == nil {
				t.Fatal("no error")
			}
			if errors.Is(err, ErrUnavailable) != tc.unavailable {
				t.Errorf("errors.Is(%v, ErrUnavailable) = %v, want %v", err, !tc.unavailable, tc.unavailable)
			}
		})
	}
}

func TestAZipHostsFailureIsMarkedByWhoseItIs(t *testing.T) {
	t.Parallel()

	for status, unavailable := range map[int]bool{
		http.StatusNotFound:            false,
		http.StatusGone:                false,
		http.StatusForbidden:           true,
		http.StatusInternalServerError: true,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
			_, err := Hub{Zips: z}.Fetch(t.Context(), s.URL+"/pdf.zip")
			if err == nil || errors.Is(err, ErrUnavailable) != unavailable {
				t.Errorf("err = %v, want ErrUnavailable %v", err, unavailable)
			}
		})
	}
}

// A connection that drops mid-download is the source's failure, however
// deep in gzip, tar or the collector the read was when it happened.
func TestADownloadCutOffMidBodyIsUnavailable(t *testing.T) {
	t.Parallel()

	t.Run("github", func(t *testing.T) {
		t.Parallel()

		tarball := aRepository(t, commit)
		f := newFakeGitHub(t, tarball)
		f.serve = func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", strconv.Itoa(len(tarball)))
			_, _ = w.Write(tarball[:len(tarball)/2])
		}
		_, err := githubHub(f, nil).Fetch(t.Context(), "github:acme/skills/skills/pdf@main")
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})

	t.Run("the commit", func(t *testing.T) {
		t.Parallel()

		s := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "40")
			_, _ = io.WriteString(w, commit[:10])
		})
		g := GitHub{Client: NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: s.roots()}), API: s.URL}
		_, err := Hub{GitHub: g}.Fetch(t.Context(), "github:acme/skills/skills/pdf@main")
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})

	t.Run("zip", func(t *testing.T) {
		t.Parallel()

		body := zipOf(t, map[string]string{"SKILL.md": pdfSKILL})
		s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body[:len(body)/2])
		})
		_, err := Hub{Zips: z}.Fetch(t.Context(), s.URL+"/pdf.zip")
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
}

// A download that arrived whole and was refused is about what it holds, not
// about whether it arrived. That includes the caps, which the caller can fix
// by importing something smaller.
func TestContentTheBrainRefusesIsNotUnavailable(t *testing.T) {
	t.Parallel()

	t.Run("over a skill's limit", func(t *testing.T) {
		t.Parallel()

		f := newFakeGitHub(t, tarballOf(t, commit,
			file(topFolder+"skills/pdf/SKILL.md", pdfSKILL),
			file(topFolder+"skills/pdf/big.bin", string(make([]byte, 6<<20))),
		))
		_, err := githubHub(f, nil).Fetch(t.Context(), "github:acme/skills/skills/pdf@main")
		if err == nil || errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want a refusal that is not ErrUnavailable", err)
		}
	})

	t.Run("too large to download", func(t *testing.T) {
		t.Parallel()

		f := newFakeGitHub(t, nil)
		f.serve = streamBig(0, maxTarballBytes+(1<<20))
		_, err := githubHub(f, nil).Fetch(t.Context(), "github:acme/skills/skills/pdf@main")
		if err == nil || !strings.Contains(err.Error(), "to download") || errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want the download cap and not ErrUnavailable", err)
		}
	})

	t.Run("not gzip", func(t *testing.T) {
		t.Parallel()

		f := newFakeGitHub(t, []byte("<html>not a tarball</html>"))
		_, err := githubHub(f, nil).Fetch(t.Context(), "github:acme/skills/skills/pdf@main")
		if err == nil || errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want a refusal that is not ErrUnavailable", err)
		}
	})

	t.Run("not a zip", func(t *testing.T) {
		t.Parallel()

		s, z := zipHost(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>login</html>") })
		_, err := Hub{Zips: z}.Fetch(t.Context(), s.URL+"/pdf.zip")
		if err == nil || errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want a refusal that is not ErrUnavailable", err)
		}
	})

	t.Run("a zip too large", func(t *testing.T) {
		t.Parallel()

		s, z := zipHost(t, streamOf(maxZipBytes+1))
		_, err := Hub{Zips: z}.Fetch(t.Context(), s.URL+"/pdf.zip")
		if err == nil || !strings.Contains(err.Error(), "over 21 MiB") || errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want the zip cap and not ErrUnavailable", err)
		}
	})
}

// A host the operator did not allow is the caller's to fix, not an outage:
// it must reach the handler as ErrRefused and nothing else, or a 502 would
// tell the caller to try again later.
func TestARefusedHostIsNotUnavailable(t *testing.T) {
	t.Parallel()

	s := newServer(t, ok)
	z := Zips{Client: NewClient(Policy{Hosts: []string{"hub.example.com"}, Allow: loopbackOnly, RootCAs: s.roots()})}

	_, err := Hub{Zips: z}.Fetch(t.Context(), s.URL+"/pdf.zip")
	if !errors.Is(err, ErrRefused) || errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrRefused and not ErrUnavailable", err)
	}
}

// A host that cannot be reached at all is unavailable, on either path.
func TestAHostThatDoesNotAnswerIsUnavailable(t *testing.T) {
	t.Parallel()

	s := newServer(t, ok)
	s.Close()
	client := NewClient(Policy{Hosts: []string{"127.0.0.1"}, Allow: loopbackOnly, RootCAs: s.roots()})

	h := Hub{GitHub: GitHub{Client: client, API: s.URL}, Zips: Zips{Client: client}}
	for _, source := range []string{"github:acme/skills/skills/pdf@main", s.URL + "/pdf.zip"} {
		if _, err := h.Fetch(t.Context(), source); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Fetch(%q) = %v, want ErrUnavailable", source, err)
		}
	}
}

// --- the marker ---

// EOF is how a reader says it is done, and io.ReadAll compares it with ==.
// Wrapped, every complete download would read as a failure.
func TestUpstreamPassesEOFThroughUnwrapped(t *testing.T) {
	t.Parallel()

	u := upstream{r: strings.NewReader("")}
	if _, err := u.Read(make([]byte, 1)); err != io.EOF { //nolint:errorlint // identity is the point
		t.Errorf("err = %v, want io.EOF itself", err)
	}
}
