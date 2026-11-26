package inmemory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

var (
	_ objects.Store  = (*store)(nil)
	_ objects.Writer = (*store)(nil)
)

type store struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func New() objects.Store { return &store{data: map[string][]byte{}} }

func (s *store) Put(_ context.Context, key string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[key] = slices.Clone(body)
	return nil
}

// PutStream reads r whole: memory is where this store keeps everything.
func (s *store) PutStream(ctx context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading %s: %w", key, err)
	}
	return s.Put(ctx, key, body)
}

func (s *store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	body, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (s *store) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	body, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", objects.ErrNotFound, key)
	}
	return slices.Clone(body), nil
}

func (s *store) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data, key)
	return nil
}
