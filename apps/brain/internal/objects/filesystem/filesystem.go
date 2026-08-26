package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

const (
	dirMode  = 0o750
	fileMode = 0o600
)

var errUnsafeKey = errors.New("unsafe object key")

type store struct{ root string }

func New(root string) (objects.Store, error) {
	if err := os.MkdirAll(root, dirMode); err != nil {
		return nil, fmt.Errorf("object root %q: %w", root, err)
	}
	return &store{root: root}, nil
}

func (s *store) path(key string) (string, error) {
	switch {
	case !filepath.IsLocal(key),
		filepath.Clean(key) != key,
		key == ".",
		strings.ContainsRune(key, 0):
		return "", fmt.Errorf("%w: %q", errUnsafeKey, key)
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

func (s *store) Get(_ context.Context, key string) ([]byte, error) {
	name, err := s.path(key)
	if err != nil {
		return nil, err
	}

	body, err := os.ReadFile(name) //nolint:gosec
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", objects.ErrNotFound, key)
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	return body, nil
}

func (s *store) Put(_ context.Context, key string, body []byte) error {
	name, err := s.path(key)
	if err != nil {
		return err
	}

	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", key, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating %s: %w", key, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", key, err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", key, err)
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		return fmt.Errorf("writing %s: %w", key, err)
	}
	return nil
}

func (s *store) Delete(_ context.Context, key string) error {
	name, err := s.path(key)
	if err != nil {
		return err
	}

	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("deleting %s: %w", key, err)
	}
	return nil
}
