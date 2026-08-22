package secrets

import (
	"context"
	"fmt"
)

type Static map[string]map[string]string

var (
	_ Store  = Static(nil)
	_ Writer = Static(nil)
)

func (s Static) Get(_ context.Context, path, key string) (string, error) {
	value, ok := s[path][key]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", ErrNotFound, path, key)
	}
	return value, nil
}

func (s Static) Put(_ context.Context, path, key, value string) error {
	if s == nil {
		return fmt.Errorf("%w: the static store was never created", ErrUnavailable)
	}
	if s[path] == nil {
		s[path] = map[string]string{}
	}
	s[path][key] = value
	return nil
}

func (s Static) Delete(_ context.Context, path string) error {
	delete(s, path)
	return nil
}
