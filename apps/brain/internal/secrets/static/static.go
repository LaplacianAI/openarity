package static

import (
	"context"
	"fmt"
	"sync"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

type store struct {
	mu     sync.RWMutex
	values map[string]map[string]string
}

var (
	_ secrets.Store   = (*store)(nil)
	_ secrets.Writer  = (*store)(nil)
	_ secrets.Creator = (*store)(nil)
)

func New() secrets.Store { return &store{} }

func (s *store) set(path, key, value string) {
	if s.values == nil {
		s.values = map[string]map[string]string{}
	}
	if s.values[path] == nil {
		s.values[path] = map[string]string{}
	}
	s.values[path][key] = value
}

func (s *store) Get(_ context.Context, path, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	value, ok := s.values[path][key]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", secrets.ErrNotFound, path, key)
	}
	return value, nil
}

func (s *store) Put(_ context.Context, path, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.set(path, key, value)
	return nil
}

func (s *store) Create(_ context.Context, path, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.values[path][key]; ok {
		return fmt.Errorf("%w: %s/%s", secrets.ErrExists, path, key)
	}
	s.set(path, key, value)
	return nil
}

func (s *store) Delete(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.values, path)
	return nil
}
