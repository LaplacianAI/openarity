package hub

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
)

const (
	GitHubAPI       = "https://api.github.com"
	maxTarballBytes = 64 << 20
	maxScannedBytes = 256 << 20
)

var GitHubHosts = []string{"api.github.com", "codeload.github.com"}

var ErrNotFound = errors.New("not found")

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	refPattern   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,254}$`)
	pathPattern  = regexp.MustCompile(`^[A-Za-z0-9._ -]+(/[A-Za-z0-9._ -]+)*$`)
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type GitHubSource struct {
	Owner, Repo, Path, Ref string
}

func ParseGitHub(s string) (GitHubSource, error) {
	rest, ok := strings.CutPrefix(s, "github:")
	if !ok {
		return GitHubSource{}, errors.New("a GitHub source is written github:owner/repo/path@ref")
	}

	where, ref, hasRef := strings.Cut(rest, "@")
	if !hasRef {
		ref = "HEAD"
	}
	parts := strings.SplitN(where, "/", 3)
	if len(parts) < 2 {
		return GitHubSource{}, errors.New("a GitHub source is written github:owner/repo/path@ref")
	}

	src := GitHubSource{Owner: parts[0], Repo: parts[1], Ref: ref}
	if len(parts) == 3 {
		src.Path = parts[2]
	}

	switch {
	case !ownerPattern.MatchString(src.Owner):
		return GitHubSource{}, fmt.Errorf("%q is not a GitHub owner", src.Owner)
	case !repoPattern.MatchString(src.Repo) || src.Repo == "." || src.Repo == "..":
		return GitHubSource{}, fmt.Errorf("%q is not a GitHub repository", src.Repo)
	case src.Path != "" && (!pathPattern.MatchString(src.Path) || hasDotSegment(src.Path)):
		return GitHubSource{}, fmt.Errorf("%q is not a folder in a repository", src.Path)
	case !refPattern.MatchString(src.Ref) || strings.Contains(src.Ref, "..") || strings.HasSuffix(src.Ref, "/"):
		return GitHubSource{}, fmt.Errorf("%q is not a branch, tag or commit", src.Ref)
	}

	return src, nil
}

func hasDotSegment(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

func (s GitHubSource) String() string {
	out := "github:" + s.Owner + "/" + s.Repo
	if s.Path != "" {
		out += "/" + s.Path
	}
	return out + "@" + s.Ref
}

type GitHub struct {
	Client *http.Client
	API    string
	Token  string
}

func (g GitHub) Fetch(ctx context.Context, src GitHubSource) (string, []skill.Entry, error) {
	sha, err := g.resolve(ctx, src)
	if err != nil {
		return "", nil, err
	}
	entries, err := g.download(ctx, src, sha)
	if err != nil {
		return "", nil, err
	}
	return sha, entries, nil
}

func (g GitHub) resolve(ctx context.Context, src GitHubSource) (string, error) {
	res, err := g.get(ctx, fmt.Sprintf("%s/repos/%s/%s/commits/%s",
		g.API, src.Owner, src.Repo, url.PathEscape(src.Ref)), "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(res.Body, 128))
	if err != nil {
		return "", fmt.Errorf("reading the commit from GitHub: %w", err)
	}
	sha := strings.TrimSpace(string(body))
	if !shaPattern.MatchString(sha) {
		return "", errors.New("GitHub did not answer with a commit")
	}
	return sha, nil
}

func (g GitHub) download(ctx context.Context, src GitHubSource, sha string) ([]skill.Entry, error) {
	res, err := g.get(ctx, fmt.Sprintf("%s/repos/%s/%s/tarball/%s",
		g.API, src.Owner, src.Repo, sha), "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()

	tooBig := fmt.Errorf("the repository is over %d MiB to download; import from a smaller one", maxTarballBytes>>20)
	gz, err := gzip.NewReader(&capped{r: res.Body, left: maxTarballBytes, err: tooBig})
	if err != nil {
		return nil, fmt.Errorf("GitHub's tarball cannot be read: %w", err)
	}

	tooMuch := fmt.Errorf("the repository is over %d MiB unpacked; import from a smaller one", maxScannedBytes>>20)
	return untar(tar.NewReader(&capped{r: gz, left: maxScannedBytes, err: tooMuch}), src.Path, sha)
}

func (g GitHub) get(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}

	res, err := g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusOK {
		return res, nil
	}
	_ = res.Body.Close()

	switch res.StatusCode {
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return nil, fmt.Errorf("%w: GitHub has no such repository or ref, or cannot show it without a token", ErrNotFound)
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub refused the request (%d): the token is wrong, or the rate limit is reached", res.StatusCode)
	default:
		return nil, fmt.Errorf("GitHub answered %d", res.StatusCode)
	}
}

func untar(tr *tar.Reader, dir, sha string) ([]skill.Entry, error) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}

	var c skill.Collector
	top, found := "", false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("GitHub's tarball cannot be read: %w", err)
		}

		if h.Typeflag == tar.TypeXGlobalHeader {
			if commit, ok := h.PAXRecords["comment"]; ok && commit != sha {
				return nil, errors.New("GitHub's tarball is not of the commit that was resolved")
			}
			continue
		}

		first, rest, _ := strings.Cut(h.Name, "/")
		if top == "" {
			top = first
		} else if first != top {
			return nil, errors.New("GitHub's tarball is not one repository folder")
		}

		rel, inside := strings.CutPrefix(rest, prefix)
		if !inside || rel == "" {
			found = found || (h.Typeflag == tar.TypeDir && strings.TrimSuffix(rest, "/") == dir)
			continue
		}
		found = true

		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg:
			if err := c.Add(rel, tr); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%q is not a regular file; a skill holds only files", rel)
		}
	}

	if !found {
		return nil, fmt.Errorf("%w: the repository has no folder %q at this commit", ErrNotFound, dir)
	}
	return c.Entries(), nil
}

type capped struct {
	r    io.Reader
	left int64
	err  error
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, c.err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
