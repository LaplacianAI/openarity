package skill

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"
)

func ReadZip(data []byte) ([]Entry, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, errors.New("the upload is not a zip archive")
	}

	var c Collector
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || clutter(f.Name) {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file; a skill holds only files", f.Name)
		}
		if err := add(&c, f); err != nil {
			return nil, err
		}
	}
	return c.Entries(), nil
}

func add(c *Collector, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%q cannot be read from the zip: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	return c.Add(f.Name, rc)
}

func clutter(name string) bool {
	return strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store"
}
