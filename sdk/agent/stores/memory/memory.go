package memory

import (
	"context"
	"fmt"
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

func (s *store) Save(ctx context.Context, session string, msgs []agent.Message) error {
	return s.SaveFrom(ctx, session, 0, msgs)
}

// SaveFrom keeps the first from messages as they are held and replaces the
// rest. Copying everything would do as well here; it is what storetest
// exercises the path through.
func (s *store) SaveFrom(_ context.Context, session string, from int, msgs []agent.Message) error {
	if from < 0 || from > len(msgs) {
		return fmt.Errorf("saving session %s: from %d is outside its %d messages", session, from, len(msgs))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.msgs[session]
	if from > len(kept) {
		from = 0 // what is kept must be whole: anything else is written again
	}
	s.msgs[session] = append(kept[:from:from], clone(msgs[from:])...)
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

		if m.Compaction != nil {
			summary := *m.Compaction
			summary.User = slices.Clone(summary.User)
			summary.References = slices.Clone(summary.References)
			summary.Skills = slices.Clone(summary.Skills)
			out[i].Compaction = &summary
		}
	}
	return out
}
