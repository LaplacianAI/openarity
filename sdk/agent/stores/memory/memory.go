package memory

import (
	"context"
	"slices"
	"sync"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func New() agent.Store {
	return &store{
		msgs:   make(map[string][]agent.Message),
		steers: make(map[string][]string),
	}
}

type store struct {
	mu     sync.Mutex
	msgs   map[string][]agent.Message
	steers map[string][]string
}

func (s *store) Save(_ context.Context, session string, msgs []agent.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs[session] = clone(msgs)
	return nil
}

func (s *store) Messages(_ context.Context, session string) ([]agent.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.msgs[session]), nil
}

func (s *store) AddSteer(_ context.Context, session, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steers[session] = append(s.steers[session], text)
	return nil
}

func (s *store) TakeSteers(_ context.Context, session string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.steers[session]
	delete(s.steers, session)
	return out, nil
}

func clone(msgs []agent.Message) []agent.Message {
	out := slices.Clone(msgs)
	for i, m := range out {
		out[i].Content = slices.Clone(m.Content)
		for j, c := range out[i].Content {
			if c.Blob == nil {
				continue
			}
			blob := *c.Blob
			blob.Data = slices.Clone(c.Blob.Data)
			out[i].Content[j].Blob = &blob
		}

		out[i].ToolCalls = slices.Clone(m.ToolCalls)
		for j, call := range out[i].ToolCalls {
			out[i].ToolCalls[j].Arguments = slices.Clone(call.Arguments)
		}
	}
	return out
}
