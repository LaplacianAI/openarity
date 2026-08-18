package secrets

import (
	"context"
	"fmt"
)

type Static map[string]map[string]string

func (s Static) Get(_ context.Context, path, key string) (string, error) {
	value, ok := s[path][key]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", ErrNotFound, path, key)
	}
	return value, nil
}
