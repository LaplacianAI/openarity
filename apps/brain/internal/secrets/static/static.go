package static

import (
	"context"
	"fmt"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

type store map[string]map[string]string

var (
	_ secrets.Store  = store(nil)
	_ secrets.Writer = store(nil)
)

// New returns a secret store that holds whatever is put into it and nothing
// else. It survives no restart and reaches no server — the fallback for running
// the brain with no OpenBao configured.
func New() secrets.Store { return store{} }

func (s store) Get(_ context.Context, path, key string) (string, error) {
	value, ok := s[path][key]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", secrets.ErrNotFound, path, key)
	}
	return value, nil
}

func (s store) Put(_ context.Context, path, key, value string) error {
	if s == nil {
		return fmt.Errorf("%w: the static store was never created", secrets.ErrUnavailable)
	}
	if s[path] == nil {
		s[path] = map[string]string{}
	}
	s[path][key] = value
	return nil
}

func (s store) Delete(_ context.Context, path string) error {
	delete(s, path)
	return nil
}
