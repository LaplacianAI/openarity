package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
)

type Store interface {
	Save(ctx context.Context, session string, msgs []Message) error
	Messages(ctx context.Context, session string) ([]Message, error)
	AddSteer(ctx context.Context, session, text string) error
	TakeSteers(ctx context.Context, session string) ([]string, error)
}

// SavingFrom is a Store that can keep the first messages of a transcript as
// they are stored and write only the rest. A session uses it when the store
// has it, so a save that only added messages writes only those; a store
// without it is handed every message, every time.
type SavingFrom interface {
	// SaveFrom keeps the first from messages stored for session and replaces
	// what follows with msgs[from:]. A store that does not hold from messages
	// writes them all.
	SaveFrom(ctx context.Context, session string, from int, msgs []Message) error
}

type Session interface {
	Save(ctx context.Context, msgs []Message) error
	Messages(ctx context.Context) ([]Message, error)
	Steer(ctx context.Context, text string) error
	TakeSteers(ctx context.Context) ([]string, error)
}

func Open(id string, store Store) (Session, error) {
	if id == "" {
		return nil, errors.New(
			"a session needs an id; an empty one would collect every conversation in a single bucket")
	}
	if store == nil {
		return nil, errors.New(
			"a session needs a Store; NewMemoryStore() is one that lives as long as the process")
	}
	return &session{id: id, store: store}, nil
}

type session struct {
	id    string
	store Store

	// held is a hash of each message as this session last saved or read it,
	// so a save knows where the transcript first changed.
	mu   sync.Mutex
	held [][sha256.Size]byte
}

func (s *session) Save(ctx context.Context, msgs []Message) error {
	now := hashes(msgs)

	s.mu.Lock()
	defer s.mu.Unlock()
	from := 0
	for from < len(s.held) && from < len(now) && s.held[from] == now[from] {
		from++
	}

	var err error
	if partial, ok := s.store.(SavingFrom); ok {
		err = partial.SaveFrom(ctx, s.id, from, msgs)
	} else {
		err = s.store.Save(ctx, s.id, msgs)
	}
	if err != nil {
		s.held = nil // what is stored is no longer known: the next save writes it all
		return err
	}
	s.held = now
	return nil
}

func (s *session) Messages(ctx context.Context) ([]Message, error) {
	msgs, err := s.store.Messages(ctx, s.id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.held = hashes(msgs)
	s.mu.Unlock()
	return msgs, nil
}

func hashes(msgs []Message) [][sha256.Size]byte {
	out := make([][sha256.Size]byte, len(msgs))
	for i, m := range msgs {
		// One that does not encode fails the store's own save, which encodes it too.
		raw, _ := json.Marshal(m)
		out[i] = sha256.Sum256(raw)
	}
	return out
}

func (s *session) Steer(ctx context.Context, text string) error {
	return s.store.AddSteer(ctx, s.id, text)
}

func (s *session) TakeSteers(ctx context.Context) ([]string, error) {
	return s.store.TakeSteers(ctx, s.id)
}

var _ Session = (*session)(nil)
