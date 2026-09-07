package stack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

var zeroTime time.Time

const maxArtifact = 512 << 20

type Downloader struct {
	Client *http.Client
	Report Reporter
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

	raw, err := d.fetch(ctx, url, checksumURL)
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

	raw, err := d.fetch(ctx, url, checksumURL)
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
func (d *Downloader) fetch(ctx context.Context, url, checksumURL string) ([]byte, error) {
	want, err := d.checksum(ctx, checksumURL)
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

func (d *Downloader) checksum(ctx context.Context, url string) (string, error) {
	raw, err := d.get(ctx, url)
	if err != nil {
		return "", err
	}

	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "", fmt.Errorf("stack: %s held no checksum", url)
	}
	return strings.ToLower(fields[0]), nil
}

func (d *Downloader) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	res, err := d.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("stack: fetching %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stack: fetching %s: %s", url, res.Status)
	}

	var buf bytes.Buffer
	tally := &counter{total: res.ContentLength, step: StepDownload, on: d.Report}
	if _, err := io.Copy(&buf, io.TeeReader(io.LimitReader(res.Body, maxArtifact), tally)); err != nil {
		return nil, fmt.Errorf("stack: fetching %s: %w", url, err)
	}
	return buf.Bytes(), nil
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

	reader := tar.NewReader(decompressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := writeEntry(staging, header, reader); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(staging, dest)
}

func writeEntry(root string, header *tar.Header, body io.Reader) error {
	path := filepath.Join(root, filepath.FromSlash(header.Name)) //nolint:gosec // checked below

	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("stack: %s escapes the directory it is extracted into", header.Name)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(path, 0o700)

	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
		if filepath.IsAbs(header.Linkname) || strings.Contains(header.Linkname, "..") {
			return fmt.Errorf("stack: %s links outside the directory it is extracted into", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.Symlink(header.Linkname, path)

	default:
		return nil
	}
}
