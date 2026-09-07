package stack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

var zeroTime time.Time

const maxArtifact = 512 << 20

type Downloader struct {
	Client *http.Client
	Report Reporter

	// Backoff is the first wait before a rate-limited fetch is tried again,
	// and doubles from there. Zero means firstBackoff. A test sets it, because
	// the real value spends thirty seconds asleep proving that a rate limit
	// which never lifts eventually gives up, and a test that slow gets deleted.
	Backoff time.Duration
}

func (d *Downloader) report(e Event) {
	if d.Report == nil {
		return
	}
	d.Report(e)
}

type counter struct {
	read  int64
	total int64
	last  int
	step  string
	on    func(Event)
}

func (c *counter) Write(p []byte) (int, error) {
	c.read += int64(len(p))
	if c.total <= 0 || c.on == nil {
		return len(p), nil
	}

	percent := int(c.read * 100 / c.total)
	// Only on a change, and only whole percents: a report per chunk is tens
	// of thousands of lines for one 325MB file, which is a progress bar that
	// costs more than the download.
	if percent != c.last {
		c.last = percent
		c.on(Event{Step: c.step, Phase: PhaseProgress, Percent: percent})
	}
	return len(p), nil
}

func (d *Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (d *Downloader) Binary(ctx context.Context, url, checksumURL, dest string) error {
	if exists(dest) {
		return nil
	}

	raw, err := d.fetch(ctx, url, checksumURL, "")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dest, raw, 0o700) //nolint:gosec // a binary the stack executes must carry the executable bit
}

func (d *Downloader) Postgres(ctx context.Context, url, checksumURL, dest string) error {
	if exists(filepath.Join(dest, "bin")) {
		return nil
	}

	raw, err := d.fetch(ctx, url, checksumURL, "")
	if err != nil {
		return err
	}

	inner, err := innerArchive(raw)
	if err != nil {
		return err
	}
	return extract(inner, dest)
}

// The checksum lives at a different address depending on who publishes it —
// Maven writes .sha256 beside the jar, MinIO writes .sha256sum — so it is
// given rather than guessed. Deriving it silently produced a 404 that read
// like the binary was missing.
func (d *Downloader) fetch(ctx context.Context, url, checksumURL, checksumName string) ([]byte, error) {
	want, err := d.checksum(ctx, checksumURL, checksumName)
	if err != nil {
		return nil, err
	}

	raw, err := d.get(ctx, url)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("stack: checksum mismatch for %s: got %s, want %s", url, got, want)
	}
	return raw, nil
}

// checksum reads a hash, from a file holding one or a file holding hundreds.
//
// Maven and MinIO publish one checksum per artefact. Node publishes a single
// SHASUMS256.txt covering every build of a release, so taking the first field
// there would verify the darwin-arm64 tarball against the hash of whatever
// happens to be listed first — which is to say, against nothing.
func (d *Downloader) checksum(ctx context.Context, url, forName string) (string, error) {
	raw, err := d.get(ctx, url)
	if err != nil {
		return "", err
	}

	if forName == "" {
		fields := strings.Fields(string(raw))
		if len(fields) == 0 {
			return "", fmt.Errorf("stack: %s held no checksum", url)
		}
		return strings.ToLower(fields[0]), nil
	}

	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Fields(line)
		// "<hash>  <name>", and the name may carry a leading * for binary
		// mode, which sha256sum writes and nobody reads.
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == forName {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("stack: %s lists no checksum for %s", url, forName)
}

// serverBusy marks a failure worth waiting out rather than reporting. Maven
// Central rate-limits by address and a CI runner shares one with everything
// else on that machine, so a 429 there is not a broken install — it is a wait.
type serverBusy struct {
	after time.Duration
	err   error
}

func (b *serverBusy) Error() string { return b.err.Error() }
func (b *serverBusy) Unwrap() error { return b.err }

const (
	fetchAttempts = 5
	firstBackoff  = 2 * time.Second
	longestWait   = 60 * time.Second
)

func (d *Downloader) get(ctx context.Context, url string) ([]byte, error) {
	backoff := d.Backoff
	if backoff <= 0 {
		backoff = firstBackoff
	}

	for attempt := 1; ; attempt++ {
		raw, err := d.getOnce(ctx, url)
		if err == nil {
			return raw, nil
		}

		// A 404 is an answer. Retrying it four times turns a wrong URL into
		// half a minute of silence before the same message.
		var busy *serverBusy
		if !errors.As(err, &busy) || attempt == fetchAttempts {
			return nil, err
		}

		wait := backoff
		if busy.after > wait {
			wait = busy.after
		}
		d.report(Event{
			Step: StepDownload, Phase: PhaseProgress,
			Detail: fmt.Sprintf("%v — trying again in %s", err, wait.Round(time.Second)),
		})

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

func (d *Downloader) getOnce(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	res, err := d.client().Do(req)
	if err != nil {
		return nil, &serverBusy{err: fmt.Errorf("stack: fetching %s: %w", url, err)}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		status := fmt.Errorf("stack: fetching %s: %s", url, res.Status)
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			return nil, &serverBusy{after: retryAfter(res.Header.Get("Retry-After")), err: status}
		}
		return nil, status
	}

	var buf bytes.Buffer
	tally := &counter{total: res.ContentLength, step: StepDownload, on: d.Report}
	if _, err := io.Copy(&buf, io.TeeReader(io.LimitReader(res.Body, maxArtifact), tally)); err != nil {
		return nil, fmt.Errorf("stack: fetching %s: %w", url, err)
	}
	return buf.Bytes(), nil
}

// Retry-After may also carry an HTTP date, which is not handled: the backoff
// covers that case anyway, and a header nobody sends is not worth a parser.
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds < 0 {
		return 0
	}
	if after := time.Duration(seconds) * time.Second; after < longestWait {
		return after
	}
	return longestWait
}

// Unpack is a runtime, fetched the way Postgres is.
//
// uv and Node both publish a gzipped tar everywhere and a zip on Windows, and
// both wrap their contents in one directory named for the version — which is
// what Strip drops, so callers get bin/node rather than
// node-v24.20.0-darwin-arm64/bin/node and nothing has to know the version
// twice.
type Archive struct {
	URL         string
	ChecksumURL string

	// The line to find, when the checksum file covers a whole release rather
	// than one artefact. Empty means the file holds one hash and nothing else.
	ChecksumName string

	Dest  string
	Strip int
}

func (d *Downloader) Unpack(ctx context.Context, a Archive) error {
	if exists(a.Dest) {
		return nil
	}

	raw, err := d.fetch(ctx, a.URL, a.ChecksumURL, a.ChecksumName)
	if err != nil {
		return err
	}

	staging := a.Dest + ".part"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	root, err := filepath.EvalSymlinks(staging)
	if err != nil {
		return err
	}
	into := &extraction{root: root, verified: map[string]bool{}, strip: a.Strip}

	if strings.HasSuffix(a.URL, ".zip") {
		err = unzipInto(into, raw)
	} else {
		err = untarInto(into, raw)
	}
	if err != nil {
		return err
	}
	if err := into.check(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(a.Dest), 0o700); err != nil {
		return err
	}
	if err := os.RemoveAll(a.Dest); err != nil {
		return err
	}
	return os.Rename(staging, a.Dest)
}

func untarInto(into *extraction, raw []byte) error {
	unzipped, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("stack: decompressing: %w", err)
	}
	defer func() { _ = unzipped.Close() }()

	reader := tar.NewReader(unzipped)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := into.entry(header, reader); err != nil {
			return err
		}
	}
}

// A zip carries a mode in ExternalAttrs on the platforms that have one and
// zeroes elsewhere, so the executable bit is taken from the entry when it has
// one and inferred from the extension when it does not. Windows is the only
// platform these zips are for, where the extension is what decides anyway.
func unzipInto(into *extraction, raw []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("stack: reading the archive: %w", err)
	}

	for _, f := range reader.File {
		if err := into.zipEntry(f); err != nil {
			return err
		}
	}
	return nil
}

func innerArchive(jar []byte) ([]byte, error) {
	reader, err := zip.NewReader(strings.NewReader(string(jar)), int64(len(jar)))
	if err != nil {
		return nil, fmt.Errorf("stack: reading the archive: %w", err)
	}

	for _, f := range reader.File {
		if !strings.HasSuffix(f.Name, ".txz") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()

		return io.ReadAll(io.LimitReader(rc, maxArtifact))
	}
	return nil, errors.New("stack: the archive holds no .txz")
}

func extract(txz []byte, dest string) error {
	decompressed, err := xz.NewReader(strings.NewReader(string(txz)))
	if err != nil {
		return fmt.Errorf("stack: decompressing: %w", err)
	}

	staging := dest + ".part"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	// Resolved once, here, so nothing below ever compares a real path against
	// an unreal one. On macOS the install root sits under /var, which is
	// itself a symlink to /private/var: a check that resolved only the entry
	// would find every path outside a root spelt the other way, and refuse an
	// archive that is entirely legitimate.
	root, err := filepath.EvalSymlinks(staging)
	if err != nil {
		return err
	}

	into := &extraction{root: root, verified: map[string]bool{}}

	reader := tar.NewReader(decompressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := into.entry(header, reader); err != nil {
			return err
		}
	}
	if err := into.check(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(staging, dest)
}

// root is already resolved and cleaned by extract, so this is a comparison of
// two real paths and needs no traversal of its own.
func pathWithinRoot(root, candidate string) bool {
	return candidate == root || strings.HasPrefix(candidate, root+string(os.PathSeparator))
}

// extraction is one archive being unpacked into one directory, carrying the
// directories it has already resolved.
//
// The cache is what makes this affordable on Windows. EvalSymlinks there opens
// each component of the path and asks the kernel for its final name, which
// Defender inspects; at one call per entry the Windows Postgres archive — 1569
// files across 74 directories — turned a four-second extraction into one that
// had not finished in sixteen minutes. Files share their parents, so resolving
// each directory once does the same work twenty times less often.
//
// Safe to cache because a directory cannot become a link mid-extraction:
// os.Symlink refuses a path that already exists, and every directory verified
// here was created by the call above it.
type extraction struct {
	root     string
	verified map[string]bool

	// Every symlink created, checked once the archive is fully unpacked.
	// See links.
	links []string

	// Leading path elements to drop. uv and Node both wrap everything in one
	// directory named for the version; the Postgres archive does not, and
	// leaves this zero.
	strip int
}

// stripped drops the leading elements, and reports whether anything is left.
// The wrapper directory itself strips to nothing and is skipped rather than
// creating the destination twice.
//
// It decides nothing about safety, deliberately. A generated fix once made it
// reject any name containing "..", which reads as a hardening and is not one:
// false here means "skip this entry", so a malicious archive stopped being
// refused and started being installed with the offending entry quietly
// missing — a runtime that looks installed and is incomplete. Four tests said
// so. Containment is decided below, where it can be refused.
func (e *extraction) stripped(name string) (string, bool) {
	clean := strings.TrimPrefix(filepath.ToSlash(name), "./")
	if e.strip == 0 {
		return clean, clean != ""
	}

	parts := strings.Split(clean, "/")
	if len(parts) <= e.strip {
		return "", false
	}
	rest := strings.Join(parts[e.strip:], "/")
	return rest, rest != ""
}

func (e *extraction) dirWithinRoot(dir string) error {
	if e.verified[dir] {
		return nil
	}

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if !pathWithinRoot(e.root, real) {
		return fmt.Errorf("stack: %s escapes the directory it is extracted into", dir)
	}

	e.verified[dir] = true
	return nil
}

func (e *extraction) entry(header *tar.Header, body io.Reader) error {
	root := e.root

	name, keep := e.stripped(header.Name)
	if !keep {
		return nil
	}

	path := filepath.Join(root, filepath.FromSlash(name)) //nolint:gosec // checked below

	// An entry naming the root itself asks for nothing to be created. `tar -cf
	// archive.tar .` produces one; none of the Postgres archives carries one,
	// which was checked rather than assumed. Handling it here keeps the guard
	// below a plain prefix test.
	if path == root {
		return nil
	}

	// Written out rather than routed through pathWithinRoot, and it has to
	// stay that way. The check is identical either way, but CodeQL recognises
	// a bare strings.HasPrefix guard where it follows neither a helper
	// returning a bool nor a condition this one is joined to. Two generated
	// fixes went through a helper and left the alert open.
	if !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return fmt.Errorf("stack: %s escapes the directory it is extracted into", header.Name)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		return e.dirWithinRoot(path)

	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := e.dirWithinRoot(filepath.Dir(path)); err != nil {
			return err
		}

		mode := os.FileMode(0o600)
		if header.FileInfo().Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}

		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode) //nolint:gosec // path is checked above against escaping root
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()

		_, err = io.Copy(file, io.LimitReader(body, maxArtifact))
		return err

	case tar.TypeSymlink:
		// Only the cheap, certain refusals here. A link may not be absolute
		// and may not be empty, because neither can ever be right and both
		// can be judged without touching the disk.
		//
		// Containment is NOT decided here, and adding a check that resolves
		// the target as each entry arrives breaks the extractor: Node writes
		// bin/corepack before lib/node_modules/corepack exists, so resolving
		// then fails with "no such file or directory" and refuses a runtime
		// that is entirely legitimate. Judging it lexically instead is the
		// mistake CodeQL's own help text describes, which let two links
		// neither of which escapes on its own compose into one that does.
		//
		// Every link is resolved once the archive is unpacked, by check(),
		// when there is a filesystem to ask. Two generated fixes have tried
		// to move it back here; TestALinkExtractedBeforeItsTargetIsAccepted
		// is what says no.
		if header.Linkname == "" || filepath.IsAbs(header.Linkname) ||
			filepath.VolumeName(header.Linkname) != "" {
			return fmt.Errorf("stack: %s links to %q, which is not a relative path",
				header.Name, header.Linkname)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := e.dirWithinRoot(filepath.Dir(path)); err != nil {
			return err
		}

		if err := os.Symlink(header.Linkname, path); err != nil {
			return err
		}
		e.links = append(e.links, path)
		return nil

	default:
		return nil
	}
}

// links resolves every symlink the archive created and refuses the lot if any
// of them leaves the root.
//
// Deferred to the end because that is the only point at which the answer is
// knowable. A link's target may be extracted after the link, so resolving as
// we go would refuse Node's bin/corepack for pointing at a file that does not
// exist yet. And resolving lexically is unsound: cleaning collapses
// subdir/parent/.. to subdir while the kernel, for which subdir/parent is a
// link, goes somewhere else entirely.
//
// Nothing is lost by waiting. Extraction writes to a staging directory that is
// renamed into place only on success, so an archive refused here leaves
// nothing behind.
func (e *extraction) check() error {
	for _, link := range e.links {
		real, err := filepath.EvalSymlinks(link)
		if err != nil {
			// A link whose target the archive never wrote. It cannot be
			// followed, so it cannot escape; the worst it can do is dangle.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if !pathWithinRoot(e.root, real) {
			return fmt.Errorf("stack: %s resolves to %s, which is outside the directory it was extracted into",
				link, real)
		}
	}
	return nil
}

// zipEntry is entry for an archive with no symlinks and no type flags. Both
// zips this reads — uv's and Node's — hold plain files and directories.
func (e *extraction) zipEntry(f *zip.File) error {
	name, keep := e.stripped(f.Name)
	if !keep {
		return nil
	}

	path := filepath.Join(e.root, filepath.FromSlash(name)) //nolint:gosec // checked below

	if path != e.root && !strings.HasPrefix(path, e.root+string(os.PathSeparator)) {
		return fmt.Errorf("stack: %s escapes the directory it is extracted into", f.Name)
	}

	if f.FileInfo().IsDir() {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		return e.dirWithinRoot(path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := e.dirWithinRoot(filepath.Dir(path)); err != nil {
		return err
	}

	mode := os.FileMode(0o600)
	if f.Mode().Perm()&0o111 != 0 || strings.HasSuffix(name, ".exe") {
		mode = 0o700
	}

	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode) //nolint:gosec // path is checked above against escaping root
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	body, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	_, err = io.Copy(out, io.LimitReader(body, maxArtifact))
	return err
}
