package skill

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

func ReadZip(data []byte) ([]Entry, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, errors.New("the upload is not a zip archive")
	}

	var entries []Entry
	total := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || clutter(f.Name) {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file; a skill holds only files", f.Name)
		}
		if len(entries) == MaxFiles {
			return nil, fmt.Errorf("a skill holds at most %d files", MaxFiles)
		}

		b, err := inflate(f, min(MaxFileBytes, MaxSkillBytes-total))
		if err != nil {
			return nil, err
		}
		if len(b) > MaxFileBytes {
			return nil, fmt.Errorf("%q is over %d MiB", f.Name, MaxFileBytes>>20)
		}
		total += len(b)
		if total > MaxSkillBytes {
			return nil, fmt.Errorf("a skill is at most %d MiB", MaxSkillBytes>>20)
		}
		entries = append(entries, Entry{Path: f.Name, Data: b})
	}

	return entries, nil
}

func inflate(f *zip.File, limit int) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%q cannot be read from the zip: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	b, err := io.ReadAll(io.LimitReader(rc, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%q cannot be read from the zip: %w", f.Name, err)
	}
	return b, nil
}

func clutter(name string) bool {
	return strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store"
}
